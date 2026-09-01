package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

var schedulerMu sync.Mutex

type runtimeScheduledItem struct {
	InverterID            int64
	InverterName          string
	IP                    string
	Port                  int
	ModelKey              string
	ModelName             string
	ParametersFile        string
	ProfileWriteConfirmed bool
	TemplateID            int64
	TemplateName          string
	ScheduleName          string
	ScheduleJSON          string
}

func (a *App) startScheduler() {
	a.schedulerCtlMu.Lock()
	defer a.schedulerCtlMu.Unlock()
	if a.schedulerStopCh != nil {
		a.appendAppLog("warn", "scheduler start skipped: already running", map[string]any{"component": "scheduler"})
		return
	}
	stopCh := make(chan struct{})
	a.schedulerStopCh = stopCh
	a.appendAppLog("info", "scheduler start requested", map[string]any{"component": "scheduler"})
	go a.schedulerLoop(stopCh)
}

func (a *App) stopScheduler() {
	a.schedulerCtlMu.Lock()
	defer a.schedulerCtlMu.Unlock()
	if a.schedulerStopCh != nil {
		close(a.schedulerStopCh)
		a.schedulerStopCh = nil
		a.appendAppLog("info", "scheduler stop requested", map[string]any{"component": "scheduler"})
	}
}

func (a *App) restartScheduler(reason string) {
	a.appendAppLog("info", "scheduler restart requested", map[string]any{"reason": reason, "component": "scheduler"})
	a.stopScheduler()
	a.startScheduler()
}

func (a *App) recreateActiveSchedulerTask(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "schedule changed"
	}
	go func() {
		// Любое изменение активного расписания должно пересоздавать периодическую задачу
		// и сразу отправлять текущие 6 точек по новой логике.
		a.restartScheduler(reason)
		a.runActiveSchedulesOnceIfAny(reason)
	}()
}

func (a *App) restoreActiveSchedulesOnStartup() {
	go func() {
		// Даем приложению полностью подняться, затем пересоздаём/восстанавливаем активную
		// периодическую задачу в памяти и сразу отправляем текущий набор из 6 точек.
		time.Sleep(2 * time.Second)
		a.runActiveSchedulesOnceIfAny("startup restore active schedules")
	}()
}

func (a *App) runActiveSchedulesOnceIfAny(command string) {
	settings, err := a.getSettings()
	if err != nil {
		a.appendAppLog("error", "scheduler active schedules restore settings read failed", map[string]any{"component": "scheduler", "command": command, "error": err.Error()})
		_ = a.insertSystemTaskRun("error", "Scheduler restore: ошибка чтения настроек: "+err.Error(), schedulerPayloadJSON(map[string]any{"error": err.Error()}), command)
		return
	}
	if !settings.SchedulerEnabled {
		a.appendAppLog("info", "scheduler active schedules restore skipped: scheduler disabled", map[string]any{"component": "scheduler", "command": command})
		return
	}

	items, err := a.getScheduledInvertersForExecution()
	if err != nil {
		a.appendAppLog("error", "scheduler active schedules restore read failed", map[string]any{"component": "scheduler", "command": command, "error": err.Error()})
		_ = a.insertSystemTaskRun("error", "Scheduler restore: ошибка выборки активных расписаний: "+err.Error(), schedulerPayloadJSON(map[string]any{"error": err.Error()}), command)
		return
	}
	if len(items) == 0 {
		a.appendAppLog("info", "scheduler active schedules restore skipped: no active schedules", map[string]any{"component": "scheduler", "command": command})
		return
	}

	a.appendAppLog("info", "scheduler active schedules immediate run queued", map[string]any{"component": "scheduler", "command": command, "active_schedules": len(items)})
	a.runSchedulerOnceWithCommand(command)
}

