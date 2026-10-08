package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// simpleTemplateFileFormat identifies exported simplified templates.
const simpleTemplateFileFormat = "inverter-simple-template"

type SimpleTemplate struct {
	ID                    int64        `json:"id,omitempty"`
	Name                  string       `json:"name"`
	Description           string       `json:"description"`
	Rules                 []SimpleRule `json:"rules"`
	CreatedAt             string       `json:"created_at,omitempty"`
	UpdatedAt             string       `json:"updated_at,omitempty"`
	Summary               []string     `json:"summary,omitempty"`
	Version               int          `json:"version"`
	Tags                  []string     `json:"tags,omitempty"`
	ModelKeys             []string     `json:"model_keys,omitempty"`
	LastAppliedInverterID int64        `json:"last_applied_inverter_id,omitempty"`
	LastAppliedUTC        string       `json:"last_applied_utc,omitempty"`
	LastAppliedStatus     string       `json:"last_applied_status,omitempty"`
	IntervalCount         int          `json:"interval_count"`
	LogicalDaySlots       int          `json:"logical_day_slots"`
	HardwareSlots         int          `json:"hardware_slots"`
	ExceedsHardware       bool         `json:"exceeds_hardware_slots"`
}

type simpleTemplateFile struct {
	Format    string           `json:"format"`
	Version   int              `json:"version"`
	Exported  string           `json:"exported_at"`
	Templates []SimpleTemplate `json:"templates"`
}

func validateSimpleTemplate(t SimpleTemplate) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("укажите название шаблона")
	}
	if len([]rune(t.Name)) > 120 {
		return fmt.Errorf("название не длиннее 120 символов")
	}
	if len(t.Rules) == 0 {
		return fmt.Errorf("шаблон «%s»: нет правил", t.Name)
	}
	for i, r := range t.Rules {
		if err := validateSimpleRule(i, r); err != nil {
			return fmt.Errorf("шаблон «%s»: %v", t.Name, err)
		}
	}
	return nil
}

