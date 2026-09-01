package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) schedulesPageHandler(w http.ResponseWriter, r *http.Request) {
	ids := parseIDsCSV(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		inverters, err := a.getAllInvertersNoCheck()
		if err != nil || len(inverters) == 0 {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/schedules?ids="+strconv.FormatInt(inverters[0].ID, 10), http.StatusSeeOther)
		return
	}
	inverters, err := a.getBasicInvertersByIDs(ids)
	if err != nil || len(inverters) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	scheduleName := "Расписание"
	viewMode := "list"
	scheduleJSON := defaultScheduleJSONForEditor()
	if len(ids) == 1 {
		s, err := a.getScheduleByInverterID(ids[0])
		if err == nil && s.ID > 0 {
			if strings.TrimSpace(s.Name) != "" {
				scheduleName = s.Name
			}
			if strings.TrimSpace(s.ViewMode) != "" {
				viewMode = s.ViewMode
			}
			if strings.TrimSpace(s.ScheduleJSON) != "" {
				scheduleJSON = compactScheduleJSONForEditor(s.ScheduleJSON)
			}
		}
	}
	templates, err := a.getTemplateOptions()
	if err != nil {
		http.Error(w, "Ошибка загрузки шаблонов", http.StatusInternalServerError)
		log.Println("getTemplates error:", err)
		return
	}
	schedulePowerMaxW := 0
	gridExportMaxW := 0
	modelsForPage := make([]InverterModelDefinition, 0, len(inverters))
	for _, inv := range inverters {
		model, modelErr := findInverterModel(inv.ModelKey)
		if modelErr != nil {
			http.Error(w, "Ошибка определения профиля инвертора: "+modelErr.Error(), http.StatusBadRequest)
			return
		}
		modelsForPage = append(modelsForPage, model)
		if schedulePowerMaxW == 0 || model.SchedulePowerMaxW < schedulePowerMaxW {
			schedulePowerMaxW = model.SchedulePowerMaxW
		}
		if gridExportMaxW == 0 || model.GridExportMaxW < gridExportMaxW {
			gridExportMaxW = model.GridExportMaxW
		}
	}
	data := SchedulesPageData{Title: "Редактирование расписания", Inverters: inverters, ScheduleName: scheduleName, ViewMode: viewMode, ScheduleJSON: scheduleJSON, Templates: templates, SelectedIDsCSV: joinIDs(ids), ChargeModeOptionsJSON: chargeModeOptionsJSONForModels(modelsForPage), SchedulePowerMaxW: schedulePowerMaxW, GridExportMaxW: gridExportMaxW}
	if err := a.tmplSchedules.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы расписаний", http.StatusInternalServerError)
		log.Println("tmplSchedules.Execute error:", err)
	}
}

func (a *App) toggleSchedulesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	ids := parseIDsCSV(r.FormValue("ids"))
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не выбраны инверторы"})
		return
	}
	enabled := r.FormValue("enabled") == "1"
	for _, id := range ids {
		if err := a.ensureScheduleExists(id); err != nil {
			log.Println("ensureScheduleExists error:", err)
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка создания расписания"})
			return
		}
		if err := a.toggleScheduleByInverterID(id, enabled); err != nil {
			log.Println("toggleScheduleByInverterID error:", err)
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка изменения Use Timer"})
			return
		}
	}
	msg := "Use Timer выключен"
	if enabled {
		msg = "Use Timer включён"
	}
	a.recreateActiveSchedulerTask("schedule use timer updated")
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: msg + ", задача пересоздана"})
}

