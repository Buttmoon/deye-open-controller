package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/simonvetter/modbus"
	"inverter-schedule/internal/models"
)

const inverterLogDir = "data/inverter_logs"

var inverterLoggingRunMu sync.Mutex
var inverterLogFileMu sync.Mutex

type activeInverterTarget struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	IP             string `json:"ip"`
	Port           int    `json:"port"`
	ModelKey       string `json:"model_key"`
	ModelName      string `json:"model_name"`
	ParametersFile string `json:"parameters_file"`
}

func (a *App) startInverterLogger() {
	a.inverterLogCtlMu.Lock()
	defer a.inverterLogCtlMu.Unlock()
	if a.inverterLogStopCh != nil {
		a.appendAppLog("warn", "inverter logger start skipped: already running", map[string]any{"component": "inverter_logger"})
		return
	}
	stopCh := make(chan struct{})
	a.inverterLogStopCh = stopCh
	a.appendAppLog("info", "inverter logger start requested", map[string]any{"component": "inverter_logger"})
	go a.inverterLoggerLoop(stopCh)
}

func (a *App) stopInverterLogger() {
	a.inverterLogCtlMu.Lock()
	defer a.inverterLogCtlMu.Unlock()
	if a.inverterLogStopCh != nil {
		close(a.inverterLogStopCh)
		a.inverterLogStopCh = nil
		a.appendAppLog("info", "inverter logger stop requested", map[string]any{"component": "inverter_logger"})
	}
}

func (a *App) restartInverterLogger(reason string) {
	a.appendAppLog("info", "inverter logger restart requested", map[string]any{"reason": reason, "component": "inverter_logger"})
	a.stopInverterLogger()
	a.startInverterLogger()
}

func (a *App) inverterLoggerLoop(stopCh <-chan struct{}) {
	a.appendAppLog("info", "inverter logger goroutine started", map[string]any{"component": "inverter_logger"})
	for {
		settings, err := a.getSettings()
		interval := 60
		if err != nil {
			a.appendAppLog("error", "inverter logger settings read failed; fallback interval used", map[string]any{"component": "inverter_logger", "error": err.Error(), "fallback_interval_seconds": interval})
		} else if settings.InverterLoggingIntervalSeconds > 0 {
			interval = settings.InverterLoggingIntervalSeconds
		}

		a.appendAppLog("info", "inverter logger loop tick", map[string]any{
			"component":        "inverter_logger",
			"enabled":          err == nil && settings.InverterLoggingEnabled,
			"interval_seconds": interval,
		})

		if err == nil && settings.InverterLoggingEnabled {
			a.runInverterLoggingOnce(settings, "auto")
		} else {
			message := "Логирование инвертора выключено"
			if err != nil {
				message = "Ошибка чтения настроек логирования: " + err.Error()
			}
			if !a.inverterLogState.Running {
				a.setInverterLogIdleState("disabled", message)
			}
			a.appendAppLog("info", "inverter logger cycle skipped", map[string]any{"component": "inverter_logger", "reason": message})
		}

		a.inverterLogState.NextRunUTC = time.Now().UTC().Add(time.Duration(interval) * time.Second)
		a.appendAppLog("info", "inverter logger next run planned", map[string]any{"component": "inverter_logger", "next_run_utc": a.inverterLogState.NextRunUTC.Format(time.RFC3339), "interval_seconds": interval})
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-stopCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			a.appendAppLog("info", "inverter logger goroutine stopped", map[string]any{"component": "inverter_logger"})
			return
		case <-timer.C:
		}
	}
}

type inverterLogTargetResult struct {
	Status       string
	Message      string
	FileName     string
	ReadCount    int
	ActiveParams int
	ZeroCount    int
	Errors       []string
	DurationMS   int64
}

