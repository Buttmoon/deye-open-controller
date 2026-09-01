package main

import (
	"os"
	"time"

	"inverter-schedule/internal/models"
)

func (a *App) getSettings() (models.Settings, error) {
	a.settingsMu.RLock()
	if a.settingsCacheLoaded {
		s := a.settingsCache
		a.settingsMu.RUnlock()
		return s, nil
	}
	a.settingsMu.RUnlock()

	s, err := a.loadSettingsFromDB()
	if err != nil {
		return s, err
	}
	a.settingsMu.Lock()
	a.settingsCache = s
	a.settingsCacheLoaded = true
	a.settingsMu.Unlock()
	return s, nil
}

func (a *App) invalidateSettingsCache() {
	a.settingsMu.Lock()
	a.settingsCacheLoaded = false
	a.settingsMu.Unlock()
}

func (a *App) loadSettingsFromDB() (models.Settings, error) {
	var s models.Settings
	var schedulerEnabled int
	var fileLoggingEnabled int
	var inverterLoggingEnabled int
	var schedulerFillAllPointsCurrent int
	var integrationAuthEnabled int
	err := a.db.QueryRow(`
		SELECT id, application_name, timezone, scheduler_enabled, file_logging_enabled,
		       scheduler_interval_minutes, scheduler_fill_all_points_current, log_level, app_log_max_size_mb, task_log_max_rows,
		       inverter_logging_enabled, inverter_logging_interval_seconds, inverter_log_max_size_mb,
		       integration_url, integration_auth_enabled, integration_auth_scheme,
		       integration_auth_token, integration_auth_username, integration_auth_password,
		       integration_push_interval_seconds, integration_server_name, integration_server_ip
		FROM settings ORDER BY id LIMIT 1`).
		Scan(&s.ID, &s.ApplicationName, &s.Timezone, &schedulerEnabled, &fileLoggingEnabled, &s.SchedulerIntervalMinutes, &schedulerFillAllPointsCurrent, &s.LogLevel, &s.AppLogMaxSizeMB, &s.TaskLogMaxRows, &inverterLoggingEnabled, &s.InverterLoggingIntervalSeconds, &s.InverterLogMaxSizeMB, &s.IntegrationURL, &integrationAuthEnabled, &s.IntegrationAuthScheme, &s.IntegrationAuthToken, &s.IntegrationAuthUsername, &s.IntegrationAuthPassword, &s.IntegrationPushIntervalSeconds, &s.IntegrationServerName, &s.IntegrationServerIP)
	s.SchedulerEnabled = schedulerEnabled == 1
	s.FileLoggingEnabled = fileLoggingEnabled == 1
	s.SchedulerFillAllPointsCurrent = schedulerFillAllPointsCurrent == 1
	s.InverterLoggingEnabled = inverterLoggingEnabled == 1
	s.IntegrationAuthEnabled = integrationAuthEnabled == 1
	if s.SchedulerIntervalMinutes <= 0 {
		s.SchedulerIntervalMinutes = 60
	}
	if s.LogLevel == "" {
		s.LogLevel = "INFO"
	}
	if s.AppLogMaxSizeMB <= 0 {
		s.AppLogMaxSizeMB = 20
	}
	if s.TaskLogMaxRows <= 0 {
		s.TaskLogMaxRows = 1000
	}
	if s.InverterLoggingIntervalSeconds <= 0 {
		s.InverterLoggingIntervalSeconds = 60
	}
	if s.InverterLogMaxSizeMB <= 0 {
		s.InverterLogMaxSizeMB = 20
	}
	if s.IntegrationPushIntervalSeconds <= 0 {
		s.IntegrationPushIntervalSeconds = 300
	}
	return s, err
}

func (a *App) updateSettingsTimezone(timezone string) error {
	_, err := a.db.Exec(`UPDATE settings SET timezone = ? WHERE id = (SELECT id FROM settings ORDER BY id LIMIT 1)`, timezone)
	if err == nil {
		a.invalidateSettingsCache()
	}
	return err
}

func (a *App) updateSchedulerEnabled(enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := a.db.Exec(`UPDATE settings SET scheduler_enabled = ? WHERE id = (SELECT id FROM settings ORDER BY id LIMIT 1)`, value)
	if err == nil {
		a.invalidateSettingsCache()
	}
	return err
}

func (a *App) updateFileLoggingEnabled(enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := a.db.Exec(`UPDATE settings SET file_logging_enabled = ? WHERE id = (SELECT id FROM settings ORDER BY id LIMIT 1)`, value)
	if err == nil {
		a.invalidateSettingsCache()
	}
	return err
}