func (a *App) listSimpleTemplates() ([]SimpleTemplate, error) {
	rows, err := a.db.Query(`SELECT id, name, description, rules_json, created_at, updated_at,
		COALESCE(version,1), COALESCE(tags_json,'[]'), COALESCE(model_keys_json,'[]'),
		last_applied_inverter_id, COALESCE(last_applied_utc,''), COALESCE(last_applied_status,'')
		FROM simple_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SimpleTemplate{}
	for rows.Next() {
		var t SimpleTemplate
		var raw, tagsRaw, modelsRaw string
		var lastInv sqlNullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &raw, &t.CreatedAt, &t.UpdatedAt,
			&t.Version, &tagsRaw, &modelsRaw, &lastInv, &t.LastAppliedUTC, &t.LastAppliedStatus); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &t.Rules)
		t.Tags = decodeStringSlice(tagsRaw)
		t.ModelKeys = decodeStringSlice(modelsRaw)
		if lastInv.Valid {
			t.LastAppliedInverterID = lastInv.Int64
		}
		t.IntervalCount = len(t.Rules)
		t.HardwareSlots = hardwareTOUSlots
		_, _, t.LogicalDaySlots = analyzeSimpleIntervals(t.Rules, false)
		t.ExceedsHardware = t.LogicalDaySlots > hardwareTOUSlots
		for _, r := range t.Rules {
			t.Summary = append(t.Summary, describeSimpleRule(r))
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (a *App) upsertSimpleTemplate(t SimpleTemplate) (int64, error) {
	return a.upsertSimpleTemplateVersioned(t, "оператор", "")
}

func friendlyUniqueError(err error, name string) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("шаблон с названием «%s» уже существует", name)
	}
	return err
}

func (a *App) apiSimpleTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := a.listSimpleTemplates()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "items": items, "modes": simpleModeOptions(),
			"load_limit_options": loadLimitOptions(), "priority_options": priorityLoadOptions(),
			"hardware_slots": hardwareTOUSlots,
			"field_help": map[string]string{
				"grid_charge":   "Регистр 130 (grid_charge_enable): заряд от сети, 0/1.",
				"load_limit":    "Регистр 142 (inverter_work_mode), в профиле назван «Load Limit»: 0 — Selling first, 1 — Zero export load, 2 — Zero export CT. Это режим, не мощность в ваттах.",
				"priority_load": "Регистр 141 (priority_load): 0 — приоритет нагрузки (Load First), 1 — приоритет батареи (Battery First).",
			},
		})
	case http.MethodPost:
		var req struct {
			SimpleTemplate
			Action  string `json:"action"`
			Summary string `json:"change_summary"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 512<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
			return
		}
		switch req.Action {
		case "delete":
			if _, err := a.db.Exec(`DELETE FROM simple_templates WHERE id = ?`, req.ID); err != nil {
				writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
				return
			}
			_, _ = a.db.Exec(`DELETE FROM simple_template_versions WHERE template_id = ?`, req.ID)
			a.recordHistory(HistoryEntry{OperationType: opJobDelete, Status: histApplied, Initiator: requestInitiator(r), JobRef: fmt.Sprintf("simple_template:%d", req.ID), Message: "Удалён упрощённый шаблон"})
			writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Шаблон удалён"})
			return
		case "rename":
			name := strings.TrimSpace(req.Name)
			if name == "" {
				writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Укажите название"})
				return
			}
			if _, err := a.db.Exec(`UPDATE simple_templates SET name = ?, updated_at = ? WHERE id = ?`, name, time.Now().UTC().Format(time.RFC3339), req.ID); err != nil {
				writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: friendlyUniqueError(err, name).Error()})
				return
			}
			a.recordHistory(HistoryEntry{OperationType: opJobUpdate, Status: histApplied, Initiator: requestInitiator(r), JobRef: fmt.Sprintf("simple_template:%d", req.ID), Message: "Переименован шаблон «" + name + "»"})
			writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Название обновлено"})
			return
		case "duplicate":
			items, _ := a.listSimpleTemplates()
			for _, t := range items {
				if t.ID == req.ID {
					t.ID = 0
					t.Name = uniqueCopyName(t.Name, func(n string) bool {
						for _, x := range items {
							if x.Name == n {
								return true
							}
						}
						return false
					})
					id, err := a.upsertSimpleTemplateVersioned(t, requestInitiator(r), "Дублирование")
					if err != nil {
						writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
						return
					}
					a.recordHistory(HistoryEntry{OperationType: opJobCreate, Status: histApplied, Initiator: requestInitiator(r), JobRef: fmt.Sprintf("simple_template:%d", id), Message: "Создана копия упрощённого шаблона «" + t.Name + "»"})
					writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Создана копия «" + t.Name + "»", "id": id})
					return
				}
			}
			writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Шаблон не найден"})
			return
		}
		isNew := req.ID == 0
		id, err := a.upsertSimpleTemplateVersioned(req.SimpleTemplate, requestInitiator(r), req.Summary)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		op, msg := opJobUpdate, "Изменён упрощённый шаблон «"+req.Name+"» (без записи в инвертор)"
		if isNew {
			op, msg = opJobCreate, "Создан упрощённый шаблон «"+req.Name+"» (без записи в инвертор)"
		}
		a.recordHistory(HistoryEntry{OperationType: op, Status: histApplied, Initiator: requestInitiator(r), JobRef: fmt.Sprintf("simple_template:%d", id), Message: msg})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Шаблон сохранён в приложении. В инвертор ничего не записано.", "id": id})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
	}
}

func simpleModeOptions() []map[string]any {
	out := []map[string]any{}
	for _, key := range []string{"self", "grid_charge", "sell", "sell_grid"} {
		p := simpleModePresets[key]
		out = append(out, map[string]any{"value": key, "label": p.Label, "charge_mode": p.ChargeMode, "grid_charge": p.GridCharge})
	}
	out = append(out, map[string]any{"value": "custom", "label": "Пользовательский (выбрать режим TOU)"})
	return out
}