func (a *App) schedulerLoop(stopCh <-chan struct{}) {
	a.appendAppLog("info", "scheduler goroutine started", map[string]any{"component": "scheduler", "mode": "every_5_minutes"})
	for {
		settings, err := a.getSettings()
		loc := time.UTC
		timezone := "UTC"
		if err != nil {
			a.appendAppLog("error", "scheduler loop settings read failed", map[string]any{"component": "scheduler", "error": err.Error()})
		} else {
			timezone = settings.Timezone
			if loaded, loadErr := time.LoadLocation(settings.Timezone); loadErr == nil {
				loc = loaded
			}
		}
		nowLocal := time.Now().In(loc)
		currentMinute := nowLocal.Minute()
		minutesToNextSlot := 5 - (currentMinute % 5)
		if minutesToNextSlot == 0 {
			minutesToNextSlot = 5
		}
		nextLocal := nowLocal.Add(time.Duration(minutesToNextSlot) * time.Minute).Truncate(5 * time.Minute)
		wait := time.Until(nextLocal)
		if wait < time.Second {
			wait = time.Second
		}
		a.schedulerState.NextRunUTC = nextLocal.UTC()
		a.appendAppLog("info", "scheduler next 5-minute run planned", map[string]any{"component": "scheduler", "mode": "every_5_minutes", "timezone": timezone, "next_run_local": nextLocal.Format(time.RFC3339), "next_run_utc": nextLocal.UTC().Format(time.RFC3339), "wait_seconds": int(wait.Seconds())})

		timer := time.NewTimer(wait)
		select {
		case <-stopCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			a.appendAppLog("info", "scheduler goroutine stopped", map[string]any{"component": "scheduler"})
			return
		case <-timer.C:
			a.runSchedulerOnceWithCommand("auto every 5 minutes")
		}
	}
}

func (a *App) runSchedulerOnce() { a.runSchedulerOnceWithCommand("manual") }

func (a *App) rerunLastSchedulerCycle() error {
	if a.schedulerState.LastRunUTC.IsZero() {
		return fmt.Errorf("предыдущая задача ещё не запускалась")
	}
	cmd := "rerun previous cycle"
	if a.schedulerState.LastCommand != "" {
		cmd = "rerun previous cycle: " + a.schedulerState.LastCommand
	}
	a.runSchedulerOnceWithCommand(cmd)
	return nil
}