func (a *App) runInverterLoggingOnce(settings models.Settings, command string) {
	inverterLoggingRunMu.Lock()
	defer inverterLoggingRunMu.Unlock()

	started := time.Now()
	a.markInverterLogRunning("Подготовка запуска логирования всех инверторов", command)
	a.appendAppLog("info", "inverter logging batch started", map[string]any{
		"component":        "inverter_logger",
		"command":          command,
		"interval_seconds": settings.InverterLoggingIntervalSeconds,
		"max_file_mb":      settings.InverterLogMaxSizeMB,
	})

	if err := os.MkdirAll(inverterLogDir, 0755); err != nil {
		msg := "Ошибка создания каталога логов: " + err.Error()
		a.setInverterLogState("error", msg, "", 0)
		a.setInverterLogErrors([]string{err.Error()}, 0, 0)
		return
	}

	a.setInverterLogStep("Получение списка зарегистрированных инверторов")
	targets, err := a.getActiveInverterTargets()
	if err != nil {
		msg := "Инверторы для логирования не найдены: " + err.Error()
		a.setInverterLogState("error", msg, "", 0)
		a.setInverterLogErrors([]string{msg}, 0, 0)
		a.appendAppLog("warn", "inverter logging skipped: no configured inverters", map[string]any{"component": "inverter_logger", "error": err.Error(), "command": command})
		return
	}

	totalRead := 0
	totalActive := 0
	totalZero := 0
	allErrors := make([]string, 0)
	lastFile := ""
	warnCount := 0
	errorCount := 0

	for index, target := range targets {
		a.inverterLogState.Running = true
		a.setInverterLogStep(fmt.Sprintf("Опрос инвертора %d/%d: %s", index+1, len(targets), target.Name))
		result := a.runInverterLoggingTargetOnce(settings, command, target, index+1, len(targets))
		totalRead += result.ReadCount
		totalActive += result.ActiveParams
		totalZero += result.ZeroCount
		if result.FileName != "" {
			lastFile = result.FileName
		}
		for _, item := range result.Errors {
			allErrors = appendLimitedError(allErrors, fmt.Sprintf("%s (%s:%d): %s", target.Name, target.IP, target.Port, item))
		}
		switch result.Status {
		case "error":
			errorCount++
		case "warn":
			warnCount++
		}
	}

	status := "ok"
	message := fmt.Sprintf("Логирование выполнено для %d инверторов: прочитано %d/%d параметров, нулей %d", len(targets), totalRead, totalActive, totalZero)
	if errorCount == len(targets) {
		status = "error"
		message = fmt.Sprintf("Логирование не выполнено ни для одного из %d инверторов", len(targets))
	} else if errorCount > 0 || warnCount > 0 || totalZero > 0 {
		status = "warn"
		message = fmt.Sprintf("Логирование выполнено частично для %d инверторов: ошибок целей %d, предупреждений %d, прочитано %d/%d, нулей %d", len(targets), errorCount, warnCount, totalRead, totalActive, totalZero)
	}

	a.setInverterLogState(status, message, lastFile, totalRead)
	a.setInverterLogErrors(allErrors, totalActive, totalZero)
	a.appendAppLog("info", "inverter logging batch completed", map[string]any{
		"component":       "inverter_logger",
		"status":          status,
		"targets":         len(targets),
		"target_errors":   errorCount,
		"target_warnings": warnCount,
		"file":            lastFile,
		"read_count":      totalRead,
		"active_params":   totalActive,
		"zero_count":      totalZero,
		"command":         command,
		"duration_ms":     time.Since(started).Milliseconds(),
	})
}