func (a *App) updateRuntimeSettings(s models.Settings) error {
	_, err := a.db.Exec(`UPDATE settings SET application_name=?, scheduler_enabled=?, file_logging_enabled=?, scheduler_interval_minutes=?, scheduler_fill_all_points_current=?, log_level=?, app_log_max_size_mb=?, task_log_max_rows=?, inverter_logging_enabled=?, inverter_logging_interval_seconds=?, inverter_log_max_size_mb=? WHERE id=(SELECT id FROM settings ORDER BY id LIMIT 1)`, s.ApplicationName, boolToInt(s.SchedulerEnabled), boolToInt(s.FileLoggingEnabled), s.SchedulerIntervalMinutes, boolToInt(s.SchedulerFillAllPointsCurrent), s.LogLevel, s.AppLogMaxSizeMB, s.TaskLogMaxRows, boolToInt(s.InverterLoggingEnabled), s.InverterLoggingIntervalSeconds, s.InverterLogMaxSizeMB)
	if err == nil {
		a.invalidateSettingsCache()
	}
	return err
}

func (a *App) clearSchedulesData() (int64, error) {
	res, err := a.db.Exec(`DELETE FROM schedules`)
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	_, _ = a.db.Exec(`VACUUM`)
	return rows, nil
}

func (a *App) updateIntegrationSettings(s models.Settings) error {
	_, err := a.db.Exec(`UPDATE settings SET integration_url=?, integration_auth_enabled=?, integration_auth_scheme=?, integration_auth_token=?, integration_auth_username=?, integration_auth_password=?, integration_push_interval_seconds=?, integration_server_name=?, integration_server_ip=? WHERE id=(SELECT id FROM settings ORDER BY id LIMIT 1)`,
		s.IntegrationURL, boolToInt(s.IntegrationAuthEnabled), s.IntegrationAuthScheme, s.IntegrationAuthToken, s.IntegrationAuthUsername, s.IntegrationAuthPassword, s.IntegrationPushIntervalSeconds, s.IntegrationServerName, s.IntegrationServerIP)
	if err == nil {
		a.invalidateSettingsCache()
	}
	return err
}

