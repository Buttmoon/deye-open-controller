package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func (a *App) apiSchedulerStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения настроек scheduler"})
		return
	}
	items, itemsErr := a.getScheduledInvertersForExecution()
	loc := time.UTC
	if settings.Timezone != "" {
		if loaded, err := time.LoadLocation(settings.Timezone); err == nil {
			loc = loaded
		}
	}
	nowUTC := time.Now().UTC()
	nowLocal := nowUTC.In(loc)
	payload := map[string]any{
		"ok":                                true,
		"scheduler_enabled":                 settings.SchedulerEnabled,
		"scheduler_interval_minutes":        settings.SchedulerIntervalMinutes,
		"scheduler_fill_all_points_current": settings.SchedulerFillAllPointsCurrent,
		"timezone":                          settings.Timezone,
		"now_utc":                           nowUTC.Format(time.RFC3339),
		"now_local":                         nowLocal.Format(time.RFC3339),
		"selected_day":                      nowLocal.Day(),
		"selected_hour":                     nowLocal.Hour(),
		"selected_minute":                   nowLocal.Minute() / 5 * 5,
		"last_run_utc":                      formatTimeRFC3339(a.schedulerState.LastRunUTC),
		"next_run_utc":                      formatTimeRFC3339(a.schedulerState.NextRunUTC),
		"last_processed":                    a.schedulerState.Processed,
		"last_triggered":                    a.schedulerState.Triggered,
		"last_command":                      a.schedulerState.LastCommand,
		"task_logs_endpoint":                "/tasks/logs",
		"app_logs_endpoint":                 "/logs",
	}
	if itemsErr != nil {
		payload["active_schedules_error"] = itemsErr.Error()
	} else {
		payload["active_schedules_count"] = len(items)
		shortItems := make([]map[string]any, 0, len(items))
		for _, item := range items {
			shortItems = append(shortItems, map[string]any{
				"inverter_id":   item.InverterID,
				"inverter_name": item.InverterName,
				"ip":            item.IP,
				"port":          item.Port,
				"template_id":   item.TemplateID,
				"template_name": item.TemplateName,
				"schedule_name": item.ScheduleName,
			})
		}
		payload["active_schedules"] = shortItems
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}