func (a *App) runInverterLoggingTargetOnce(settings models.Settings, command string, target activeInverterTarget, targetIndex, targetTotal int) inverterLogTargetResult {
	started := time.Now()
	result := inverterLogTargetResult{Status: "error"}
	targetText := fmt.Sprintf("%s:%d", target.IP, target.Port)
	if strings.TrimSpace(target.Name) != "" {
		targetText = fmt.Sprintf("%s (%s:%d)", target.Name, target.IP, target.Port)
	}
	a.inverterLogState.CurrentTarget = fmt.Sprintf("%d/%d · %s", targetIndex, targetTotal, targetText)
	a.appendAppLog("info", "inverter logging target selected", map[string]any{
		"component":    "inverter_logger",
		"command":      command,
		"target_index": targetIndex,
		"target_total": targetTotal,
		"inverter_id":  target.ID,
		"name":         target.Name,
		"ip":           target.IP,
		"port":         target.Port,
		"model_key":    target.ModelKey,
		"model_name":   target.ModelName,
	})

	a.setInverterLogStep(fmt.Sprintf("%d/%d · Чтение профиля регистров выбранной модели", targetIndex, targetTotal))
	model, params, err := loadDeviceParametersForModel(target.ModelKey)
	if err != nil {
		result.Message = "Ошибка чтения профиля инвертора: " + err.Error()
		result.Errors = []string{result.Message}
		result.DurationMS = time.Since(started).Milliseconds()
		a.appendAppLog("error", "inverter logging parameter profile read failed", map[string]any{"component": "inverter_logger", "error": err.Error(), "model_key": target.ModelKey, "inverter_id": target.ID})
		return result
	}
	target.ModelKey, target.ModelName, target.ParametersFile = model.Key, model.Name, model.ParametersFile
	activeParams := countActiveDeviceParameters(params)
	result.ActiveParams = activeParams
	if activeParams == 0 {
		result.Message = "В профиле модели нет активных параметров для логирования"
		result.Errors = []string{result.Message}
		result.DurationMS = time.Since(started).Milliseconds()
		a.appendAppLog("warn", "inverter logging skipped: no active params", map[string]any{"component": "inverter_logger", "params_total": len(params), "model_key": model.Key})
		return result
	}
	a.inverterLogState.ActiveParams = activeParams
	a.appendAppLog("info", "inverter logging parameters loaded", map[string]any{"component": "inverter_logger", "params_total": len(params), "active_params": activeParams, "model_key": model.Key, "parameters_file": model.ParametersFile, "inverter_id": target.ID})

	url := fmt.Sprintf("tcp://%s:%d", target.IP, target.Port)
	a.modbusMu.Lock()
	defer a.modbusMu.Unlock()
	a.setInverterLogStep(fmt.Sprintf("%d/%d · Создание Modbus-клиента %s", targetIndex, targetTotal, url))
	client, err := modbus.NewClient(&modbus.ClientConfiguration{URL: url, Timeout: modbusTimeout})
	if err != nil {
		msg := "Ошибка создания Modbus-клиента: " + err.Error()
		entry := zeroInverterLogEntry(target, params, settings, msg)
		fileName, writeErr := appendInverterJSONLine(entry, settings.InverterLogMaxSizeMB)
		if writeErr != nil {
			msg += "; ошибка записи лога: " + writeErr.Error()
		}
		result.Status = "error"
		result.Message = msg
		result.FileName = fileName
		result.ZeroCount = activeParams
		result.Errors = []string{msg}
		result.DurationMS = time.Since(started).Milliseconds()
		a.appendAppLog("error", "inverter logging modbus client creation failed", map[string]any{"component": "inverter_logger", "url": url, "error": err.Error(), "inverter_id": target.ID})
		return result
	}

	a.setInverterLogStep(fmt.Sprintf("%d/%d · Подключение к Modbus %s", targetIndex, targetTotal, url))
	connectStarted := time.Now()
	connectErr := a.openClientWithRetryForInverterLogger(client, url, maxRetries, retryDelay)
	opened := connectErr == nil
	if opened {
		defer client.Close()
		a.appendAppLog("info", "inverter logging modbus connection opened", map[string]any{"component": "inverter_logger", "url": url, "duration_ms": time.Since(connectStarted).Milliseconds(), "inverter_id": target.ID})
	} else {
		a.appendAppLog("warn", "inverter logging modbus connection failed; zero values will be written", map[string]any{"component": "inverter_logger", "url": url, "error": connectErr.Error(), "duration_ms": time.Since(connectStarted).Milliseconds(), "inverter_id": target.ID})
	}

	readErrors := make([]string, 0)
	nowForEntry := time.Now()
	entry := map[string]any{
		"timestamp":   nowForEntry.Format("02.01.06 15:04:05"),
		"inverter_id": target.ID, "inverter_name": target.Name, "inverter_ip": target.IP, "inverter_port": target.Port,
		"inverter_model_key": target.ModelKey, "inverter_model_name": target.ModelName, "parameters_file": target.ParametersFile,
	}
	for k, v := range a.currentSetpointSnapshotForInverter(target.ID, settings.Timezone, nowForEntry) {
		entry[k] = v
	}

	if opened {
		paramByCode := deviceParametersByCode(params)
		timeYearMonth, ok1 := paramByCode["inverter_time_year_month"]
		timeDayHour, ok2 := paramByCode["inverter_time_day_hour"]
		timeMinSec, ok3 := paramByCode["inverter_time_minute_second"]
		if ok1 && ok2 && ok3 {
			a.setInverterLogStep(fmt.Sprintf("%d/%d · Чтение времени инвертора: %d, %d, %d", targetIndex, targetTotal, timeYearMonth.ModbusAddress, timeDayHour.ModbusAddress, timeMinSec.ModbusAddress))
			regYearMonth, errYearMonth := client.ReadRegister(timeYearMonth.ModbusAddress, modbus.HOLDING_REGISTER)
			regDayHour, errDayHour := client.ReadRegister(timeDayHour.ModbusAddress, modbus.HOLDING_REGISTER)
			regMinSec, errMinSec := client.ReadRegister(timeMinSec.ModbusAddress, modbus.HOLDING_REGISTER)
			if errYearMonth == nil && errDayHour == nil && errMinSec == nil {
				invTime := decodeInverterDateTime(regYearMonth, regDayHour, regMinSec)
				entry["inverter_timestamp"] = invTime.Formatted
				entry["inverter_time_status"] = invTime.Status
				entry["inverter_time_register_year_month"] = regYearMonth
				entry["inverter_time_register_day_hour"] = regDayHour
				entry["inverter_time_register_minute_second"] = regMinSec
				entry["inverter_time_address_year_month"] = timeYearMonth.ModbusAddress
				entry["inverter_time_address_day_hour"] = timeDayHour.ModbusAddress
				entry["inverter_time_address_minute_second"] = timeMinSec.ModbusAddress
				// Старые ключи сохранены для совместимости экспорта и внешних интеграций.
				entry["inverter_time_register_62"] = regYearMonth
				entry["inverter_time_register_63"] = regDayHour
				entry["inverter_time_register_64"] = regMinSec
			} else {
				errText := fmt.Sprintf("ошибка чтения времени инвертора: year_month=%v day_hour=%v minute_second=%v", errYearMonth, errDayHour, errMinSec)
				entry["inverter_timestamp"] = ""
				entry["inverter_time_status"] = "error"
				readErrors = appendLimitedError(readErrors, errText)
			}
		} else {
			entry["inverter_timestamp"] = ""
			entry["inverter_time_status"] = "profile_error"
			readErrors = appendLimitedError(readErrors, "в профиле отсутствуют коды времени инвертора")
		}
	} else {
		entry["inverter_timestamp"] = ""
		entry["inverter_time_status"] = "no_connection"
	}

	readCount := 0
	zeroCount := 0
	currentIndex := 0
	for _, param := range params {
		fields := param.Fields
		if !fields.IsActive || strings.TrimSpace(fields.Code) == "" {
			continue
		}
		currentIndex++
		value := zeroValueForDeviceField(fields)
		if opened {
			a.setInverterLogStep(fmt.Sprintf("%d/%d · Параметр %d/%d: %s addresses=%v", targetIndex, targetTotal, currentIndex, activeParams, fields.Code, deviceParameterAddresses(fields)))
			registerType, ok := getRegisterType(fields.RegisterType)
			if !ok {
				readErrors = appendLimitedError(readErrors, fmt.Sprintf("%s: неподдерживаемый register_type=%s", fields.Code, fields.RegisterType))
				zeroCount++
				entry[fields.Code] = value
				continue
			}
			addresses := deviceParameterAddresses(fields)
			rawValues, readErr := a.readDeviceParameterRegistersForInverterLogger(client, fields.Code, addresses, registerType, 1, 0)
			if readErr == nil {
				resultValue, decodeErr := decodeDeviceParameterRegisters(rawValues, fields)
				if decodeErr == nil {
					value = resultValue
					readCount++
				} else {
					readErr = decodeErr
				}
			}
			if readErr != nil {
				readErrors = appendLimitedError(readErrors, fmt.Sprintf("%s addresses=%v: %v", fields.Code, addresses, readErr))
				zeroCount++
			}
		} else {
			zeroCount++
		}
		entry[fields.Code] = value
	}

	a.setInverterLogStep(fmt.Sprintf("%d/%d · Запись JSON-строки в log файл", targetIndex, targetTotal))
	fileName, err := appendInverterJSONLine(entry, settings.InverterLogMaxSizeMB)
	if err != nil {
		msg := "Ошибка записи inverter log: " + err.Error()
		result.Status = "error"
		result.Message = msg
		result.ReadCount = readCount
		result.ZeroCount = zeroCount
		result.Errors = appendLimitedError(readErrors, msg)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}

	status := "ok"
	message := fmt.Sprintf("%s:%d: прочитано %d/%d, нулей %d", target.IP, target.Port, readCount, activeParams, zeroCount)
	if !opened {
		status = "warn"
		message = fmt.Sprintf("Не удалось подключиться к %s:%d, записаны нули по %d параметрам", target.IP, target.Port, zeroCount)
		readErrors = appendLimitedError(readErrors, "Ошибка подключения к Modbus: "+connectErr.Error())
	} else if len(readErrors) > 0 {
		status = "warn"
		message = fmt.Sprintf("%s:%d прочитан частично: %d/%d, ошибок/нулей %d", target.IP, target.Port, readCount, activeParams, zeroCount)
	}

	result.Status = status
	result.Message = message
	result.FileName = fileName
	result.ReadCount = readCount
	result.ZeroCount = zeroCount
	result.Errors = readErrors
	result.DurationMS = time.Since(started).Milliseconds()
	a.appendAppLog("info", "inverter logging target completed", map[string]any{
		"component":     "inverter_logger",
		"status":        status,
		"file":          fileName,
		"read_count":    readCount,
		"active_params": activeParams,
		"zero_count":    zeroCount,
		"error_count":   len(readErrors),
		"sample_errors": readErrors,
		"inverter_id":   target.ID,
		"model_key":     target.ModelKey,
		"command":       command,
		"duration_ms":   result.DurationMS,
	})
	return result
}