func uniqueCopyName(name string, exists func(string) bool) string {
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (копия)", name)
		if i > 1 {
			candidate = fmt.Sprintf("%s (копия %d)", name, i)
		}
		if !exists(candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s (копия %d)", name, time.Now().Unix())
}

func (a *App) apiSimpleTemplateExportHandler(w http.ResponseWriter, r *http.Request) {
	items, err := a.listSimpleTemplates()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if idRaw := r.URL.Query().Get("id"); idRaw != "" {
		id, _ := strconv.ParseInt(idRaw, 10, 64)
		filtered := []SimpleTemplate{}
		for _, t := range items {
			if t.ID == id {
				filtered = append(filtered, t)
			}
		}
		items = filtered
	}
	for i := range items {
		items[i].ID, items[i].Summary, items[i].CreatedAt, items[i].UpdatedAt = 0, nil, "", ""
	}
	file := simpleTemplateFile{Format: simpleTemplateFileFormat, Version: 1, Exported: time.Now().Format(time.RFC3339), Templates: items}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(file)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "simple-templates-"+time.Now().Format("20060102")+".json"))
	_, _ = w.Write(buf.Bytes())
}

func parseSimpleTemplateFile(data []byte) ([]SimpleTemplate, error) {
	var file simpleTemplateFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("файл не является JSON: %v", err)
	}
	if file.Format != simpleTemplateFileFormat {
		return nil, fmt.Errorf("неизвестный формат файла (ожидается format=%q)", simpleTemplateFileFormat)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("неподдерживаемая версия формата: %d", file.Version)
	}
	if len(file.Templates) == 0 {
		return nil, fmt.Errorf("файл не содержит шаблонов")
	}
	return file.Templates, nil
}

func (a *App) apiSimpleTemplateImportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ожидается файл (multipart/form-data)"})
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Файл не передан"})
		return
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 4<<20))
	templates, err := parseSimpleTemplateFile(data)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	dryRun := r.FormValue("dry_run") == "1"
	overwrite := r.FormValue("overwrite") == "1"
	existing, _ := a.listSimpleTemplates()
	byName := map[string]int64{}
	for _, t := range existing {
		byName[t.Name] = t.ID
	}
	results := []map[string]any{}
	created := 0
	for _, t := range templates {
		item := map[string]any{"name": t.Name, "rules": len(t.Rules)}
		if err := validateSimpleTemplate(t); err != nil {
			item["error"] = err.Error()
			results = append(results, item)
			continue
		}
		if id, ok := byName[strings.TrimSpace(t.Name)]; ok {
			if !overwrite {
				item["error"] = "шаблон с таким названием уже есть (включите «перезаписать»)"
				results = append(results, item)
				continue
			}
			t.ID = id
			item["action"] = "обновлён"
		} else {
			t.ID = 0
			item["action"] = "создан"
		}
		if !dryRun {
			if _, err := a.upsertSimpleTemplate(t); err != nil {
				item["error"] = err.Error()
				delete(item, "action")
				results = append(results, item)
				continue
			}
			created++
		}
		results = append(results, item)
	}
	if !dryRun && created > 0 {
		a.recordHistory(HistoryEntry{OperationType: opJobCreate, Status: histApplied, Initiator: requestInitiator(r), Message: fmt.Sprintf("Импортировано упрощённых шаблонов: %d", created)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dry_run": dryRun, "results": results, "imported": created})
}

// apiTemplateDuplicateHandler copies a classic (advanced) template.
func (a *App) apiTemplateDuplicateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	t, err := a.getTemplateByID(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Шаблон не найден"})
		return
	}
	all, _ := a.getTemplateOptions()
	name := uniqueCopyName(t.Name, func(n string) bool {
		for _, x := range all {
			if x.Name == n {
				return true
			}
		}
		return false
	})
	if err := a.createTemplate(name, t.Description, t.ViewMode, t.ScheduleJSON); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось создать копию: " + err.Error()})
		return
	}
	a.recordHistory(HistoryEntry{OperationType: opJobCreate, Status: histApplied, Initiator: requestInitiator(r), JobRef: fmt.Sprintf("template:%d", id), Message: "Создана копия шаблона «" + name + "»"})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Создана копия «" + name + "»"})
}