func (a *App) saveSchedulesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	ids := parseIDsCSV(r.FormValue("ids"))
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не выбраны инверторы"})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	viewMode := strings.TrimSpace(r.FormValue("view_mode"))
	scheduleJSON := strings.TrimSpace(r.FormValue("schedule_json"))
	templateIDStr := strings.TrimSpace(r.FormValue("template_id"))
	if name == "" {
		name = "Расписание"
	}
	if viewMode != "grid" {
		viewMode = "list"
	}
	if scheduleJSON == "" {
		scheduleJSON = defaultScheduleJSON()
	}
	if !isValidScheduleJSON(scheduleJSON) {
		if normalized, err := normalizeScheduleJSONSellTimes(scheduleJSON); err == nil {
			scheduleJSON = normalized
		}
		if !isValidScheduleJSON(scheduleJSON) {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON расписания"})
			return
		}
	}
	selectedInverters, err := a.getBasicInvertersByIDs(ids)
	if err != nil || len(selectedInverters) != len(ids) {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не удалось определить модели выбранных инверторов"})
		return
	}
	modelsForValidation := make([]InverterModelDefinition, 0, len(selectedInverters))
	for _, inv := range selectedInverters {
		model, modelErr := findInverterModel(inv.ModelKey)
		if modelErr != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: modelErr.Error()})
			return
		}
		modelsForValidation = append(modelsForValidation, model)
	}
	if err := validateScheduleJSONForModels(scheduleJSON, modelsForValidation); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Расписание не прошло проверку: " + err.Error()})
		return
	}
	if templateIDStr != "" && templateIDStr != "0" {
		templateID, err := strconv.ParseInt(templateIDStr, 10, 64)
		if err != nil || templateID <= 0 {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный template_id"})
			return
		}
		if err := a.saveSchedulesForInverters(ids, templateID, name, viewMode, scheduleJSON); err != nil {
			log.Println("saveSchedulesForInverters with template error:", err)
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения расписания"})
			return
		}
	} else {
		if err := a.saveSchedulesForInverters(ids, 0, name, viewMode, scheduleJSON); err != nil {
			log.Println("saveSchedulesForInverters error:", err)
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения расписания"})
			return
		}
	}
	a.recreateActiveSchedulerTask("schedule saved")
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Расписание сохранено, задача пересоздана", Redirect: "/"})
}

func (a *App) previewScheduleRegisterWritesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}

	scheduleJSON := strings.TrimSpace(r.FormValue("schedule_json"))
	if scheduleJSON == "" {
		scheduleJSON = defaultScheduleJSON()
	}
	payload, err := parseSchedulePayloadLoose(scheduleJSON)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка разбора schedule_json: " + err.Error()})
		return
	}
	normalizeSchedulePayloadSellTimes(&payload)

	settings, settingsErr := a.getSettings()
	timezone := "UTC"
	loc := time.UTC
	if settingsErr == nil && strings.TrimSpace(settings.Timezone) != "" {
		timezone = settings.Timezone
		if loaded, loadErr := time.LoadLocation(settings.Timezone); loadErr == nil {
			loc = loaded
		} else {
			timezone = "UTC"
		}
	}
	localNow := time.Now().In(loc)
	day := localNow.Day()
	hour := localNow.Hour()
	minute := localNow.Minute() / 5 * 5

	ids := parseIDsCSV(r.FormValue("ids"))
	inverterRows := make([]map[string]any, 0, len(ids))
	previewModelKeys := make([]string, 0)
	seenPreviewModels := map[string]bool{}
	if len(ids) > 0 {
		if inverters, invErr := a.getBasicInvertersByIDs(ids); invErr == nil {
			for _, inv := range inverters {
				inverterRows = append(inverterRows, map[string]any{"id": inv.ID, "name": inv.Name, "ip": inv.IP, "port": inv.Port, "model_key": inv.ModelKey, "model_name": inv.ModelName, "parameters_file": inv.ParametersFile})
				if !seenPreviewModels[inv.ModelKey] {
					previewModelKeys = append(previewModelKeys, inv.ModelKey)
					seenPreviewModels[inv.ModelKey] = true
				}
			}
		}
	}
	if len(previewModelKeys) == 0 {
		previewModelKeys = append(previewModelKeys, defaultInverterModelKey)
	}

	plan, slotFound, planErr := buildScheduleExecutionPlanFromJSON(scheduleJSON, day, hour, minute, settingsErr == nil && settings.SchedulerFillAllPointsCurrent)
	if planErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"message": "Ошибка подготовки preview: " + planErr.Error(),
		})
		return
	}
	if !slotFound {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                 true,
			"will_send":          false,
			"message":            "Текущий день или ближайшая заполненная точка не найдены в расписании, отправки не будет",
			"day":                day,
			"hour":               hour,
			"minute":             minute,
			"timezone":           timezone,
			"generated_at_local": localNow.Format(time.RFC3339),
			"use_timer_mask":     payload.UseTimerMask,
			"inverters":          inverterRows,
			"rows":               []map[string]any{},
		})
		return
	}

	slot := plan.Slot
	values := plan.Values

	profiles := make([]map[string]any, 0, len(previewModelKeys))
	rows := []map[string]any{}
	profileErrors := []string{}
	for _, modelKey := range previewModelKeys {
		model, params, loadErr := loadDeviceParametersForModel(modelKey)
		if loadErr != nil {
			profileErrors = append(profileErrors, loadErr.Error())
			continue
		}
		paramByCode := deviceParametersByCode(params)
		modelRows := make([]map[string]any, 0, len(values))
		for idx, item := range values {
			row := map[string]any{"order": idx + 1, "code": item.Code, "schedule_value": item.Value, "note": item.Note}
			if field, ok := paramByCode[strings.ToLower(strings.TrimSpace(item.Code))]; ok {
				row["address"] = field.ModbusAddress
				row["addresses"] = deviceParameterAddresses(field)
				row["register_type"] = field.RegisterType
				row["writable"] = field.IsWritable
				row["name"] = field.Name
				rawValue, logicalValue, encodeErr := encodeScheduleDeviceParameterValue(item.Value, field)
				row["logical_value"] = logicalValue
				row["schedule_input_scale_factor"] = deviceParameterScheduleInputScale(field)
				if encodeErr != nil {
					row["encode_error"] = encodeErr.Error()
					row["raw_value"] = nil
				} else {
					row["raw_value"] = rawValue
					row["value"] = rawValue
				}
			} else {
				row["address"] = nil
				row["addresses"] = nil
				row["register_type"] = ""
				row["writable"] = nil
				row["raw_value"] = nil
			}
			modelRows = append(modelRows, row)
		}
		profiles = append(profiles, map[string]any{"model_key": model.Key, "model_name": model.Name, "parameters_file": model.ParametersFile, "validation_status": model.ValidationStatus, "rows": modelRows})
		if len(rows) == 0 {
			rows = modelRows
		}
	}

	source := plan.Source
	message := "Предпросмотр готов по профилям выбранных инверторов"
	if len(profileErrors) > 0 {
		message = "Предпросмотр сформирован частично: " + strings.Join(profileErrors, "; ")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                                true,
		"will_send":                         true,
		"message":                           message,
		"day":                               day,
		"hour":                              hour,
		"minute":                            minute,
		"timezone":                          timezone,
		"generated_at_local":                localNow.Format(time.RFC3339),
		"use_timer_mask":                    payload.UseTimerMask,
		"schedule_source":                   "schedule_json_only",
		"fill_all_points_with_current_slot": plan.FillAllPointsWithCurrent,
		"candidate_count_same_day":          plan.CandidateCount,
		"inverters":                         inverterRows,
		"slot": map[string]any{
			"hour":                    slot.Hour,
			"minute":                  slot.Minute,
			"label":                   slot.Label,
			"source":                  source,
			"sell_time":               slot.SellTime,
			"sell_mode_kw":            slot.SellModeKW,
			"sell_mode_batt_capacity": slot.SellModeBattCapacity,
			"charge_mode":             slot.ChargeMode,
			"grid_export_limit":       slot.GridExportLimit,
			"grid_charge_enabled":     slot.GridChargeEnabled,
			"solar_export":            slot.SolarExport,
			"load_limit_mode":         slot.LoadLimitMode,
			"priority_load":           normalizePriorityLoadValue(slot.PriorityLoad),
		},
		"rows":     rows,
		"profiles": profiles,
	})
}
