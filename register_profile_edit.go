package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// orderedObject is a JSON object that keeps its key order, so profile edits
// change only the edited lines of the file.
type orderedObject struct {
	keys []string
	vals map[string]json.RawMessage
}

func (o *orderedObject) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("ожидался JSON-объект")
	}
	o.vals = map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := kt.(string)
		if !ok {
			return fmt.Errorf("некорректный ключ JSON")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if _, exists := o.vals[key]; !exists {
			o.keys = append(o.keys, key)
		}
		o.vals[key] = raw
	}
	_, err = dec.Token()
	return err
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(o.vals[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (o *orderedObject) set(key string, value any) error {
	b, err := marshalNoEscape(value)
	if err != nil {
		return err
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = b
	return nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type rawProfileEntry struct {
	obj    orderedObject
	fields orderedObject
}

func readRawProfile(data []byte) ([]rawProfileEntry, error) {
	var list []orderedObject
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]rawProfileEntry, 0, len(list))
	for i, o := range list {
		var f orderedObject
		raw, ok := o.vals["fields"]
		if !ok {
			return nil, fmt.Errorf("элемент %d не содержит fields", i)
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("элемент %d: %w", i, err)
		}
		out = append(out, rawProfileEntry{obj: o, fields: f})
	}
	return out, nil
}

func encodeRawProfile(entries []rawProfileEntry) ([]byte, error) {
	list := make([]orderedObject, len(entries))
	for i, e := range entries {
		fb, err := e.fields.MarshalJSON()
		if err != nil {
			return nil, err
		}
		e.obj.vals["fields"] = fb
		list[i] = e.obj
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// editableProfileKeys are metadata keys the correction workflow may change.
// Addresses, register types, write modes, write masks and writability are
// deliberately excluded: they define what the application is able to write.
var editableProfileKeys = map[string]bool{
	"name": true, "description": true, "notes": true, "suffix": true, "source": true, "confidence": true,
	"min": true, "max": true, "step": true, "raw_min": true, "raw_max": true,
	"allowed_values": true, "enum_values": true, "bits": true, "value_kind": true,
}

var profileEditMu sync.Mutex

type profilePatchRequest struct {
	ModelKey string                     `json:"model_key"`
	Code     string                     `json:"code"`
	Changes  map[string]json.RawMessage `json:"changes"`
	Note     string                     `json:"note"`
	DryRun   bool                       `json:"dry_run"`
}

func profileBackupDir() string { return filepath.Join("data", "backups", "profiles") }

func backupProfileFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(profileBackupDir(), 0755); err != nil {
		return "", err
	}
	name := filepath.Base(path) + "." + time.Now().Format("20060102-150405.000000000") + ".json"
	dst := filepath.Join(profileBackupDir(), name)
	return dst, os.WriteFile(dst, data, 0644)
}

// applyProfilePatch validates and applies metadata changes to one field. It
// returns the new file content, the before/after JSON of the field and any
// validation messages.
func applyProfilePatch(model InverterModelDefinition, data []byte, code string, changes map[string]json.RawMessage) ([]byte, string, string, []ProfileIssue, error) {
	entries, err := readRawProfile(data)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("профиль не разобран: %w", err)
	}
	idx := -1
	for i, e := range entries {
		var c string
		_ = json.Unmarshal(e.fields.vals["code"], &c)
		if strings.EqualFold(strings.TrimSpace(c), strings.TrimSpace(code)) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, "", "", nil, fmt.Errorf("code=%s не найден в профиле", code)
	}
	before, _ := entries[idx].fields.MarshalJSON()
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !editableProfileKeys[k] {
			return nil, "", "", nil, fmt.Errorf("поле %q нельзя изменять через корректировку метаданных (разрешено: описание, единицы, диапазоны, значения, биты)", k)
		}
		var v any
		if err := json.Unmarshal(changes[k], &v); err != nil {
			return nil, "", "", nil, fmt.Errorf("поле %s: некорректный JSON", k)
		}
		if err := entries[idx].fields.set(k, v); err != nil {
			return nil, "", "", nil, err
		}
	}
	after, _ := entries[idx].fields.MarshalJSON()
	out, err := encodeRawProfile(entries)
	if err != nil {
		return nil, "", "", nil, err
	}
	var params []DeviceParameterFixture
	if err := json.Unmarshal(out, &params); err != nil {
		return nil, "", "", nil, fmt.Errorf("после изменения профиль не соответствует схеме: %w", err)
	}
	enrichProfileBitLayouts(params)
	issues := validateProfileStructure(params)
	if profileIssuesHaveErrors(issues) {
		return nil, "", "", issues, fmt.Errorf("изменение нарушает структуру профиля")
	}
	field := params[idx].Fields
	if strings.EqualFold(field.WriteMode, "masked_bits") && maskBitCount(field) > 1 && isBooleanOnlyAllowed(field.AllowedValues) && len(field.Bits) == 0 {
		return nil, "", "", issues, fmt.Errorf("регистр %d — многобитовая маска; allowed_values=%v превращает её в логическое значение. Опишите биты (bits) вместо списка допустимых значений", field.ModbusAddress, field.AllowedValues)
	}
	if prevErr := validateDeviceParameterProfile(model, mustParseProfile(data)); prevErr == nil {
		if err := validateDeviceParameterProfile(model, params); err != nil {
			return nil, "", "", issues, fmt.Errorf("изменение заблокировало бы запись расписаний для модели: %v", err)
		}
	}
	return out, string(before), string(after), issues, nil
}

func mustParseProfile(data []byte) []DeviceParameterFixture {
	var params []DeviceParameterFixture
	_ = json.Unmarshal(data, &params)
	enrichProfileBitLayouts(params)
	return params
}

func (a *App) apiRegisterProfilePatchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	var req profilePatchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON: " + err.Error()})
		return
	}
	if len(req.Changes) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Нет изменений"})
		return
	}
	model, err := findInverterModel(req.ModelKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	profileEditMu.Lock()
	defer profileEditMu.Unlock()
	data, err := os.ReadFile(model.ParametersFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось прочитать профиль: " + err.Error()})
		return
	}
	out, before, after, issues, err := applyProfilePatch(model, data, req.Code, req.Changes)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "message": err.Error(), "issues": issues})
		return
	}
	if req.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dry_run": true, "message": "Проверка пройдена, изменения не сохранены", "before": json.RawMessage(before), "after": json.RawMessage(after), "issues": issues})
		return
	}
	backup, err := backupProfileFile(model.ParametersFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось создать резервную копию: " + err.Error()})
		return
	}
	if err := writeFileAtomic(model.ParametersFile, out); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось сохранить профиль: " + err.Error()})
		return
	}
	var address int
	var parsed struct {
		ModbusAddress int `json:"modbus_address"`
	}
	if json.Unmarshal([]byte(after), &parsed) == nil {
		address = parsed.ModbusAddress
	}
	a.insertProfileChange(model, req.Code, address, "patch", before, after, backup, requestInitiator(r), req.Note)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Метаданные регистра сохранены; резервная копия: " + backup, "before": json.RawMessage(before), "after": json.RawMessage(after), "issues": issues, "backup": backup})
}