// apiTemplateImportPreviewHandler parses a legacy XLSX file and reports what
// would be imported, without saving anything.
func (a *App) apiTemplateImportPreviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ожидается XLSX-файл"})
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Файл не передан"})
		return
	}
	defer f.Close()
	imported, err := ParseTemplatesFromXLSX(f)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, jsonResponse{OK: false, Message: "Файл не распознан как шаблон расписания: " + err.Error()})
		return
	}
	existing, _ := a.getTemplateOptions()
	names := map[string]bool{}
	for _, t := range existing {
		names[t.Name] = true
	}
	defaultModel, _ := defaultInverterModel()
	items := []map[string]any{}
	for _, t := range imported {
		item := map[string]any{"name": t.Name, "exists": names[t.Name]}
		if err := validateScheduleJSONForModels(t.ScheduleJSON, []InverterModelDefinition{defaultModel}); err != nil {
			item["error"] = err.Error()
		}
		item["summary"] = summarizeSchedule(t.ScheduleJSON)
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": header.Filename, "templates": items})
}

type scheduleSummary struct {
	Days         int            `json:"days"`
	EnabledHours int            `json:"enabled_hours"`
	CustomSlots  int            `json:"custom_slots"`
	UseTimerMask int            `json:"use_timer_mask"`
	ChargeModes  map[string]int `json:"charge_modes"`
	MaxPowerW    int            `json:"max_power_w"`
	MaxExportW   int            `json:"max_export_w"`
}

func summarizeSchedule(raw string) scheduleSummary {
	s := scheduleSummary{ChargeModes: map[string]int{}}
	p, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		return s
	}
	normalizeSchedulePayloadSellTimes(&p)
	s.UseTimerMask = p.UseTimerMask
	for _, d := range p.Days {
		dayHas := false
		s.CustomSlots += len(d.CustomSlots)
		for _, h := range d.Hours {
			if !h.Enabled {
				continue
			}
			dayHas = true
			s.EnabledHours++
			s.ChargeModes[chargeModeLabelFromValue(h.ChargeMode)]++
			s.MaxPowerW = max(s.MaxPowerW, h.SellModeKW*10)
			s.MaxExportW = max(s.MaxExportW, h.GridExportLimit*10)
		}
		if dayHas {
			s.Days++
		}
	}
	return s
}

type timelineHour struct {
	Hour        int    `json:"hour"`
	Enabled     bool   `json:"enabled"`
	ChargeMode  int    `json:"charge_mode"`
	ModeLabel   string `json:"mode_label"`
	Category    string `json:"category"`
	PowerW      int    `json:"power_w"`
	SOC         int    `json:"soc"`
	ExportW     int    `json:"export_w"`
	GridCharge  bool   `json:"grid_charge"`
	CustomSlots int    `json:"custom_slots"`
	State       string `json:"state"` // configured | pending | executing | completed | failed | past
	RunStatus   string `json:"run_status,omitempty"`
}

type timelineDay struct {
	Day     int            `json:"day"`
	Date    string         `json:"date,omitempty"`
	Weekday string         `json:"weekday,omitempty"`
	Active  bool           `json:"active"`
	Hours   []timelineHour `json:"hours"`
}

func chargeModeCategory(mode int, gridCharge bool) string {
	sell := mode&32 != 0
	grid := mode&1 != 0 || gridCharge
	switch {
	case sell && grid:
		return "sell_grid"
	case sell:
		return "sell"
	case grid:
		return "grid_charge"
	}
	return "self"
}

