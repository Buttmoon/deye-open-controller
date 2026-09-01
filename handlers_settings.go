package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (a *App) settingsPageHandler(w http.ResponseWriter, r *http.Request) {
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
	modelsCatalog, err := availableInverterModels()
	if err != nil {
		http.Error(w, "Ошибка чтения каталога моделей: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := SettingsPageData{Title: "Настройки", Settings: settings, TimezoneOptions: getTimezoneOptions(), SettingsStats: stats, InverterModels: modelsCatalog, TestCommand: stats.SchedulerTestCommand}
	if err := a.tmplSettings.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы настроек", http.StatusInternalServerError)
		log.Println(err)
	}
}

func (a *App) tasksSettingsPageHandler(w http.ResponseWriter, r *http.Request) {
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
	data := TasksSettingsPageData{Title: "Периодические задачи", Settings: settings, SettingsStats: stats, TestCommand: stats.SchedulerTestCommand}
	if err := a.tmplTaskSettings.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы периодических задач", http.StatusInternalServerError)
		log.Println(err)
	}
}

func (a *App) restartPreviousTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := a.rerunLastSchedulerCycle(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Предыдущая задача перезапущена"})
}

func (a *App) apiDocsPageHandler(w http.ResponseWriter, r *http.Request) {
	data := APIDocsPageData{
		Title:                  "API документация",
		MarkdownHTML:           renderMarkdownDocument(string(apiDocumentationMD)),
		ExampleCreateInverter:  `{"name":"Инвертор 1","ip":"192.168.1.101","port":8899,"model_key":"deye_sun_25k_sg01hp3_eu_am2_v104","profile_write_confirmed":false}`,
		ExampleCreateTemplate:  `{"name":"Ночной режим","description":"Пример","view_mode":"list","schedule_json":{"days":[]}}`,
		ExampleBindTemplate:    `{"template_id":1,"ip":"192.168.1.101","port":8899}`,
		ExampleScheduleExport:  `GET /schedules/export?id=1 или GET /schedules/export?ids=1,2`,
		ExampleInverterLogList: `GET /api/inverter-logs`,
		ExampleInverterLogFile: `GET /api/inverter-logs/file?code=latest или GET /api/inverter-logs/file?name=inverter-20260430-120000.log`,
	}
	if err := a.tmplAPIDocs.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга API docs", http.StatusInternalServerError)
	}
}

func renderMarkdownDocument(md string) template.HTML {
	var b strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(md))
	inCode := false
	inTable := false
	inList := false

	closeTable := func() {
		if inTable {
			b.WriteString("</tbody></table></div>\n")
			inTable = false
		}
	}
	closeList := func() {
		if inList {
			b.WriteString("</ul>\n")
			inList = false
		}
	}

	formatInline := func(s string) string {
		esc := html.EscapeString(s)
		esc = regexp.MustCompile("`([^`]+)`").ReplaceAllString(esc, "<code>$1</code>")
		return esc
	}

	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			closeTable()
			closeList()
			if inCode {
				b.WriteString("</code></pre>\n")
				inCode = false
			} else {
				inCode = true
				b.WriteString("<pre><code>")
			}
			continue
		}
		if inCode {
			b.WriteString(html.EscapeString(line))
			b.WriteByte('\n')
			continue
		}
		if trim == "" || trim == "---" {
			closeTable()
			closeList()
			if trim == "---" {
				b.WriteString("<hr>\n")
			}
			continue
		}
		if strings.HasPrefix(trim, "|") && strings.HasSuffix(trim, "|") {
			closeList()
			cells := strings.Split(strings.Trim(trim, "|"), "|")
			isSeparator := true
			for _, c := range cells {
				cc := strings.TrimSpace(c)
				if strings.Trim(cc, "-: ") != "" {
					isSeparator = false
					break
				}
			}
			if isSeparator {
				continue
			}
			if !inTable {
				b.WriteString("<div class=\"md-table-wrap\"><table class=\"md-table\"><tbody>\n")
				inTable = true
			}
			b.WriteString("<tr>")
			for _, c := range cells {
				b.WriteString("<td>")
				b.WriteString(formatInline(strings.TrimSpace(c)))
				b.WriteString("</td>")
			}
			b.WriteString("</tr>\n")
			continue
		}
		closeTable()
		if strings.HasPrefix(trim, "# ") {
			closeList()
			b.WriteString("<h1>" + formatInline(strings.TrimSpace(trim[2:])) + "</h1>\n")
			continue
		}
		if strings.HasPrefix(trim, "## ") {
			closeList()
			b.WriteString("<h2>" + formatInline(strings.TrimSpace(trim[3:])) + "</h2>\n")
			continue
		}
		if strings.HasPrefix(trim, "### ") {
			closeList()
			b.WriteString("<h3>" + formatInline(strings.TrimSpace(trim[4:])) + "</h3>\n")
			continue
		}
		if strings.HasPrefix(trim, "- ") {
			if !inList {
				b.WriteString("<ul>\n")
				inList = true
			}
			b.WriteString("<li>" + formatInline(strings.TrimSpace(trim[2:])) + "</li>\n")
			continue
		}
		closeList()
		b.WriteString("<p>" + formatInline(trim) + "</p>\n")
	}
	closeTable()
	closeList()
	if inCode {
		b.WriteString("</code></pre>\n")
	}
	return template.HTML(b.String())
}