func zeroInverterLogEntry(target activeInverterTarget, params []DeviceParameterFixture, settings models.Settings, message string) map[string]any {
	now := time.Now()
	entry := map[string]any{
		"timestamp":   now.Format("02.01.06 15:04:05"),
		"inverter_id": target.ID, "inverter_name": target.Name, "inverter_ip": target.IP, "inverter_port": target.Port,
		"inverter_model_key": target.ModelKey, "inverter_model_name": target.ModelName, "parameters_file": target.ParametersFile,
		"inverter_time_status": "error", "logger_error": message,
	}
	for _, param := range params {
		if param.Fields.IsActive && strings.TrimSpace(param.Fields.Code) != "" {
			entry[param.Fields.Code] = zeroValueForDeviceField(param.Fields)
		}
	}
	return entry
}

func appendLimitedError(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	if len(items) >= 20 {
		if len(items) == 20 {
			items = append(items, "...остальные ошибки скрыты, смотри app log")
		}
		return items
	}
	return append(items, value)
}

func countActiveDeviceParameters(params []DeviceParameterFixture) int {
	count := 0
	for _, param := range params {
		if param.Fields.IsActive && strings.TrimSpace(param.Fields.Code) != "" {
			count++
		}
	}
	return count
}

func zeroValueForDeviceField(fields DeviceParameterFields) any {
	switch strings.ToLower(strings.TrimSpace(fields.Type)) {
	case "string", "str", "text":
		return "0"
	default:
		return 0.0
	}
}