// apiScheduleTimelineHandler returns a calendar/timeline view of a schedule or
// template. For inverter schedules hours are annotated with execution results
// from task_runs; nothing is read from the inverter.
func (a *App) apiScheduleTimelineHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var raw string
	inverterID, _ := strconv.ParseInt(q.Get("inverter_id"), 10, 64)
	templateID, _ := strconv.ParseInt(q.Get("template_id"), 10, 64)
	title := ""
	switch {
	case inverterID > 0:
		s, err := a.getScheduleByInverterID(inverterID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "У инвертора нет расписания"})
			return
		}
		raw, title = s.ScheduleJSON, s.Name
	case templateID > 0:
		t, err := a.getTemplateByID(templateID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Шаблон не найден"})
			return
		}
		raw, title = t.ScheduleJSON, t.Name
	default:
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Укажите inverter_id или template_id"})
		return
	}
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, jsonResponse{OK: false, Message: "Расписание не разобрано: " + err.Error()})
		return
	}
	normalizeSchedulePayloadSellTimes(&payload)
	loc := time.Local
	if settings, err := a.getSettings(); err == nil {
		if l, err := time.LoadLocation(settings.Timezone); err == nil {
			loc = l
		}
	}
	now := time.Now().In(loc)
	month, err := parseMonth(q.Get("month"), now)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, loc)
	runs := map[string]string{}
	if inverterID > 0 {
		from := month.UTC().Format("2006-01-02 15:04:05")
		to := month.AddDate(0, 1, 0).UTC().Format("2006-01-02 15:04:05")
		rows, err := a.db.Query(`SELECT executed_at_utc, status FROM task_runs WHERE inverter_id = ? AND executed_at_utc >= ? AND executed_at_utc < ? ORDER BY id`, inverterID, from, to)
		if err == nil {
			for rows.Next() {
				var ts, st string
				if rows.Scan(&ts, &st) == nil {
					t, perr := time.ParseInLocation("2006-01-02 15:04:05", strings.Replace(strings.TrimSuffix(ts, "Z"), "T", " ", 1), time.UTC)
					if perr == nil {
						runs[t.In(loc).Format("2006-01-02 15")] = st
					}
				}
			}
			rows.Close()
		}
	}
	daysInMonth := month.AddDate(0, 1, -1).Day()
	days := []timelineDay{}
	for _, d := range payload.Days {
		td := timelineDay{Day: d.Day, Hours: []timelineHour{}}
		var date time.Time
		if inverterID > 0 {
			if d.Day > daysInMonth {
				continue
			}
			date = time.Date(month.Year(), month.Month(), d.Day, 0, 0, 0, 0, loc)
			td.Date, td.Weekday = date.Format("2006-01-02"), weekdayShort[isoWeekday(date)]
		}
		customByHour := map[int]int{}
		for _, cs := range d.CustomSlots {
			customByHour[cs.Hour]++
		}
		for _, h := range d.Hours {
			th := timelineHour{Hour: h.Hour, Enabled: h.Enabled, ChargeMode: h.ChargeMode, ModeLabel: chargeModeLabelFromValue(h.ChargeMode),
				Category: chargeModeCategory(h.ChargeMode, h.GridChargeEnabled), PowerW: h.SellModeKW * 10, SOC: h.SellModeBattCapacity,
				ExportW: h.GridExportLimit * 10, GridCharge: h.GridChargeEnabled, CustomSlots: customByHour[h.Hour], State: "configured"}
			if !h.Enabled {
				th.Category, th.ModeLabel = "off", "не задано"
			}
			if inverterID > 0 {
				hourStart := date.Add(time.Duration(h.Hour) * time.Hour)
				key := hourStart.Format("2006-01-02 15")
				switch {
				case runs[key] != "":
					th.RunStatus = runs[key]
					if strings.Contains(strings.ToLower(runs[key]), "error") || strings.Contains(strings.ToLower(runs[key]), "fail") {
						th.State = "failed"
					} else {
						th.State = "completed"
					}
				case now.After(hourStart) && now.Before(hourStart.Add(time.Hour)):
					th.State = "executing"
				case now.Before(hourStart):
					th.State = "pending"
				default:
					th.State = "past"
				}
			}
			if h.Enabled {
				td.Active = true
			}
			td.Hours = append(td.Hours, th)
		}
		days = append(days, td)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "title": title, "month": month.Format("2006-01"), "use_timer_mask": payload.UseTimerMask,
		"days": days, "summary": summarizeSchedule(raw), "now": now.Format("2006-01-02 15:04"), "is_template": inverterID == 0,
		"schedule_json": raw, "name": title})
}