func (a *App) updateTimezoneHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	timezone := strings.TrimSpace(r.FormValue("timezone"))
	if timezone == "" {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Timezone обязательна"})
		return
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректная timezone"})
		return
	}
	if err := a.updateSettingsTimezone(timezone); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения timezone"})
		return
	}
	a.appendAppLog("info", "timezone updated", map[string]any{"timezone": timezone})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Timezone успешно сохранена"})
}

func (a *App) updateSchedulerToggleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	enabled := strings.TrimSpace(r.FormValue("enabled")) == "1"
	if err := a.updateSchedulerEnabled(enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения настройки scheduler"})
		return
	}
	a.restartScheduler("scheduler toggle updated")
	a.appendAppLog("info", "scheduler toggle updated", map[string]any{"enabled": enabled})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Настройка scheduler обновлена, scheduler перезапущен"})
}

func (a *App) updateFileLoggingToggleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	enabled := strings.TrimSpace(r.FormValue("enabled")) == "1"
	if err := a.updateFileLoggingEnabled(enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения настройки логирования"})
		return
	}
	a.appendAppLog("info", "file logging toggle updated", map[string]any{"enabled": enabled})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Настройка логирования обновлена"})
}

func (a *App) updateRuntimeSettingsHandler(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := r.Form["application_name"]; ok {
		settings.ApplicationName = strings.TrimSpace(r.FormValue("application_name"))
		if len([]rune(settings.ApplicationName)) > 120 {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Имя приложения должно быть не длиннее 120 символов"})
			return
		}
	}
	settings.SchedulerEnabled = strings.TrimSpace(r.FormValue("scheduler_enabled")) == "1"
	settings.FileLoggingEnabled = strings.TrimSpace(r.FormValue("file_logging_enabled")) == "1"
	if _, ok := r.Form["scheduler_fill_all_points_current"]; ok {
		settings.SchedulerFillAllPointsCurrent = strings.TrimSpace(r.FormValue("scheduler_fill_all_points_current")) == "1"
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("scheduler_interval_minutes"))); err == nil && v > 0 {
		settings.SchedulerIntervalMinutes = v
	}
	settings.LogLevel = strings.ToUpper(strings.TrimSpace(r.FormValue("log_level")))
	if settings.LogLevel == "" {
		settings.LogLevel = "INFO"
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("app_log_max_size_mb"))); err == nil && v > 0 {
		settings.AppLogMaxSizeMB = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("task_log_max_rows"))); err == nil && v > 0 {
		settings.TaskLogMaxRows = v
	}
	if err := a.updateRuntimeSettings(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения runtime настроек"})
		return
	}
	a.recreateActiveSchedulerTask("runtime settings updated")
	a.appendAppLog("info", "runtime settings updated", map[string]any{"application_name": settings.ApplicationName, "scheduler_interval_minutes": settings.SchedulerIntervalMinutes, "log_level": settings.LogLevel, "scheduler_enabled": settings.SchedulerEnabled, "file_logging_enabled": settings.FileLoggingEnabled, "scheduler_fill_all_points_current": settings.SchedulerFillAllPointsCurrent})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Настройки обновлены, scheduler перезапущен"})
}