func (a *App) openClientWithRetryForInverterLogger(client *modbus.ModbusClient, url string, retries int, delay time.Duration) error {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		a.setInverterLogStep(fmt.Sprintf("Подключение к Modbus %s, попытка %d/%d", url, attempt, retries))
		attemptStarted := time.Now()
		err := client.Open()
		if err == nil {
			a.appendAppLog("info", "inverter logger modbus open attempt ok", map[string]any{"component": "inverter_logger", "url": url, "attempt": attempt, "duration_ms": time.Since(attemptStarted).Milliseconds()})
			return nil
		}
		lastErr = err
		a.appendAppLog("warn", "inverter logger modbus open attempt failed", map[string]any{"component": "inverter_logger", "url": url, "attempt": attempt, "max_attempts": retries, "error": err.Error(), "duration_ms": time.Since(attemptStarted).Milliseconds()})
		if attempt < retries {
			time.Sleep(delay)
		}
	}
	return lastErr
}

func (a *App) readRegisterWithRetryForInverterLogger(client *modbus.ModbusClient, _ string, address uint16, registerType modbus.RegType, retries int, delay time.Duration) (uint16, error) {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		value, err := client.ReadRegister(address, registerType)
		if err == nil {
			return value, nil
		}
		lastErr = err
		if attempt < retries {
			time.Sleep(delay)
		}
	}
	return 0, lastErr
}