func (a *App) runSchedulerOnceWithCommand(command string) {
	if command == "" {
		command = "unknown"
	}

	cycleStarted := time.Now()
	if !schedulerMu.TryLock() {
		utcNow := time.Now().UTC()
		a.appendAppLog("warn", "scheduler cycle skipped: previous cycle still running", map[string]any{
			"component": "scheduler",
			"command":   command,
			"ts_utc":    utcNow.Format(time.RFC3339),
		})
		_ = a.insertSystemTaskRun(
			"skipped",
			"Scheduler: предыдущий цикл ещё выполняется, новый запуск пропущен",
			schedulerPayloadJSON(map[string]any{"command": command, "reason": "previous_cycle_still_running", "ts_utc": utcNow.Format(time.RFC3339)}),
			command,
		)
		return
	}
	defer schedulerMu.Unlock()

	utcNow := time.Now().UTC()
	a.appendAppLog("info", "scheduler cycle started", map[string]any{"component": "scheduler", "command": command, "started_utc": utcNow.Format(time.RFC3339)})
	_ = a.insertSystemTaskRun("info", "Scheduler: цикл запущен", schedulerPayloadJSON(map[string]any{"command": command, "started_utc": utcNow.Format(time.RFC3339)}), command)

	settings, err := a.getSettings()
	if err != nil {
		log.Println("scheduler getSettings error:", err)
		a.appendAppLog("error", "scheduler getSettings error", map[string]any{"component": "scheduler", "error": err.Error(), "command": command})
		_ = a.insertSystemTaskRun("error", "Scheduler: ошибка чтения настроек: "+err.Error(), schedulerPayloadJSON(map[string]any{"error": err.Error()}), command)
		return
	}

	if !settings.SchedulerEnabled {
		msg := "Scheduler выключен в настройках"
		a.appendAppLog("info", "scheduler skipped because disabled", map[string]any{"component": "scheduler", "command": command})
		_ = a.insertSystemTaskRun("skipped", msg, schedulerPayloadJSON(map[string]any{"scheduler_enabled": false}), command)
		return
	}

	loc := time.UTC
	if timezone := settings.Timezone; timezone != "" {
		loaded, err := time.LoadLocation(timezone)
		if err == nil {
			loc = loaded
		} else {
			a.appendAppLog("warn", "scheduler timezone load failed; UTC used", map[string]any{"component": "scheduler", "timezone": timezone, "error": err.Error(), "command": command})
			_ = a.insertSystemTaskRun("warn", "Scheduler: timezone не загрузилась, используется UTC: "+err.Error(), schedulerPayloadJSON(map[string]any{"timezone": timezone, "error": err.Error()}), command)
		}
	}

	localNow := utcNow.In(loc)
	day := localNow.Day()
	hour := localNow.Hour()
	minute := localNow.Minute() / 5 * 5
	a.appendAppLog("info", "scheduler current time resolved", map[string]any{
		"component": "scheduler",
		"command":   command,
		"utc_now":   utcNow.Format(time.RFC3339),
		"local_now": localNow.Format(time.RFC3339),
		"timezone":  loc.String(),
		"day":       day,
		"hour":      hour,
		"minute":    minute,
	})

	items, err := a.getScheduledInvertersForExecution()
	if err != nil {
		log.Println("scheduler getScheduledInvertersForExecution error:", err)
		a.appendAppLog("error", "scheduler query error", map[string]any{"component": "scheduler", "error": err.Error(), "command": command})
		_ = a.insertSystemTaskRun("error", "Scheduler: ошибка выборки активных расписаний: "+err.Error(), schedulerPayloadJSON(map[string]any{"error": err.Error()}), command)
		return
	}

	a.appendAppLog("info", "scheduler active schedules loaded", map[string]any{"component": "scheduler", "command": command, "items_count": len(items), "filter": "use_timer_enabled"})
	if len(items) == 0 {
		msg := "Нет включённых расписаний. Проверь, что у инвертора включено расписание и оно сохранено."
		_ = a.insertSystemTaskRun("skipped", msg, schedulerPayloadJSON(map[string]any{"items_count": 0}), command)
		a.schedulerState.LastRunUTC = utcNow
		a.schedulerState.Processed = 0
		a.schedulerState.Triggered = 0
		a.schedulerState.LastCommand = command
		a.appendAppLog("warn", "scheduler cycle completed without active schedules", map[string]any{"component": "scheduler", "command": command})
		return
	}

	processed, triggered := 0, 0
	for _, item := range items {
		processed++
		itemPayload := map[string]any{
			"inverter": map[string]any{"id": item.InverterID, "name": item.InverterName, "ip": item.IP, "port": item.Port},
			"template": map[string]any{"id": item.TemplateID, "name": item.TemplateName},
			"schedule": map[string]any{"name": item.ScheduleName},
			"day":      day,
			"hour":     hour,
			"minute":   minute,
		}
		a.appendAppLog("info", "scheduler processing inverter", map[string]any{
			"component":     "scheduler",
			"command":       command,
			"inverter_id":   item.InverterID,
			"inverter_name": item.InverterName,
			"ip":            item.IP,
			"port":          item.Port,
			"template_id":   item.TemplateID,
			"template_name": item.TemplateName,
			"schedule_name": item.ScheduleName,
		})
		_ = a.insertTaskRun(item.InverterID, item.TemplateID, "info", fmt.Sprintf("Scheduler: обработка инвертора %s:%d", item.IP, item.Port), schedulerPayloadJSON(itemPayload), command)

		if strings.Contains(strings.ToLower(command), "auto") && !scheduleUseTimerEnabled(item.ScheduleJSON) {
			msg := "Use Timer выключен в расписании: автоматическая отправка пропущена"
			itemPayload["use_timer_mask"] = scheduleUseTimerMask(item.ScheduleJSON)
			a.appendAppLog("info", "scheduler skipped: use timer disabled", map[string]any{"component": "scheduler", "command": command, "inverter_id": item.InverterID, "use_timer_mask": scheduleUseTimerMask(item.ScheduleJSON)})
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "skipped", msg, schedulerPayloadJSON(itemPayload), command)
			continue
		}

		model, modelErr := findInverterModel(item.ModelKey)
		if modelErr != nil {
			itemPayload["profile_validation_error"] = modelErr.Error()
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "error", "Scheduler: неизвестный профиль инвертора: "+modelErr.Error(), schedulerPayloadJSON(itemPayload), command)
			continue
		}
		if model.WriteRequiresConfirm && !item.ProfileWriteConfirmed {
			msg := "Scheduler: запись заблокирована — профиль требует read-only проверки и явного подтверждения в настройках инвертора"
			itemPayload["profile_write_confirmation_required"] = true
			a.appendAppLog("warn", "scheduler write blocked: profile confirmation required", map[string]any{"component": "scheduler", "command": command, "inverter_id": item.InverterID, "model_key": model.Key})
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "blocked", msg, schedulerPayloadJSON(itemPayload), command)
			continue
		}
		if err := validateScheduleJSONForModels(item.ScheduleJSON, []InverterModelDefinition{model}); err != nil {
			itemPayload["schedule_validation_error"] = err.Error()
			a.appendAppLog("error", "scheduler schedule preflight failed", map[string]any{"component": "scheduler", "command": command, "inverter_id": item.InverterID, "model_key": model.Key, "error": err.Error()})
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "error", "Scheduler: расписание не прошло проверку профиля; подключение к Modbus не выполнялось: "+err.Error(), schedulerPayloadJSON(itemPayload), command)
			continue
		}

		tcpStarted := time.Now()
		if !checkTCPPort(item.IP, item.Port, 500*time.Millisecond) {
			msg := fmt.Sprintf("Инвертор недоступен по TCP: %s:%d", item.IP, item.Port)
			itemPayload["tcp_check"] = map[string]any{"ok": false, "duration_ms": time.Since(tcpStarted).Milliseconds(), "timeout_ms": 500}
			a.appendAppLog("warn", "scheduler tcp check failed", map[string]any{
				"component":   "scheduler",
				"command":     command,
				"inverter_id": item.InverterID,
				"ip":          item.IP,
				"port":        item.Port,
				"duration_ms": time.Since(tcpStarted).Milliseconds(),
			})
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "skipped", msg, schedulerPayloadJSON(itemPayload), command)
			continue
		}
		itemPayload["tcp_check"] = map[string]any{"ok": true, "duration_ms": time.Since(tcpStarted).Milliseconds(), "timeout_ms": 500}
		a.appendAppLog("debug", "scheduler tcp check ok", map[string]any{"component": "scheduler", "command": command, "inverter_id": item.InverterID, "ip": item.IP, "port": item.Port, "duration_ms": time.Since(tcpStarted).Milliseconds()})

		payload, plan, ok, err := a.buildExecutionPayload(item, day, hour, minute, utcNow, localNow, settings.Timezone, settings.SchedulerFillAllPointsCurrent)
		if err != nil {
			itemPayload["build_payload_error"] = err.Error()
			a.appendAppLog("error", "scheduler payload build failed", map[string]any{"component": "scheduler", "command": command, "inverter_id": item.InverterID, "error": err.Error()})
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "error", "Scheduler: ошибка подготовки payload: "+err.Error(), schedulerPayloadJSON(itemPayload), command)
			continue
		}
		if !ok {
			msg := fmt.Sprintf("Для текущего времени не найден слот: день=%d %02d:%02d", day, hour, minute)
			itemPayload["selected_day"] = day
			itemPayload["selected_hour"] = hour
			itemPayload["selected_minute"] = minute
			a.appendAppLog("info", "scheduler skipped: current slot not found", map[string]any{"component": "scheduler", "command": command, "inverter_id": item.InverterID, "day": day, "hour": hour, "minute": minute})
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "skipped", msg, schedulerPayloadJSON(itemPayload), command)
			continue
		}

		slotConfig, _ := payload["slot_config"].(MinuteSlot)
		effectiveSellTime := currentHourSellTime(slotConfig.Hour, slotConfig.Minute)
		payload["effective_sell_time"] = effectiveSellTime
		a.appendAppLog("debug", "scheduler selected slot config", map[string]any{
			"component":           "scheduler",
			"command":             command,
			"inverter_id":         item.InverterID,
			"day":                 day,
			"hour":                slotConfig.Hour,
			"minute":              slotConfig.Minute,
			"label":               slotConfig.Label,
			"point":               slotConfig.Point,
			"effective_sell_time": effectiveSellTime,
			"sell_mode_kw":        slotConfig.SellModeKW,
			"charge_mode":         slotConfig.ChargeMode,
			"use_timer":           slotConfig.UseTimer,
			"use_timer_mask":      slotConfig.UseTimerMask,
			"priority_load":       slotConfig.PriorityLoad,
		})

		// Modbus получает уже готовый план, собранный только из schedule_json конкретного инвертора.
		// Повторно расписание здесь не пересчитываем, чтобы preview/registers и реальная запись совпадали 1:1.
		writeResults, err := a.sendScheduleExecutionPlanToInverterWithCommand(item, plan, command)
		writeSummary := summarizeScheduleWriteResults(writeResults)
		payload["modbus_writes"] = writeResults
		payload["modbus_summary"] = writeSummary
		payload["modbus_failed"] = scheduleWriteFailedDetails(writeResults)
		if err != nil {
			payload["modbus_error"] = err.Error()
			encoded, _ := json.MarshalIndent(payload, "", "  ")
			a.appendAppLog("error", "scheduler modbus write failed", map[string]any{
				"component":   "scheduler",
				"inverter_id": item.InverterID,
				"template_id": item.TemplateID,
				"summary":     writeSummary,
				"error":       err.Error(),
				"payload":     json.RawMessage(encoded),
				"command":     command,
			})
			_ = a.insertTaskRun(item.InverterID, item.TemplateID, "error", "Scheduler: ошибка записи Modbus: "+err.Error(), string(encoded), command)
			continue
		}

		encoded, _ := json.MarshalIndent(payload, "", "  ")
		log.Printf("scheduler payload: %s\n", string(encoded))
		status := "executed"
		message := fmt.Sprintf("Scheduler: запись Modbus выполнена. ok=%d, errors=%d, skipped=%d", writeSummary.OK, writeSummary.Error, writeSummary.Skipped)
		if writeSummary.Error > 0 || writeSummary.Skipped > 0 {
			status = "warn"
		}
		a.appendAppLog("info", "scheduler payload generated and modbus writes completed", map[string]any{
			"component":   "scheduler",
			"inverter_id": item.InverterID,
			"template_id": item.TemplateID,
			"summary":     writeSummary,
			"payload":     json.RawMessage(encoded),
			"command":     command,
		})
		_ = a.insertTaskRun(item.InverterID, item.TemplateID, status, message, string(encoded), command)
		triggered++
	}
	a.schedulerState.LastRunUTC = utcNow
	a.schedulerState.Processed = processed
	a.schedulerState.Triggered = triggered
	a.schedulerState.LastCommand = command
	_ = a.trimTaskRunLogs(settings.TaskLogMaxRows)
	_ = a.rotateAppLogIfNeeded(settings.AppLogMaxSizeMB)
	durationMS := time.Since(cycleStarted).Milliseconds()
	completedPayload := schedulerPayloadJSON(map[string]any{
		"processed":        processed,
		"triggered":        triggered,
		"timezone":         settings.Timezone,
		"command":          command,
		"interval_minutes": settings.SchedulerIntervalMinutes,
		"duration_ms":      durationMS,
	})
	_ = a.insertSystemTaskRun("info", fmt.Sprintf("Scheduler: цикл завершён. Обработано: %d, записано: %d", processed, triggered), completedPayload, command)
	a.appendAppLog("info", "scheduler cycle completed", map[string]any{"component": "scheduler", "processed": processed, "triggered": triggered, "tz": settings.Timezone, "command": command, "interval_minutes": settings.SchedulerIntervalMinutes, "duration_ms": durationMS})
}