func (a *App) runSchedulerNowHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	cmd := "curl -X POST http://localhost:8080/api/tasks/run-now"
	a.appendAppLog("info", "manual scheduler run requested", map[string]any{"source": "http", "command": cmd})
	go a.runSchedulerOnceWithCommand(cmd)
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Почасовая задача запущена вручную"})
}

func (a *App) clearLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		_ = r.ParseForm()
		kind = strings.TrimSpace(r.FormValue("kind"))
	}
	switch kind {
	case "app":
		if err := a.clearAppLogs(); err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка очистки app логов"})
			return
		}
	case "tasks", "task":
		if err := a.clearTaskRunLogs(); err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка очистки task логов"})
			return
		}
	case "all":
		if err := a.clearAppLogs(); err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка очистки app логов"})
			return
		}
		if err := a.clearTaskRunLogs(); err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка очистки task логов"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Неизвестный тип логов"})
		return
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Логи очищены"})
}

func (a *App) clearDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := a.clearDatabaseData(); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка очистки БД"})
		return
	}
	a.schedulerState = SchedulerState{}
	a.restartScheduler("database cleared")
	a.appendAppLog("warn", "database data cleared from settings page", map[string]any{"component": "settings"})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Данные БД очищены, scheduler перезапущен"})
}

func (a *App) clearSchedulesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	rows, err := a.clearSchedulesData()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка очистки расписаний: " + err.Error()})
		return
	}
	a.schedulerState = SchedulerState{}
	a.restartScheduler("schedules cleared")
	msg := fmt.Sprintf("Расписания очищены: удалено %d. Scheduler перезапущен", rows)
	a.appendAppLog("warn", "schedules cleared from settings page", map[string]any{"component": "settings", "deleted_rows": rows})
	_ = a.insertSystemTaskRun("warn", "Scheduler: расписания очищены через настройки", schedulerPayloadJSON(map[string]any{"deleted_rows": rows}), "settings clear schedules")
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: msg})
}

// API CRUD
func (a *App) apiInvertersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		items, _, err := a.getPaginatedInverters("", 1, 100000)
		if err != nil {
			http.Error(w, `{"ok":false}`, http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "items": items})
	case http.MethodPost:
		var in struct {
			Name                  string `json:"name"`
			IP                    string `json:"ip"`
			Port                  int    `json:"port"`
			ModelKey              string `json:"model_key"`
			ProfileWriteConfirmed bool   `json:"profile_write_confirmed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, `{"ok":false,"message":"bad json"}`, http.StatusBadRequest)
			return
		}
		if in.Port == 0 {
			in.Port = 8899
		}
		id, err := a.createInverter(in.Name, in.IP, in.Port, in.ModelKey, in.ProfileWriteConfirmed)
		if err != nil {
			http.Error(w, `{"ok":false,"message":"create failed"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) apiInverterModelsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	modelsCatalog, err := availableInverterModels()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(modelsCatalog))
	for _, model := range modelsCatalog {
		_, params, profileErr := loadDeviceParametersForModel(model.Key)
		item := map[string]any{
			"key": model.Key, "manufacturer": model.Manufacturer, "name": model.Name, "model_code": model.ModelCode,
			"parameters_file": model.ParametersFile, "rated_power_w": model.RatedPowerW, "phase_count": model.PhaseCount,
			"mppt_count": model.MPPTCount, "protocol": model.Protocol, "validation_status": model.ValidationStatus,
			"documentation": model.Documentation, "notes": model.Notes, "default": model.Default,
			"parameter_count": len(params), "profile_valid": profileErr == nil,
		}
		if profileErr != nil {
			item["profile_error"] = profileErr.Error()
		}
		items = append(items, item)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "schema_version": 1, "items": items})
}

