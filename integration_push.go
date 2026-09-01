package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var integrationPushMu sync.Mutex

type integrationPushState struct {
	LastRunUTC   time.Time
	LastStatus   string
	LastMessage  string
	LastDuration int64
	NextRunUTC   time.Time
	Running      bool
}

func (a *App) startIntegrationPusher() {
	a.integrationPusherCtlMu.Lock()
	defer a.integrationPusherCtlMu.Unlock()
	if a.integrationPusherStopCh != nil {
		a.appendAppLog("warn", "integration pusher start skipped: already running", map[string]any{"component": "integration"})
		return
	}
	stopCh := make(chan struct{})
	a.integrationPusherStopCh = stopCh
	a.appendAppLog("info", "integration pusher started", map[string]any{"component": "integration"})
	go a.integrationPusherLoop(stopCh)
}

func (a *App) stopIntegrationPusher() {
	a.integrationPusherCtlMu.Lock()
	defer a.integrationPusherCtlMu.Unlock()
	if a.integrationPusherStopCh != nil {
		close(a.integrationPusherStopCh)
		a.integrationPusherStopCh = nil
		a.appendAppLog("info", "integration pusher stopped", map[string]any{"component": "integration"})
	}
}

func (a *App) restartIntegrationPusher(reason string) {
	a.appendAppLog("info", "integration pusher restart requested", map[string]any{"component": "integration", "reason": reason})
	a.stopIntegrationPusher()
	a.startIntegrationPusher()
}

func (a *App) integrationPusherLoop(stopCh <-chan struct{}) {
	for {
		settings, err := a.getSettings()
		interval := 300
		if err != nil {
			a.appendAppLog("error", "integration pusher settings read failed", map[string]any{"component": "integration", "error": err.Error()})
		} else {
			interval = settings.IntegrationPushIntervalSeconds
		}

		a.appendAppLog("info", "integration pusher loop tick", map[string]any{
			"component":        "integration",
			"interval_seconds": interval,
			"url":              settings.IntegrationURL,
		})

		if err == nil && settings.IntegrationURL != "" {
			a.runIntegrationPushOnce("auto")
		}

		alignedNow := time.Now()
		waitSec := int64(interval) - (alignedNow.Unix() % int64(interval))
		if waitSec <= 0 {
			waitSec = int64(interval)
		}
		nextRun := time.Now().Add(time.Duration(waitSec) * time.Second)
		a.integrationPushState.NextRunUTC = nextRun.UTC()

		timer := time.NewTimer(time.Duration(waitSec) * time.Second)
		select {
		case <-stopCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			a.appendAppLog("info", "integration pusher goroutine stopped", map[string]any{"component": "integration"})
			return
		case <-timer.C:
		}
	}
}

