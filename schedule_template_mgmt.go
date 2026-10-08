package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type SimpleTemplateVersion struct {
	Version     int          `json:"version"`
	Timestamp   string       `json:"ts_utc"`
	Author      string       `json:"author,omitempty"`
	Summary     string       `json:"summary,omitempty"`
	Name        string       `json:"name,omitempty"`
	Description string       `json:"description,omitempty"`
	Rules       []SimpleRule `json:"rules,omitempty"`
	Tags        []string     `json:"tags,omitempty"`
	ModelKeys   []string     `json:"model_keys,omitempty"`
}

func (a *App) listSimpleTemplateVersions(templateID int64) ([]SimpleTemplateVersion, error) {
	rows, err := a.db.Query(`SELECT version, ts_utc, author, summary, name, description, rules_json, tags_json, model_keys_json
		FROM simple_template_versions WHERE template_id = ? ORDER BY version DESC`, templateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SimpleTemplateVersion{}
	for rows.Next() {
		var v SimpleTemplateVersion
		var rulesRaw, tagsRaw, modelsRaw string
		if err := rows.Scan(&v.Version, &v.Timestamp, &v.Author, &v.Summary, &v.Name, &v.Description, &rulesRaw, &tagsRaw, &modelsRaw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(rulesRaw), &v.Rules)
		_ = json.Unmarshal([]byte(tagsRaw), &v.Tags)
		_ = json.Unmarshal([]byte(modelsRaw), &v.ModelKeys)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (a *App) snapshotSimpleTemplateVersion(t SimpleTemplate, author, summary string) error {
	rb, _ := json.Marshal(t.Rules)
	tb, _ := json.Marshal(t.Tags)
	mb, _ := json.Marshal(t.ModelKeys)
	_, err := a.db.Exec(`INSERT OR REPLACE INTO simple_template_versions
		(template_id, version, ts_utc, author, summary, name, description, rules_json, tags_json, model_keys_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Version, time.Now().UTC().Format(time.RFC3339), author, summary, t.Name, t.Description, string(rb), string(tb), string(mb))
	return err
}

func decodeStringSlice(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func encodeStringSlice(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// enrich listSimpleTemplates scan — replaced by extended query in this file helpers.

func (a *App) getSimpleTemplate(id int64) (SimpleTemplate, error) {
	var t SimpleTemplate
	var rulesRaw, tagsRaw, modelsRaw string
	var lastInv sqlNullInt64
	err := a.db.QueryRow(`SELECT id, name, description, rules_json, created_at, updated_at,
		COALESCE(version,1), COALESCE(tags_json,'[]'), COALESCE(model_keys_json,'[]'),
		last_applied_inverter_id, COALESCE(last_applied_utc,''), COALESCE(last_applied_status,'')
		FROM simple_templates WHERE id = ?`, id).Scan(
		&t.ID, &t.Name, &t.Description, &rulesRaw, &t.CreatedAt, &t.UpdatedAt,
		&t.Version, &tagsRaw, &modelsRaw, &lastInv, &t.LastAppliedUTC, &t.LastAppliedStatus)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal([]byte(rulesRaw), &t.Rules)
	t.Tags = decodeStringSlice(tagsRaw)
	t.ModelKeys = decodeStringSlice(modelsRaw)
	if lastInv.Valid {
		t.LastAppliedInverterID = lastInv.Int64
	}
	t.IntervalCount = len(t.Rules)
	for _, r := range t.Rules {
		t.Summary = append(t.Summary, describeSimpleRule(r))
	}
	_, _, daySlots := analyzeSimpleIntervals(t.Rules, false)
	t.LogicalDaySlots = daySlots
	t.HardwareSlots = hardwareTOUSlots
	t.ExceedsHardware = daySlots > hardwareTOUSlots
	return t, nil
}

// sqlNullInt64 avoids importing database/sql in call sites that already have it.
type sqlNullInt64 struct {
	Int64 int64
	Valid bool
}

func (n *sqlNullInt64) Scan(value any) error {
	if value == nil {
		n.Int64, n.Valid = 0, false
		return nil
	}
	switch v := value.(type) {
	case int64:
		n.Int64, n.Valid = v, true
	case int:
		n.Int64, n.Valid = int64(v), true
	default:
		n.Valid = false
	}
	return nil
}

func (a *App) upsertSimpleTemplateVersioned(t SimpleTemplate, author, summary string) (int64, error) {
	if err := validateSimpleTemplate(t); err != nil {
		return 0, err
	}
	rb := mustJSON(t.Rules)
	tb := encodeStringSlice(t.Tags)
	mb := encodeStringSlice(t.ModelKeys)
	now := time.Now().UTC().Format(time.RFC3339)
	name := strings.TrimSpace(t.Name)
	if t.ID > 0 {
		prev, err := a.getSimpleTemplate(t.ID)
		if err != nil {
			return 0, fmt.Errorf("шаблон не найден")
		}
		// Preserve a snapshot of the previous version before overwriting.
		_ = a.snapshotSimpleTemplateVersion(prev, author, "Автоснимок перед изменением")
		newVer := prev.Version + 1
		if summary == "" {
			summary = fmt.Sprintf("Версия %d", newVer)
		}
		res, err := a.db.Exec(`UPDATE simple_templates SET name=?, description=?, rules_json=?, updated_at=?, version=?, tags_json=?, model_keys_json=? WHERE id=?`,
			name, t.Description, rb, now, newVer, tb, mb, t.ID)
		if err != nil {
			return 0, friendlyUniqueError(err, name)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, fmt.Errorf("шаблон не найден")
		}
		t.Version = newVer
		t.ID = prev.ID
		_ = a.snapshotSimpleTemplateVersion(t, author, summary)
		return t.ID, nil
	}
	res, err := a.db.Exec(`INSERT INTO simple_templates (name, description, rules_json, created_at, updated_at, version, tags_json, model_keys_json)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, name, t.Description, rb, now, now, tb, mb)
	if err != nil {
		return 0, friendlyUniqueError(err, name)
	}
	id, _ := res.LastInsertId()
	t.ID, t.Version = id, 1
	_ = a.snapshotSimpleTemplateVersion(t, author, "Создание шаблона")
	return id, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *App) restoreSimpleTemplateVersion(templateID int64, version int, author string) error {
	var v SimpleTemplateVersion
	var rulesRaw, tagsRaw, modelsRaw string
	err := a.db.QueryRow(`SELECT version, ts_utc, author, summary, name, description, rules_json, tags_json, model_keys_json
		FROM simple_template_versions WHERE template_id = ? AND version = ?`, templateID, version).
		Scan(&v.Version, &v.Timestamp, &v.Author, &v.Summary, &v.Name, &v.Description, &rulesRaw, &tagsRaw, &modelsRaw)
	if err != nil {
		return fmt.Errorf("версия не найдена")
	}
	_ = json.Unmarshal([]byte(rulesRaw), &v.Rules)
	t := SimpleTemplate{
		ID: templateID, Name: v.Name, Description: v.Description, Rules: v.Rules,
		Tags: decodeStringSlice(tagsRaw), ModelKeys: decodeStringSlice(modelsRaw),
	}
	_, err = a.upsertSimpleTemplateVersioned(t, author, fmt.Sprintf("Восстановление версии %d", version))
	return err
}

type templateCompatibility struct {
	Status   string   `json:"status"` // compatible | adjustments | incompatible | unverified
	Messages []string `json:"messages"`
	ModelKey string   `json:"model_key,omitempty"`
	Slots    int      `json:"logical_day_slots"`
	Hardware int      `json:"hardware_slots"`
}

func (a *App) assessTemplateCompatibility(t SimpleTemplate, modelKey string) templateCompatibility {
	out := templateCompatibility{Status: "compatible", Hardware: hardwareTOUSlots, ModelKey: modelKey}
	_, _, slots := analyzeSimpleIntervals(t.Rules, false)
	out.Slots = slots
	if slots > hardwareTOUSlots {
		out.Status = "adjustments"
		out.Messages = append(out.Messages, fmt.Sprintf("логических интервалов на день: %d, аппаратных слотов TOU: %d — прямая запись всех слотов невозможна; программный планировщик применит почасово", slots, hardwareTOUSlots))
	}
	if len(t.ModelKeys) > 0 && modelKey != "" {
		ok := false
		for _, k := range t.ModelKeys {
			if k == modelKey || k == "*" {
				ok = true
				break
			}
		}
		if !ok {
			out.Status = "incompatible"
			out.Messages = append(out.Messages, "модель инвертора не входит в список совместимости шаблона")
		}
	} else if modelKey == "" {
		out.Status = "unverified"
		out.Messages = append(out.Messages, "модель инвертора не указана — совместимость не подтверждена")
	}
	if modelKey != "" {
		if m, err := findInverterModel(modelKey); err == nil {
			if err := validateScheduleJSONForModels(mustJSON(defaultSchedulePayload()), []InverterModelDefinition{m}); err != nil {
				// Model loads; validate rules via build
			}
			res, _, err := buildSimpleSchedule(t.Rules, time.Now(), nil, 255)
			if err != nil {
				out.Status = "incompatible"
				out.Messages = append(out.Messages, err.Error())
			} else if len(res.ValidationErrs) > 0 {
				out.Status = "incompatible"
				out.Messages = append(out.Messages, res.ValidationErrs...)
			} else if err := validateScheduleJSONForModels(res.ScheduleJSON, []InverterModelDefinition{m}); err != nil {
				out.Status = "adjustments"
				out.Messages = append(out.Messages, err.Error())
			}
		} else {
			out.Status = "unverified"
			out.Messages = append(out.Messages, "профиль модели не найден: "+modelKey)
		}
	}
	return out
}

func (a *App) apiSimpleTemplateVersionsHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Укажите id шаблона"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := a.listSimpleTemplateVersions(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "hardware_slots": hardwareTOUSlots})
	case http.MethodPost:
		var req struct {
			Action  string `json:"action"`
			Version int    `json:"version"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
			return
		}
		if req.Action != "restore" {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Поддерживается action=restore"})
			return
		}
		if err := a.restoreSimpleTemplateVersion(id, req.Version, requestInitiator(r)); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		a.recordHistory(HistoryEntry{OperationType: opJobUpdate, Status: histApplied, Initiator: requestInitiator(r),
			JobRef: fmt.Sprintf("simple_template:%d", id), Message: fmt.Sprintf("Восстановлена версия %d шаблона", req.Version)})
		writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Версия восстановлена как новая редакция"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
	}
}

func (a *App) apiSimpleTemplatePreviewApplyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	var req struct {
		TemplateID  int64   `json:"template_id"`
		InverterIDs []int64 `json:"inverter_ids"`
		Month       string  `json:"month"`
		BaseMode    string  `json:"base_mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
		return
	}
	t, err := a.getSimpleTemplate(req.TemplateID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Шаблон не найден"})
		return
	}
	if len(req.InverterIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Выберите инверторы"})
		return
	}
	baseMode := req.BaseMode
	if baseMode == "" {
		baseMode = "replace"
	}
	devices := []map[string]any{}
	for _, id := range req.InverterIDs {
		invs, err := a.getBasicInvertersByIDs([]int64{id})
		if err != nil || len(invs) == 0 {
			devices = append(devices, map[string]any{"inverter_id": id, "ok": false, "error": "инвертор не найден"})
			continue
		}
		inv := invs[0]
		compat := a.assessTemplateCompatibility(t, inv.ModelKey)
		sreq := SimpleScheduleRequest{InverterIDs: []int64{id}, Month: req.Month, Rules: t.Rules, BaseMode: baseMode, Name: t.Name}
		res, err := a.resolveSimpleSchedule(sreq, id, time.Now())
		item := map[string]any{
			"inverter_id": id, "name": inv.Name, "model_key": inv.ModelKey,
			"compatibility": compat, "ok": err == nil && len(res.ValidationErrs) == 0 && compat.Status != "incompatible",
		}
		if err != nil {
			item["error"] = err.Error()
		} else {
			item["result"] = res
			item["exceeds_hardware"] = res.ExceedsHardware
		}
		// Current schedule snapshot summary for comparison.
		if s, err := a.getScheduleByInverterID(id); err == nil {
			item["current"] = map[string]any{"name": s.Name, "summary": summarizeSchedule(s.ScheduleJSON), "analysis": analyzeScheduleForSimpleMode(s.ScheduleJSON)}
		}
		devices = append(devices, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "template": t, "devices": devices,
		"note": "Это только предпросмотр. «Сохранить шаблон» не пишет в инвертор. «Применить к инвертору» сохраняет расписание в приложении; запись регистров выполняет планировщик после подтверждения.",
	})
}

func (a *App) scheduleTemplatesPageHandler(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":         "Шаблоны расписаний",
		"HardwareSlots": hardwareTOUSlots,
		"SimpleEnabled": a.kvBool(kvFeatureSimpleScheduler),
	}
	rows, err := a.db.Query(`SELECT id, COALESCE(NULLIF(TRIM(name), ''), 'Инвертор ' || id), ip, port, model_key FROM inverters ORDER BY id`)
	inverters := []registerTestInverter{}
	if err == nil {
		for rows.Next() {
			var i registerTestInverter
			var ip string
			var port int
			if rows.Scan(&i.ID, &i.Name, &ip, &port, &i.ModelKey) == nil {
				i.Endpoint = fmt.Sprintf("%s:%d", ip, port)
				i.ModelKey = normalizeInverterModelKey(i.ModelKey)
				inverters = append(inverters, i)
			}
		}
		rows.Close()
	}
	data["Inverters"] = inverters
	models := []map[string]any{}
	if defs, err := availableInverterModels(); err == nil {
		for _, m := range defs {
			models = append(models, map[string]any{"key": m.Key, "name": m.Name})
		}
	}
	b, _ := json.Marshal(models)
	data["ModelsJSON"] = string(b)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = a.tmplScheduleTemplates.Execute(w, data)
}