func (a *App) apiTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		items, _, err := a.getPaginatedTemplateList("", 1, 100000)
		if err != nil {
			http.Error(w, `{"ok":false}`, http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "items": items})
	case http.MethodPost:
		var in struct {
			Name, Description, ViewMode string
			ScheduleJSON                json.RawMessage `json:"schedule_json"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, `{"ok":false,"message":"bad json"}`, http.StatusBadRequest)
			return
		}
		if in.ViewMode == "" {
			in.ViewMode = "list"
		}
		raw := string(in.ScheduleJSON)
		if raw == "" || raw == "null" {
			raw = defaultScheduleJSON()
		}
		if err := a.createTemplate(in.Name, in.Description, in.ViewMode, raw); err != nil {
			http.Error(w, fmt.Sprintf(`{"ok":false,"message":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) apiTemplateByIDHandler(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/templates/")
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"ok":false}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		tpl, err := a.getTemplateByID(id)
		if err != nil {
			http.Error(w, `{"ok":false}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "item": tpl})
	case http.MethodPut, http.MethodPost:
		var in struct {
			Name, Description, ViewMode string
			ScheduleJSON                json.RawMessage `json:"schedule_json"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, `{"ok":false}`, http.StatusBadRequest)
			return
		}
		raw := string(in.ScheduleJSON)
		if raw == "" || raw == "null" {
			raw = defaultScheduleJSON()
		}
		if err := a.updateTemplate(id, in.Name, in.Description, in.ViewMode, raw); err != nil {
			http.Error(w, fmt.Sprintf(`{"ok":false,"message":%q}`, err.Error()), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *App) apiSchedulesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		InverterID     int64 `json:"inverter_id"`
		Name, ViewMode string
		ScheduleJSON   json.RawMessage `json:"schedule_json"`
		TemplateID     int64           `json:"template_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, `{"ok":false}`, http.StatusBadRequest)
		return
	}
	raw := string(in.ScheduleJSON)
	if raw == "" || raw == "null" {
		raw = defaultScheduleJSON()
	}
	var err error
	if in.TemplateID > 0 {
		err = a.saveScheduleForInverterWithTemplate(in.InverterID, in.TemplateID, in.Name, in.ViewMode, raw)
	} else {
		err = a.saveScheduleForInverter(in.InverterID, in.Name, in.ViewMode, raw)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"message":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	a.recreateActiveSchedulerTask("api schedule saved")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "schedule saved, scheduler task recreated"})
}

func (a *App) apiBindTemplateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		TemplateID int64  `json:"template_id"`
		IP         string `json:"ip"`
		Port       int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, `{"ok":false}`, http.StatusBadRequest)
		return
	}
	if in.Port == 0 {
		in.Port = 8899
	}
	var inverterID int64
	if err := a.db.QueryRow(`SELECT id FROM inverters WHERE ip=? AND port=?`, in.IP, in.Port).Scan(&inverterID); err != nil {
		http.Error(w, `{"ok":false,"message":"inverter not found"}`, http.StatusNotFound)
		return
	}
	tpl, err := a.getTemplateByID(in.TemplateID)
	if err != nil {
		http.Error(w, `{"ok":false,"message":"template not found"}`, http.StatusNotFound)
		return
	}
	if err := a.saveScheduleForInverterWithTemplate(inverterID, tpl.ID, tpl.Name, tpl.ViewMode, tpl.ScheduleJSON); err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"message":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	a.recreateActiveSchedulerTask("api template bound to schedule")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": "template bound, scheduler task recreated"})
}
