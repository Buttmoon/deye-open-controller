package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var appLogMu sync.Mutex

func (a *App) ensureLogDir() error { return os.MkdirAll(filepath.Dir(a.appLogPath), 0755) }

func logLevelWeight(level string) int {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return 10
	case "INFO":
		return 20
	case "WARN":
		return 30
	case "ERROR":
		return 40
	default:
		return 20
	}
}

func (a *App) shouldLog(level string) bool {
	settings, err := a.getSettings()
	if err != nil {
		return true
	}
	return logLevelWeight(level) >= logLevelWeight(settings.LogLevel)
}

func (a *App) appendAppLog(level, message string, fields map[string]any) {
	settings, err := a.getSettings()
	if err == nil && logLevelWeight(level) < logLevelWeight(settings.LogLevel) {
		return
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	entry := map[string]any{"ts_utc": stamp, "level": strings.ToUpper(level), "message": message}
	for k, v := range fields {
		entry[k] = v
	}
	payload, _ := json.Marshal(entry)
	log.Println(string(payload))

	if err == nil && !settings.FileLoggingEnabled {
		return
	}
	if err := a.ensureLogDir(); err != nil {
		return
	}
	appLogMu.Lock()
	defer appLogMu.Unlock()
	f, err := os.OpenFile(a.appLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, string(payload))
	_ = f.Close()
	if err == nil && settings.AppLogMaxSizeMB > 0 {
		_ = a.rotateAppLogIfNeeded(settings.AppLogMaxSizeMB)
	}
}

func (a *App) rotateAppLogIfNeeded(maxMB int) error {
	info, err := os.Stat(a.appLogPath)
	if err != nil {
		return nil
	}
	limit := int64(maxMB) * 1024 * 1024
	if info.Size() <= limit {
		return nil
	}
	data, err := os.ReadFile(a.appLogPath)
	if err != nil {
		return err
	}
	if int64(len(data)) > limit/2 {
		data = data[len(data)-int(limit/2):]
		if idx := strings.Index(string(data), "\n"); idx >= 0 {
			data = data[idx+1:]
		}
	}
	return os.WriteFile(a.appLogPath, data, 0644)
}

func (a *App) readAppLogs(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	if err := a.ensureLogDir(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(a.appLogPath); os.IsNotExist(err) {
		return []string{}, nil
	}
	return tailFileLines(a.appLogPath, limit)
}

func tailFileLines(path string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return []string{}, nil
	}

	const blockSize int64 = 32 * 1024
	pos := info.Size()
	buf := make([]byte, 0, minInt64(info.Size(), blockSize*4))
	newlineCount := 0

	for pos > 0 && newlineCount <= limit {
		readSize := blockSize
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		block := make([]byte, readSize)
		if _, err := f.ReadAt(block, pos); err != nil {
			return nil, err
		}
		newlineCount += strings.Count(string(block), "\n")
		buf = append(block, buf...)
	}

	text := strings.TrimRight(string(buf), "\r\n")
	if text == "" {
		return []string{}, nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (a *App) clearAppLogs() error {
	if err := a.ensureLogDir(); err != nil {
		return err
	}
	appLogMu.Lock()
	defer appLogMu.Unlock()
	return os.WriteFile(a.appLogPath, []byte{}, 0644)
}