func (a *App) clearDatabaseData() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`DELETE FROM task_runs`,
		`DELETE FROM schedules`,
		`DELETE FROM schedule_templates`,
		`DELETE FROM inverters`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, _ = a.db.Exec(`VACUUM`)
	return nil
}
func (a *App) getSettingsStats(settings models.Settings) (SettingsStats, error) {
	stats := SettingsStats{
		DBPath:                         "data/app.db",
		AppLogPath:                     a.appLogPath,
		InverterLogDir:                 inverterLogDir,
		SchedulerEnabled:               settings.SchedulerEnabled,
		FileLoggingEnabled:             settings.FileLoggingEnabled,
		SchedulerIntervalMinutes:       settings.SchedulerIntervalMinutes,
		InverterLoggingEnabled:         settings.InverterLoggingEnabled,
		InverterLoggingIntervalSeconds: settings.InverterLoggingIntervalSeconds,
		InverterLogMaxSizeMB:           settings.InverterLogMaxSizeMB,
		SchedulerTestCommand:           "PowerShell: Invoke-WebRequest -UseBasicParsing -Uri http://localhost:8080/api/tasks/run-now -Method POST",
		InverterLoggingTestCommand:     "PowerShell: Invoke-WebRequest -UseBasicParsing -Uri http://localhost:8080/api/inverter-logs/run-now -Method POST",
		LogsAPIEndpoint:                "/api/logs?kind=app&limit=200",
		RawLogsEndpoint:                "/api/logs/raw",
		TaskLogsEndpoint:               "/api/logs?kind=task&limit=200",
		InverterLogsEndpoint:           "/api/inverter-logs",
		InverterLogFileEndpoint:        "/api/inverter-logs/file?code=latest",
		TemplateExampleURL:             "/templates/import/example.xlsx",
	}

	if info, err := os.Stat(stats.DBPath); err == nil {
		stats.DBSizeBytes = info.Size()
		stats.DBSizeHuman = formatBytes(info.Size())
	}
	if info, err := os.Stat(stats.AppLogPath); err == nil {
		stats.AppLogSizeBytes = info.Size()
		stats.AppLogSizeHuman = formatBytes(info.Size())
	}
	if files, listErr := listInverterLogFiles(); listErr == nil {
		var totalSize int64
		for _, f := range files {
			totalSize += f.SizeBytes
		}
		stats.InverterLogSizeBytes = totalSize
		stats.InverterLogSizeHuman = formatBytes(totalSize)
		stats.InverterLogFiles = len(files)
		if len(files) > 0 {
			stats.LastInverterLogFile = files[0].Name
		}
	}

	_ = a.db.QueryRow(`SELECT COUNT(*) FROM inverters`).Scan(&stats.TotalInverters)
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM schedule_templates`).Scan(&stats.TotalTemplates)
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM task_runs`).Scan(&stats.TaskLogRows)

	_ = a.db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE is_enabled = 1`).Scan(&stats.EnabledInverters)
	if stats.TotalInverters >= stats.EnabledInverters {
		stats.DisabledInverters = stats.TotalInverters - stats.EnabledInverters
	}
	_ = a.db.QueryRow(`SELECT COUNT(DISTINCT applied_template_id) FROM schedules WHERE is_enabled = 1 AND applied_template_id IS NOT NULL`).Scan(&stats.EnabledTemplates)
	stats.OnlineInverters = 0
	stats.OfflineInverters = stats.TotalInverters

	if !a.schedulerState.LastRunUTC.IsZero() {
		stats.LastSchedulerRunUTC = a.schedulerState.LastRunUTC.UTC().Format(time.RFC3339)
		if loc, err := time.LoadLocation(settings.Timezone); err == nil {
			stats.LastSchedulerRunLocal = a.schedulerState.LastRunUTC.In(loc).Format("2006-01-02 15:04:05 MST")
		}
		stats.LastSchedulerProcessed = a.schedulerState.Processed
		stats.LastSchedulerTriggered = a.schedulerState.Triggered
		stats.LastSchedulerCommand = a.schedulerState.LastCommand
	}
	if !a.schedulerState.NextRunUTC.IsZero() {
		stats.NextSchedulerRunUTC = a.schedulerState.NextRunUTC.UTC().Format(time.RFC3339)
		if loc, err := time.LoadLocation(settings.Timezone); err == nil {
			stats.NextSchedulerRunLocal = a.schedulerState.NextRunUTC.In(loc).Format("2006-01-02 15:04:05 MST")
		}
	}
	stats.LastInverterLoggerStatus = a.inverterLogState.LastStatus
	stats.LastInverterLoggerMessage = a.inverterLogState.LastMessage
	stats.LastInverterLoggerCount = a.inverterLogState.LastCount
	stats.InverterLoggerRunning = a.inverterLogState.Running
	stats.InverterLoggerCurrentTarget = a.inverterLogState.CurrentTarget
	stats.InverterLoggerCurrentStep = a.inverterLogState.CurrentStep
	stats.InverterLoggerLastErrors = a.inverterLogState.LastErrors
	stats.InverterLoggerDurationMS = a.inverterLogState.LastDurationMS
	stats.InverterLoggerActiveParams = a.inverterLogState.ActiveParams
	stats.InverterLoggerZeroParams = a.inverterLogState.ZeroParams
	if !a.inverterLogState.LastRunUTC.IsZero() {
		stats.LastInverterLoggerRunUTC = a.inverterLogState.LastRunUTC.UTC().Format(time.RFC3339)
		if loc, err := time.LoadLocation(settings.Timezone); err == nil {
			stats.LastInverterLoggerRunLocal = a.inverterLogState.LastRunUTC.In(loc).Format("2006-01-02 15:04:05 MST")
		}
		stats.LastInverterLoggerStatus = a.inverterLogState.LastStatus
		stats.LastInverterLoggerMessage = a.inverterLogState.LastMessage
		stats.LastInverterLoggerCount = a.inverterLogState.LastCount
		stats.InverterLoggerRunning = a.inverterLogState.Running
		stats.InverterLoggerCurrentTarget = a.inverterLogState.CurrentTarget
		stats.InverterLoggerCurrentStep = a.inverterLogState.CurrentStep
		stats.InverterLoggerLastErrors = a.inverterLogState.LastErrors
		stats.InverterLoggerDurationMS = a.inverterLogState.LastDurationMS
		stats.InverterLoggerActiveParams = a.inverterLogState.ActiveParams
		stats.InverterLoggerZeroParams = a.inverterLogState.ZeroParams
		if a.inverterLogState.LastFile != "" {
			stats.LastInverterLogFile = a.inverterLogState.LastFile
		}
	}
	if !a.inverterLogState.NextRunUTC.IsZero() {
		stats.NextInverterLoggerRunUTC = a.inverterLogState.NextRunUTC.UTC().Format(time.RFC3339)
		if loc, err := time.LoadLocation(settings.Timezone); err == nil {
			stats.NextInverterLoggerRunLocal = a.inverterLogState.NextRunUTC.In(loc).Format("2006-01-02 15:04:05 MST")
		}
	}

	return stats, nil
}