func (a *App) runIntegrationPushOnce(command string) {
	if !integrationPushMu.TryLock() {
		a.appendAppLog("warn", "integration push skipped: previous cycle still running", map[string]any{"component": "integration", "command": command})
		return
	}
	defer integrationPushMu.Unlock()

	started := time.Now()
	a.integrationPushState.Running = true
	a.integrationPushState.LastRunUTC = time.Now().UTC()

	settings, err := a.getSettings()
	if err != nil {
		a.integrationPushState.LastStatus = "error"
		a.integrationPushState.LastMessage = "Ошибка чтения настроек: " + err.Error()
		a.integrationPushState.Running = false
		return
	}

	if settings.IntegrationURL == "" {
		a.integrationPushState.LastStatus = "skipped"
		a.integrationPushState.LastMessage = "URL интеграции не задан"
		a.integrationPushState.Running = false
		return
	}

	latestLog, err := a.getLatestInverterLogJSON()
	if err != nil {
		a.integrationPushState.LastStatus = "error"
		a.integrationPushState.LastMessage = "Ошибка чтения лога: " + err.Error()
		a.integrationPushState.LastDuration = time.Since(started).Milliseconds()
		a.integrationPushState.Running = false
		a.appendAppLog("error", "integration push: failed to read latest log", map[string]any{"component": "integration", "error": err.Error()})
		return
	}

	payload := map[string]any{
		"server_name": settings.IntegrationServerName,
		"server_ip":   settings.IntegrationServerIP,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"log":         latestLog,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		a.integrationPushState.LastStatus = "error"
		a.integrationPushState.LastMessage = "Ошибка marshal payload: " + err.Error()
		a.integrationPushState.LastDuration = time.Since(started).Milliseconds()
		a.integrationPushState.Running = false
		return
	}

	req, err := http.NewRequest(http.MethodPost, settings.IntegrationURL, bytes.NewReader(body))
	if err != nil {
		a.integrationPushState.LastStatus = "error"
		a.integrationPushState.LastMessage = "Ошибка создания запроса: " + err.Error()
		a.integrationPushState.LastDuration = time.Since(started).Milliseconds()
		a.integrationPushState.Running = false
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	if settings.IntegrationAuthEnabled {
		switch settings.IntegrationAuthScheme {
		case "bearer":
			if settings.IntegrationAuthToken != "" {
				req.Header.Set("Authorization", "Bearer "+settings.IntegrationAuthToken)
			}
		case "basic":
			if settings.IntegrationAuthUsername != "" || settings.IntegrationAuthPassword != "" {
				req.SetBasicAuth(settings.IntegrationAuthUsername, settings.IntegrationAuthPassword)
			}
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		a.integrationPushState.LastStatus = "error"
		a.integrationPushState.LastMessage = fmt.Sprintf("Ошибка отправки: %v", err)
		a.integrationPushState.LastDuration = time.Since(started).Milliseconds()
		a.integrationPushState.Running = false
		a.appendAppLog("error", "integration push: request failed", map[string]any{"component": "integration", "url": settings.IntegrationURL, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		a.integrationPushState.LastStatus = "ok"
		a.integrationPushState.LastMessage = fmt.Sprintf("Отправлено: HTTP %d", resp.StatusCode)
		a.appendAppLog("info", "integration push: success", map[string]any{"component": "integration", "url": settings.IntegrationURL, "status": resp.StatusCode})
	} else {
		a.integrationPushState.LastStatus = "error"
		a.integrationPushState.LastMessage = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody))
		a.appendAppLog("warn", "integration push: non-2xx response", map[string]any{"component": "integration", "url": settings.IntegrationURL, "status": resp.StatusCode, "body": string(respBody)})
	}
	a.integrationPushState.LastDuration = time.Since(started).Milliseconds()
	a.integrationPushState.Running = false
}

func (a *App) getLatestInverterLogJSON() (map[string]any, error) {
	return a.getLatestInverterLogJSONForInverterID(0)
}

func (a *App) getLatestInverterLogJSONForInverterID(inverterID int64) (map[string]any, error) {
	files, err := listInverterLogFiles()
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("логи инвертора не найдены")
	}

	now := time.Now()
	bestRecord := map[string]any{}
	bestDiff := time.Duration(1<<63 - 1)
	found := false

	for _, f := range files {
		filePath := inverterLogDir + "/" + f.Name
		records, err := readAllLogRecords(filePath)
		if err != nil {
			continue
		}
		for _, rec := range records {
			if inverterID > 0 && logRecordInverterID(rec) != inverterID {
				continue
			}
			t := parseLogRecordTime(rec)
			if t.IsZero() {
				continue
			}
			diff := now.Sub(t)
			if diff < 0 {
				diff = -diff
			}
			if diff < bestDiff {
				bestDiff = diff
				bestRecord = rec
				found = true
			}
		}
		if found && bestDiff < 5*time.Minute {
			break
		}
	}

	if !found {
		if inverterID > 0 {
			return nil, fmt.Errorf("подходящая запись для inverter_id=%d не найдена", inverterID)
		}
		return nil, fmt.Errorf("подходящая запись не найдена (сейчас %s)", now.Format("15:04:05"))
	}

	bestRecord["_match_diff_seconds"] = int(bestDiff.Seconds())
	return bestRecord, nil
}

func logRecordInverterID(record map[string]any) int64 {
	value, ok := record["inverter_id"]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		parsed, _ := v.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return parsed
	default:
		return 0
	}
}

func readAllLogRecords(filePath string) ([]map[string]any, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var records []map[string]any
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 1024, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records, scanner.Err()
}