func (a *App) buildExecutionPayload(item runtimeScheduledItem, day int, hour int, minute int, utcNow, localNow time.Time, timezone string, fillAllPointsWithCurrent bool) (map[string]any, ScheduleExecutionPlan, bool, error) {
	plan, ok, err := buildScheduleExecutionPlanFromJSON(item.ScheduleJSON, day, hour, minute, fillAllPointsWithCurrent)
	if err != nil || !ok {
		return nil, plan, ok, err
	}

	slot := plan.Slot
	payloadMap := map[string]any{
		"generated_at_utc":                  utcNow.Format(time.RFC3339),
		"generated_at_local":                localNow.Format(time.RFC3339),
		"timezone":                          timezone,
		"day":                               day,
		"hour":                              slot.Label,
		"slot_minute":                       minute,
		"inverter":                          map[string]any{"id": item.InverterID, "name": item.InverterName, "ip": item.IP, "port": item.Port, "model_key": item.ModelKey, "model_name": item.ModelName, "parameters_file": item.ParametersFile},
		"template":                          map[string]any{"id": item.TemplateID, "name": item.TemplateName},
		"schedule":                          map[string]any{"name": item.ScheduleName, "json": json.RawMessage(item.ScheduleJSON)},
		"slot_config":                       slot,
		"slot_source":                       plan.Source,
		"schedule_source":                   "schedule_json_only",
		"candidate_count_same_day":          plan.CandidateCount,
		"fill_all_points_with_current_slot": plan.FillAllPointsWithCurrent,
		"point_configs":                     plan.PointConfigs,
		"registers":                         plan.Registers,
	}
	return payloadMap, plan, true, nil
}

func schedulerPayloadJSON(payload map[string]any) string {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func boolTo255(v bool) int {
	if v {
		return 255
	}
	return 0
}