func (a *App) readDeviceParameterRegistersForInverterLogger(client *modbus.ModbusClient, code string, addresses []uint16, registerType modbus.RegType, retries int, delay time.Duration) ([]uint16, error) {
	values := make([]uint16, 0, len(addresses))
	for _, address := range addresses {
		value, err := a.readRegisterWithRetryForInverterLogger(client, code, address, registerType, retries, delay)
		if err != nil {
			return nil, fmt.Errorf("address=%d: %w", address, err)
		}
		values = append(values, value)
	}
	return values, nil
}

func (a *App) writeZeroInverterLogLine(target activeInverterTarget, params []DeviceParameterFixture, maxMB int, message string) {
	entry := map[string]any{"timestamp": time.Now().Format("02.01.06 15:04:05"),
		"inverter_id": target.ID, "inverter_name": target.Name, "inverter_ip": target.IP, "inverter_port": target.Port,
		"inverter_model_key": target.ModelKey, "inverter_model_name": target.ModelName, "parameters_file": target.ParametersFile}
	activeParams := 0
	for _, param := range params {
		if param.Fields.IsActive && strings.TrimSpace(param.Fields.Code) != "" {
			entry[param.Fields.Code] = zeroValueForDeviceField(param.Fields)
			activeParams++
		}
	}
	fileName, err := appendInverterJSONLine(entry, maxMB)
	if err != nil {
		a.setInverterLogState("error", message+"; ошибка записи логов: "+err.Error(), "", 0)
		return
	}
	a.setInverterLogState("warn", message+"; записаны нули", fileName, 0)
	a.setInverterLogErrors([]string{message}, activeParams, activeParams)
}

func (a *App) markInverterLogRunning(step, command string) {
	now := time.Now().UTC()
	a.inverterLogState.Running = true
	a.inverterLogState.LastStartedUTC = now
	a.inverterLogState.LastStatus = "running"
	a.inverterLogState.LastMessage = "Логирование запущено: " + command
	a.inverterLogState.CurrentStep = step
	a.inverterLogState.CurrentTarget = ""
	a.inverterLogState.LastErrors = nil
	a.inverterLogState.LastDurationMS = 0
	a.inverterLogState.LastCount = 0
	a.inverterLogState.ActiveParams = 0
	a.inverterLogState.ZeroParams = 0
}

func (a *App) setInverterLogStep(step string) {
	a.inverterLogState.CurrentStep = step
}

func (a *App) setInverterLogIdleState(status, message string) {
	a.inverterLogState.Running = false
	a.inverterLogState.LastStatus = status
	a.inverterLogState.LastMessage = message
	a.inverterLogState.CurrentStep = ""
}

func (a *App) setInverterLogState(status, message, file string, count int) {
	now := time.Now().UTC()
	a.inverterLogState.Running = false
	a.inverterLogState.LastRunUTC = now
	a.inverterLogState.LastStatus = status
	a.inverterLogState.LastMessage = message
	a.inverterLogState.LastFile = file
	a.inverterLogState.LastCount = count
	a.inverterLogState.CurrentStep = ""
	if !a.inverterLogState.LastStartedUTC.IsZero() {
		a.inverterLogState.LastDurationMS = now.Sub(a.inverterLogState.LastStartedUTC).Milliseconds()
	}
}

func (a *App) setInverterLogErrors(errors []string, activeParams int, zeroParams int) {
	a.inverterLogState.LastErrors = errors
	a.inverterLogState.ActiveParams = activeParams
	a.inverterLogState.ZeroParams = zeroParams
}