func (a *App) insertProfileChange(model InverterModelDefinition, code string, address int, action, before, after, backup, initiator, note string) {
	_, err := a.db.Exec(`INSERT INTO register_profile_changes (ts_utc, model_key, profile_file, register_code, register_address, action, before_json, after_json, backup_file, initiator, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, time.Now().UTC().Format(time.RFC3339Nano), model.Key, model.ParametersFile, code, address, action, before, after, backup, initiator, note)
	if err != nil {
		a.appendAppLog("error", "profile change insert failed", map[string]any{"error": err.Error()})
	}
	msg := map[string]string{"patch": "Изменены метаданные регистра", "rollback": "Откат профиля регистров", "install": "Установлен профиль регистров"}[action]
	var addr *int
	if address > 0 {
		addr = intPtr(address)
	}
	a.recordHistory(HistoryEntry{OperationType: opProfileChange, Status: histApplied, Initiator: initiator, RegisterAddress: addr, RegisterCode: code,
		Message: msg + " (" + model.Name + ")", Details: map[string]any{"model_key": model.Key, "file": model.ParametersFile, "backup": backup, "note": note}})
}

type profileChangeRow struct {
	ID         int64           `json:"id"`
	TS         string          `json:"ts_utc"`
	ModelKey   string          `json:"model_key"`
	File       string          `json:"profile_file"`
	Code       string          `json:"register_code"`
	Address    int             `json:"register_address"`
	Action     string          `json:"action"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	BackupFile string          `json:"backup_file"`
	Initiator  string          `json:"initiator"`
	Note       string          `json:"note"`
}

func (a *App) apiRegisterProfileChangesHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := `SELECT id, ts_utc, model_key, profile_file, register_code, register_address, action, before_json, after_json, backup_file, initiator, note FROM register_profile_changes WHERE 1=1`
	args := []any{}
	if mk := strings.TrimSpace(q.Get("model_key")); mk != "" {
		query += ` AND model_key = ?`
		args = append(args, normalizeInverterModelKey(mk))
	}
	if code := strings.TrimSpace(q.Get("code")); code != "" {
		query += ` AND register_code = ?`
		args = append(args, code)
	}
	query += ` ORDER BY id DESC LIMIT 200`
	rows, err := a.db.Query(query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	defer rows.Close()
	items := []profileChangeRow{}
	for rows.Next() {
		var c profileChangeRow
		var before, after string
		if err := rows.Scan(&c.ID, &c.TS, &c.ModelKey, &c.File, &c.Code, &c.Address, &c.Action, &before, &after, &c.BackupFile, &c.Initiator, &c.Note); err != nil {
			continue
		}
		if json.Valid([]byte(before)) {
			c.Before = json.RawMessage(before)
		}
		if json.Valid([]byte(after)) {
			c.After = json.RawMessage(after)
		}
		items = append(items, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

func (a *App) apiRegisterProfileRollbackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("change_id"), 10, 64)
	var modelKey, backup, code string
	var address int
	if err := a.db.QueryRow(`SELECT model_key, backup_file, register_code, register_address FROM register_profile_changes WHERE id = ?`, id).Scan(&modelKey, &backup, &code, &address); err != nil {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Изменение не найдено"})
		return
	}
	model, err := findInverterModel(modelKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	cleanBackup := filepath.Clean(backup)
	if !strings.HasPrefix(cleanBackup, filepath.Clean(profileBackupDir())+string(filepath.Separator)) {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Резервная копия вне каталога резервных копий"})
		return
	}
	data, err := os.ReadFile(cleanBackup)
	if err != nil {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Файл резервной копии не найден: " + err.Error()})
		return
	}
	params := mustParseProfile(data)
	if issues := validateProfileStructure(params); profileIssuesHaveErrors(issues) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "message": "Резервная копия структурно некорректна", "issues": issues})
		return
	}
	profileEditMu.Lock()
	defer profileEditMu.Unlock()
	current, err := backupProfileFile(model.ParametersFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось сохранить текущую версию: " + err.Error()})
		return
	}
	if err := writeFileAtomic(model.ParametersFile, data); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	a.insertProfileChange(model, code, address, "rollback", "", "", current, requestInitiator(r), fmt.Sprintf("откат изменения #%d к %s", id, backup))
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Профиль восстановлен из " + backup + "; текущая версия сохранена в " + current})
}