// apiScheduleCopyHandler copies a schedule to other inverters (mode=inverters)
// or copies one day to other days inside a schedule (mode=days).
func (a *App) apiScheduleCopyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	_ = r.ParseForm()
	sourceID, _ := strconv.ParseInt(r.FormValue("source_inverter_id"), 10, 64)
	src, err := a.getScheduleByInverterID(sourceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "У исходного инвертора нет расписания"})
		return
	}
	switch r.FormValue("mode") {
	case "days":
		fromDay, _ := strconv.Atoi(r.FormValue("from_day"))
		toDays := []int{}
		for _, part := range strings.Split(r.FormValue("to_days"), ",") {
			if d, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && d >= 1 && d <= 31 && d != fromDay {
				toDays = append(toDays, d)
			}
		}
		if fromDay < 1 || fromDay > 31 || len(toDays) == 0 {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Укажите исходный день и дни назначения (1–31)"})
			return
		}
		p, err := parseSchedulePayloadLoose(src.ScheduleJSON)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		p = compactSchedulePayloadForEditor(p)
		normalizeSchedulePayloadSellTimes(&p)
		source := p.Days[fromDay-1]
		for _, d := range toDays {
			copyDay := source
			copyDay.Day = d
			copyDay.Hours = append([]HourConfig(nil), source.Hours...)
			copyDay.CustomSlots = append([]MinuteSlot(nil), source.CustomSlots...)
			copyDay.Slots = nil
			p.Days[d-1] = copyDay
		}
		normalizeSchedulePayloadSellTimes(&p)
		b, _ := json.Marshal(compactSchedulePayloadForEditor(p))
		inv, _ := a.getBasicInvertersByIDs([]int64{sourceID})
		models := []InverterModelDefinition{}
		for _, i := range inv {
			if m, err := findInverterModel(i.ModelKey); err == nil {
				models = append(models, m)
			}
		}
		if err := validateScheduleJSONForModels(string(b), models); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Результат не прошёл проверку: " + err.Error()})
			return
		}
		if err := a.saveScheduleForInverter(sourceID, src.Name, src.ViewMode, string(b)); err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		a.recordHistory(HistoryEntry{OperationType: opJobUpdate, Status: histApplied, InverterID: sourceID, Initiator: requestInitiator(r),
			Message: fmt.Sprintf("День %d скопирован в дни %v", fromDay, toDays)})
		a.recreateActiveSchedulerTask("schedule day copied")
		writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: fmt.Sprintf("День %d скопирован в %d дн.", fromDay, len(toDays))})
	default:
		targets := parseIDsCSV(r.FormValue("target_ids"))
		filtered := []int64{}
		for _, id := range targets {
			if id != sourceID {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Выберите инверторы назначения"})
			return
		}
		invs, err := a.getBasicInvertersByIDs(filtered)
		if err != nil || len(invs) != len(filtered) {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не все инверторы назначения найдены"})
			return
		}
		models := []InverterModelDefinition{}
		for _, i := range invs {
			m, err := findInverterModel(i.ModelKey)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
				return
			}
			models = append(models, m)
		}
		if err := validateScheduleJSONForModels(src.ScheduleJSON, models); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Расписание несовместимо с моделями назначения: " + err.Error()})
			return
		}
		templateID := int64(0)
		if src.AppliedTemplateID.Valid {
			templateID = src.AppliedTemplateID.Int64
		}
		if err := a.saveSchedulesForInverters(filtered, templateID, src.Name, src.ViewMode, src.ScheduleJSON); err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		a.recordHistory(HistoryEntry{OperationType: opScheduleAssign, Status: histApplied, InverterID: sourceID, Initiator: requestInitiator(r),
			JobRef: "inverters:" + joinIDs(filtered), Message: fmt.Sprintf("Расписание «%s» скопировано на инверторы: %d", src.Name, len(filtered))})
		a.recreateActiveSchedulerTask("schedule copied")
		writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: fmt.Sprintf("Расписание скопировано на %d инвертор(а/ов)", len(filtered))})
	}
}