func (a *App) getActiveInverterTargets() ([]activeInverterTarget, error) {
	rows, err := a.db.Query(`
		SELECT i.id, i.name, i.ip, i.port, i.model_key
		FROM inverters i
		LEFT JOIN schedules s ON s.inverter_id = i.id
		ORDER BY CASE WHEN COALESCE(s.is_enabled, 0) = 1 THEN 0 ELSE 1 END, i.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	targets := make([]activeInverterTarget, 0)
	for rows.Next() {
		var target activeInverterTarget
		if err := rows.Scan(&target.ID, &target.Name, &target.IP, &target.Port, &target.ModelKey); err != nil {
			return nil, err
		}
		model, err := findInverterModel(target.ModelKey)
		if err != nil {
			return nil, fmt.Errorf("инвертор id=%d: %w", target.ID, err)
		}
		target.ModelKey = model.Key
		target.ModelName = model.Name
		target.ParametersFile = model.ParametersFile
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("в базе нет инверторов")
	}
	return targets, nil
}

func (a *App) getFirstActiveInverterTarget() (activeInverterTarget, error) {
	targets, err := a.getActiveInverterTargets()
	if err != nil {
		return activeInverterTarget{}, err
	}
	return targets[0], nil
}

func appendInverterJSONLine(entry map[string]any, maxMB int) (string, error) {
	if maxMB <= 0 {
		maxMB = 20
	}
	if err := os.MkdirAll(inverterLogDir, 0755); err != nil {
		return "", err
	}
	inverterLogFileMu.Lock()
	defer inverterLogFileMu.Unlock()

	fileName, err := currentInverterLogFileName(maxMB)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	path := filepath.Join(inverterLogDir, fileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, string(payload))
	return fileName, err
}

func currentInverterLogFileName(maxMB int) (string, error) {
	files, err := listInverterLogFiles()
	if err != nil {
		return "", err
	}
	limit := int64(maxMB) * 1024 * 1024
	if len(files) > 0 {
		latest := files[0]
		if latest.SizeBytes < limit {
			return latest.Name, nil
		}
	}
	return "inverter-" + time.Now().Format("20060102-150405") + ".log", nil
}

func listInverterLogFiles() ([]InverterLogFileInfo, error) {
	if err := os.MkdirAll(inverterLogDir, 0755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(inverterLogDir)
	if err != nil {
		return nil, err
	}
	files := make([]InverterLogFileInfo, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		files = append(files, InverterLogFileInfo{
			Name:        name,
			SizeBytes:   info.Size(),
			SizeHuman:   formatBytes(info.Size()),
			ModifiedUTC: info.ModTime().UTC().Format(time.RFC3339),
			DownloadURL: "/api/inverter-logs/file?name=" + name,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModifiedUTC > files[j].ModifiedUTC })
	return files, nil
}

func dirSize(path string) (int64, error) {
	var size int64
	if err := os.MkdirAll(path, 0755); err != nil {
		return 0, err
	}
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size, err
}

func safeInverterLogName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") || !strings.HasSuffix(strings.ToLower(name), ".log") {
		return "", false
	}
	return name, true
}

func readInverterLogTail(name string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	path := filepath.Join(inverterLogDir, name)
	return tailFileLines(path, limit)
}

func (a *App) inverterLoggingPageHandler(w http.ResponseWriter, r *http.Request) {
	settings, err := a.getSettings()
	if err != nil {
		http.Error(w, "Ошибка получения настроек", http.StatusInternalServerError)
		return
	}
	stats, err := a.getSettingsStats(settings)
	if err != nil {
		http.Error(w, "Ошибка получения статистики", http.StatusInternalServerError)
		return
	}
	files, _ := listInverterLogFiles()
	columns := buildInverterLogExportColumns()
	data := InverterLoggingPageData{Title: "Логирование инвертора", Settings: settings, SettingsStats: stats, Files: files, ExportColumns: columns}
	if err := a.tmplInverterLogs.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы логирования", http.StatusInternalServerError)
	}
}

func (a *App) updateInverterLoggingSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения настроек"})
		return
	}
	settings.InverterLoggingEnabled = strings.TrimSpace(r.FormValue("inverter_logging_enabled")) == "1"
	if v := parsePositiveInt(r.FormValue("inverter_logging_interval_seconds"), settings.InverterLoggingIntervalSeconds); v > 0 {
		settings.InverterLoggingIntervalSeconds = v
	}
	if v := parsePositiveInt(r.FormValue("inverter_log_max_size_mb"), settings.InverterLogMaxSizeMB); v > 0 {
		settings.InverterLogMaxSizeMB = v
	}
	if settings.InverterLoggingIntervalSeconds < 1 {
		settings.InverterLoggingIntervalSeconds = 1
	}
	if settings.InverterLogMaxSizeMB < 1 {
		settings.InverterLogMaxSizeMB = 1
	}
	if err := a.updateRuntimeSettings(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения настроек логирования"})
		return
	}
	a.restartInverterLogger("inverter logging settings updated")
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Настройки логирования инвертора сохранены"})
}

func (a *App) apiInverterLogFilesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	files, err := listInverterLogFiles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения списка файлов"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "items": files})
}

func (a *App) apiInverterLoggerStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения настроек"})
		return
	}
	files, _ := listInverterLogFiles()
	targets, targetErr := a.getActiveInverterTargets()
	var targetValue any
	var targetsValue any
	var targetError string
	if targetErr == nil {
		targetsValue = targets
		if len(targets) > 0 {
			targetValue = targets[0]
		}
	} else {
		targetError = targetErr.Error()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":                         true,
		"enabled":                    settings.InverterLoggingEnabled,
		"interval_seconds":           settings.InverterLoggingIntervalSeconds,
		"max_file_mb":                settings.InverterLogMaxSizeMB,
		"running":                    a.inverterLogState.Running,
		"current_step":               a.inverterLogState.CurrentStep,
		"current_target":             a.inverterLogState.CurrentTarget,
		"last_started_utc":           formatTimeRFC3339(a.inverterLogState.LastStartedUTC),
		"last_run_utc":               formatTimeRFC3339(a.inverterLogState.LastRunUTC),
		"next_run_utc":               formatTimeRFC3339(a.inverterLogState.NextRunUTC),
		"last_file":                  a.inverterLogState.LastFile,
		"last_status":                a.inverterLogState.LastStatus,
		"last_message":               a.inverterLogState.LastMessage,
		"last_count":                 a.inverterLogState.LastCount,
		"last_duration_ms":           a.inverterLogState.LastDurationMS,
		"active_params":              a.inverterLogState.ActiveParams,
		"zero_params":                a.inverterLogState.ZeroParams,
		"last_errors":                a.inverterLogState.LastErrors,
		"target":                     targetValue,
		"targets":                    targetsValue,
		"target_count":               len(targets),
		"target_error":               targetError,
		"files_count":                len(files),
		"latest_file":                latestInverterLogFileName(files),
		"inverter_logs_endpoint":     "/api/inverter-logs",
		"inverter_log_file_endpoint": "/api/inverter-logs/file?code=latest",
	})
}

func formatTimeRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func latestInverterLogFileName(files []InverterLogFileInfo) string {
	if len(files) == 0 {
		return ""
	}
	return files[0].Name
}

func (a *App) runInverterLoggerNowHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения настроек"})
		return
	}
	a.runInverterLoggingOnce(settings, "manual run-now")
	statusCode := http.StatusOK
	if a.inverterLogState.LastStatus == "error" {
		statusCode = http.StatusInternalServerError
	}
	writeJSON(w, statusCode, map[string]any{
		"ok":               statusCode == http.StatusOK,
		"message":          a.inverterLogState.LastMessage,
		"status":           a.inverterLogState.LastStatus,
		"file":             a.inverterLogState.LastFile,
		"read_count":       a.inverterLogState.LastCount,
		"active_params":    a.inverterLogState.ActiveParams,
		"zero_params":      a.inverterLogState.ZeroParams,
		"last_errors":      a.inverterLogState.LastErrors,
		"last_duration_ms": a.inverterLogState.LastDurationMS,
	})
}

func (a *App) apiInverterLogFileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	files, err := listInverterLogFiles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения логов"})
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if name == "" && (code == "latest" || code == "last") && len(files) > 0 {
		name = files[0].Name
	}
	if name == "" {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Лог файл не найден"})
		return
	}
	safeName, ok := safeInverterLogName(name)
	if !ok {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректное имя файла"})
		return
	}
	path := filepath.Join(inverterLogDir, safeName)
	if _, err := os.Stat(path); err != nil {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Файл не найден"})
		return
	}
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	if format == "json" {
		limit := parsePositiveInt(r.URL.Query().Get("limit"), 200)
		lines, err := readInverterLogTail(safeName, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения файла"})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "name": safeName, "lines": lines})
		return
	}
	if format == "xlsx" {
		lines, err := readInverterLogTail(safeName, 1000000)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения файла"})
			return
		}
		items := make([]inverterLogLine, 0, len(lines))
		union := map[string]bool{}
		for _, raw := range lines {
			var rec map[string]any
			if json.Unmarshal([]byte(raw), &rec) == nil {
				for k := range rec {
					union[k] = true
				}
				items = append(items, inverterLogLine{Record: rec, Raw: raw, Time: parseLogRecordTime(rec)})
			}
		}
		headers := defaultLogExportHeaders(union)
		xlsxHeaders, rows := buildLogExportXLSXRows(items, headers)
		data, err := buildGenericXLSX("InverterLog", xlsxHeaders, rows)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка генерации XLSX"})
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.TrimSuffix(safeName, ".log")+".xlsx"))
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeName))
	http.ServeFile(w, r, path)
}