func (a *App) apiRegisterProfileExportHandler(w http.ResponseWriter, r *http.Request) {
	model, err := findInverterModel(r.URL.Query().Get("model_key"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	data, err := os.ReadFile(model.ParametersFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(model.ParametersFile)))
	_, _ = w.Write(data)
}

// apiRegisterProfileInstallHandler replaces a model's profile file after a
// structural check. Unverified profiles are accepted; the stricter write
// validation result is reported and continues to gate writes on its own.
func (a *App) apiRegisterProfileInstallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ожидается файл профиля (multipart/form-data)"})
		return
	}
	model, err := findInverterModel(r.FormValue("model_key"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Файл не передан"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	var params []DeviceParameterFixture
	if err := json.Unmarshal(data, &params); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, jsonResponse{OK: false, Message: "Файл не является профилем регистров: " + err.Error()})
		return
	}
	enrichProfileBitLayouts(params)
	issues := validateProfileStructure(params)
	if profileIssuesHaveErrors(issues) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "message": "Профиль не прошёл структурную проверку", "issues": issues})
		return
	}
	writeCheck := "пройдена — запись расписаний разрешена"
	writeAllowed := true
	if err := validateDeviceParameterProfile(model, params); err != nil {
		writeCheck = "не пройдена — запись для этой модели будет заблокирована: " + err.Error()
		writeAllowed = false
	}
	if r.FormValue("dry_run") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dry_run": true, "message": "Структурная проверка пройдена", "issues": issues, "write_check": writeCheck, "write_allowed": writeAllowed, "parameters": len(params)})
		return
	}
	profileEditMu.Lock()
	defer profileEditMu.Unlock()
	backup, err := backupProfileFile(model.ParametersFile)
	if err != nil && !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось создать резервную копию: " + err.Error()})
		return
	}
	if err := writeFileAtomic(model.ParametersFile, data); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	a.insertProfileChange(model, "", 0, "install", "", "", backup, requestInitiator(r), r.FormValue("note"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Профиль установлен", "issues": issues, "write_check": writeCheck, "write_allowed": writeAllowed, "backup": backup, "parameters": len(params)})
}

type profileStatus struct {
	ModelKey             string         `json:"model_key"`
	ModelName            string         `json:"model_name"`
	File                 string         `json:"file"`
	ValidationStatus     string         `json:"validation_status"`
	Experimental         bool           `json:"experimental"`
	WriteRequiresConfirm bool           `json:"write_requires_confirmation"`
	Parameters           int            `json:"parameters"`
	StructureOK          bool           `json:"structure_ok"`
	WriteCheckOK         bool           `json:"write_check_ok"`
	WriteCheck           string         `json:"write_check"`
	Customized           bool           `json:"customized"`
	Confidence           map[string]int `json:"confidence"`
	ConfidenceLevel      string         `json:"confidence_level"`
	Issues               []ProfileIssue `json:"issues"`
}

func (a *App) profileStatuses() []profileStatus {
	out := []profileStatus{}
	models, err := availableInverterModels()
	if err != nil {
		return out
	}
	customized := map[string]bool{}
	for _, p := range a.defaultsReport.Customized() {
		customized[filepath.Clean(filepath.FromSlash(p))] = true
	}
	for _, m := range models {
		st := profileStatus{ModelKey: m.Key, ModelName: m.Name, File: m.ParametersFile, ValidationStatus: m.ValidationStatus, Experimental: m.Experimental,
			WriteRequiresConfirm: m.WriteRequiresConfirm, Confidence: map[string]int{}, Customized: customized[filepath.Clean(m.ParametersFile)]}
		params, err := LoadDeviceParameters(m.ParametersFile)
		if err != nil {
			st.WriteCheck = "профиль не прочитан: " + err.Error()
			st.Issues = []ProfileIssue{{Severity: "error", Message: err.Error()}}
			out = append(out, st)
			continue
		}
		st.Parameters = len(params)
		st.Issues = validateProfileStructure(params)
		st.StructureOK = !profileIssuesHaveErrors(st.Issues)
		if err := validateDeviceParameterProfile(m, params); err != nil {
			st.WriteCheck = err.Error()
		} else {
			st.WriteCheckOK = true
			st.WriteCheck = "пройдена"
		}
		for _, p := range params {
			st.Confidence[confidenceLevel(p.Fields.Confidence)]++
		}
		switch {
		case st.Confidence["verified"] > 0 && st.Confidence["verified"] >= len(params)/2:
			st.ConfidenceLevel = "verified"
		case st.Confidence["documented"] >= len(params)/2:
			st.ConfidenceLevel = "documented"
		default:
			st.ConfidenceLevel = "unverified"
		}
		out = append(out, st)
	}
	return out
}

func (a *App) apiRegisterProfilesStatusHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": a.profileStatuses()})
}
