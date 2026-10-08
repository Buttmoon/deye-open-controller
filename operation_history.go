package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Operation types recorded in operation_history.
const (
	opJobCreate         = "job_create"
	opJobUpdate         = "job_update"
	opJobDelete         = "job_delete"
	opScheduleAssign    = "schedule_assign"
	opScheduleExecution = "schedule_execution"
	opRegisterWrite     = "register_write"
	opRegisterReadback  = "register_readback"
	opRegisterRead      = "register_read"
	opConfigChange      = "config_change"
	opManualOperation   = "manual_operation"
	opProfileChange     = "profile_change"
	opRetryAttempt      = "retry_attempt"
)

// Execution statuses. "verified" is only ever used after a successful
// read-back comparison; a write without read-back is at most "applied".
const (
	histScheduled  = "scheduled"
	histRunning    = "running"
	histApplied    = "applied"
	histVerified   = "verified"
	histError      = "error"
	histPartial    = "partial"
	histUnverified = "unverified"
	histSkipped    = "skipped"
	histBlocked    = "blocked"
	histDryRun     = "dry_run"
	histInfo       = "info"
)

var historyTypeLabels = map[string]string{
	opJobCreate:         "Создание задания",
	opJobUpdate:         "Изменение задания",
	opJobDelete:         "Удаление задания",
	opScheduleAssign:    "Назначение расписания",
	opScheduleExecution: "Выполнение расписания",
	opRegisterWrite:     "Запись регистра",
	opRegisterReadback:  "Проверка чтением",
	opRegisterRead:      "Чтение регистра",
	opConfigChange:      "Изменение конфигурации",
	opManualOperation:   "Ручная операция",
	opProfileChange:     "Изменение профиля регистров",
	opRetryAttempt:      "Повторная попытка",
}

var historyStatusLabels = map[string]string{
	histScheduled:  "Запланировано",
	histRunning:    "Выполняется",
	histApplied:    "Применено",
	histVerified:   "Подтверждено чтением",
	histError:      "Ошибка",
	histPartial:    "Частично выполнено",
	histUnverified: "Не подтверждено",
	histSkipped:    "Пропущено",
	histBlocked:    "Заблокировано",
	histDryRun:     "Пробный расчёт",
	histInfo:       "Информация",
}

type HistoryEntry struct {
	ID              int64          `json:"id"`
	TimestampUTC    time.Time      `json:"ts_utc"`
	OperationID     string         `json:"operation_id"`
	ParentID        string         `json:"parent_id,omitempty"`
	OperationType   string         `json:"operation_type"`
	OperationLabel  string         `json:"operation_label"`
	Status          string         `json:"status"`
	StatusLabel     string         `json:"status_label"`
	InverterID      int64          `json:"inverter_id,omitempty"`
	InverterName    string         `json:"inverter_name,omitempty"`
	Endpoint        string         `json:"endpoint,omitempty"`
	Initiator       string         `json:"initiator,omitempty"`
	JobRef          string         `json:"job_ref,omitempty"`
	RegisterAddress *int           `json:"register_address,omitempty"`
	RegisterCode    string         `json:"register_code,omitempty"`
	PreviousValue   *int           `json:"previous_value,omitempty"`
	RequestedValue  *int           `json:"requested_value,omitempty"`
	WrittenValue    *int           `json:"written_value,omitempty"`
	VerifiedValue   *int           `json:"verified_value,omitempty"`
	Attempt         int            `json:"attempt,omitempty"`
	DurationMS      int64          `json:"duration_ms"`
	Message         string         `json:"message,omitempty"`
	Error           string         `json:"error,omitempty"`
	Details         map[string]any `json:"details,omitempty"`
	ChildCount      int            `json:"child_count,omitempty"`
}

func intPtr(v int) *int { return &v }

func newOperationID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// requestInitiator describes who triggered an HTTP request. The application has
// no user accounts, so the best available identity is the client address.
func requestInitiator(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if fwd := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); fwd != "" {
		host = fwd + " (через прокси " + host + ")"
	}
	kind := "оператор"
	if strings.HasPrefix(r.URL.Path, "/api/") {
		kind = "API"
	}
	return kind + " " + host
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// recordHistory persists one history row. Failures never interrupt the
// operation being recorded; they are reported to the application log instead.
func (a *App) recordHistory(e HistoryEntry) string {
	if a == nil || a.db == nil {
		return e.OperationID
	}
	if e.TimestampUTC.IsZero() {
		e.TimestampUTC = time.Now().UTC()
	}
	if e.OperationID == "" {
		e.OperationID = newOperationID()
	}
	if e.Status == "" {
		e.Status = histInfo
	}
	details := ""
	if len(e.Details) > 0 {
		if b, err := json.Marshal(e.Details); err == nil {
			details = string(b)
		}
	}
	if e.InverterID > 0 && (e.InverterName == "" || e.Endpoint == "") {
		var name, ip string
		var port int
		if err := a.db.QueryRow(`SELECT name, ip, port FROM inverters WHERE id = ?`, e.InverterID).Scan(&name, &ip, &port); err == nil {
			if e.InverterName == "" {
				e.InverterName = name
			}
			if e.Endpoint == "" {
				e.Endpoint = fmt.Sprintf("%s:%d", ip, port)
			}
		}
	}
	_, err := a.db.Exec(`INSERT INTO operation_history
		(ts_utc, operation_id, parent_id, operation_type, status, inverter_id, inverter_name, endpoint, initiator, job_ref,
		 register_address, register_code, previous_value, requested_value, written_value, verified_value,
		 attempt, duration_ms, message, error, details_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TimestampUTC.UTC().Format(time.RFC3339Nano), e.OperationID, e.ParentID, e.OperationType, e.Status,
		nullableInt64(e.InverterID), e.InverterName, e.Endpoint, e.Initiator, e.JobRef,
		nullableInt(e.RegisterAddress), e.RegisterCode, nullableInt(e.PreviousValue), nullableInt(e.RequestedValue),
		nullableInt(e.WrittenValue), nullableInt(e.VerifiedValue), e.Attempt, e.DurationMS, e.Message, e.Error, details)
	if err != nil {
		a.appendAppLog("error", "operation history insert failed", map[string]any{"component": "history", "error": err.Error(), "operation_type": e.OperationType})
	}
	return e.OperationID
}

type HistoryFilter struct {
	From          time.Time
	To            time.Time
	InverterID    int64
	OperationType string
	Status        string
	Register      *int
	Search        string
	OperationID   string
	TopLevelOnly  bool
	Page          int
	PerPage       int
}

func parseHistoryFilter(r *http.Request) HistoryFilter {
	q := r.URL.Query()
	f := HistoryFilter{Page: parsePositiveInt(q.Get("page"), 1), PerPage: parsePositiveInt(q.Get("per_page"), 50)}
	if f.PerPage > 500 {
		f.PerPage = 500
	}
	loc := time.Local
	if tz := strings.TrimSpace(q.Get("tz")); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	parse := func(raw string) time.Time {
		raw = strings.TrimSpace(raw)
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
				return t
			}
		}
		return time.Time{}
	}
	f.From = parse(q.Get("from"))
	f.To = parse(q.Get("to"))
	if !f.To.IsZero() && len(strings.TrimSpace(q.Get("to"))) == len("2006-01-02") {
		f.To = f.To.Add(24*time.Hour - time.Nanosecond)
	}
	f.InverterID, _ = strconv.ParseInt(strings.TrimSpace(q.Get("inverter_id")), 10, 64)
	f.OperationType = strings.TrimSpace(q.Get("type"))
	f.Status = strings.TrimSpace(q.Get("status"))
	if reg := strings.TrimSpace(q.Get("register")); reg != "" {
		if n, err := parseRegisterNumber(reg); err == nil {
			f.Register = intPtr(int(n))
		}
	}
	f.Search = strings.TrimSpace(q.Get("q"))
	f.OperationID = strings.TrimSpace(q.Get("operation_id"))
	f.TopLevelOnly = q.Get("top") == "1"
	return f
}

func (f HistoryFilter) where() (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	if !f.From.IsZero() {
		clauses = append(clauses, "ts_utc >= ?")
		args = append(args, f.From.UTC().Format(time.RFC3339Nano))
	}
	if !f.To.IsZero() {
		clauses = append(clauses, "ts_utc <= ?")
		args = append(args, f.To.UTC().Format(time.RFC3339Nano))
	}
	if f.InverterID > 0 {
		clauses = append(clauses, "inverter_id = ?")
		args = append(args, f.InverterID)
	}
	if f.OperationType != "" {
		clauses = append(clauses, "operation_type = ?")
		args = append(args, f.OperationType)
	}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, f.Status)
	}
	if f.Register != nil {
		clauses = append(clauses, "register_address = ?")
		args = append(args, *f.Register)
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		clauses = append(clauses, "(message LIKE ? OR error LIKE ? OR register_code LIKE ? OR inverter_name LIKE ? OR job_ref LIKE ? OR operation_id LIKE ?)")
		args = append(args, like, like, like, like, like, like)
	}
	if f.OperationID != "" {
		clauses = append(clauses, "(operation_id = ? OR parent_id = ?)")
		args = append(args, f.OperationID, f.OperationID)
	}
	if f.TopLevelOnly {
		clauses = append(clauses, "parent_id = ''")
	}
	return strings.Join(clauses, " AND "), args
}

const historySelectColumns = `id, ts_utc, operation_id, parent_id, operation_type, status, COALESCE(inverter_id, 0), inverter_name, endpoint,
	initiator, job_ref, register_address, register_code, previous_value, requested_value, written_value, verified_value,
	attempt, duration_ms, message, error, details_json`

func scanHistoryRow(rows interface{ Scan(dest ...any) error }) (HistoryEntry, error) {
	var e HistoryEntry
	var ts, details string
	var reg, prev, req, written, verified sql.NullInt64
	if err := rows.Scan(&e.ID, &ts, &e.OperationID, &e.ParentID, &e.OperationType, &e.Status, &e.InverterID, &e.InverterName, &e.Endpoint,
		&e.Initiator, &e.JobRef, &reg, &e.RegisterCode, &prev, &req, &written, &verified, &e.Attempt, &e.DurationMS, &e.Message, &e.Error, &details); err != nil {
		return e, err
	}
	e.TimestampUTC, _ = time.Parse(time.RFC3339Nano, ts)
	toPtr := func(v sql.NullInt64) *int {
		if !v.Valid {
			return nil
		}
		return intPtr(int(v.Int64))
	}
	e.RegisterAddress, e.PreviousValue, e.RequestedValue, e.WrittenValue, e.VerifiedValue = toPtr(reg), toPtr(prev), toPtr(req), toPtr(written), toPtr(verified)
	if details != "" {
		_ = json.Unmarshal([]byte(details), &e.Details)
	}
	e.OperationLabel = historyTypeLabels[e.OperationType]
	if e.OperationLabel == "" {
		e.OperationLabel = e.OperationType
	}
	e.StatusLabel = historyStatusLabels[e.Status]
	if e.StatusLabel == "" {
		e.StatusLabel = e.Status
	}
	return e, nil
}

func (a *App) queryHistory(f HistoryFilter) ([]HistoryEntry, int, error) {
	where, args := f.where()
	var total int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM operation_history WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PerPage < 1 {
		f.PerPage = 50
	}
	query := `SELECT ` + historySelectColumns + ` FROM operation_history WHERE ` + where + ` ORDER BY ts_utc DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := a.db.Query(query, append(args, f.PerPage, (f.Page-1)*f.PerPage)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []HistoryEntry{}
	ids := []string{}
	for rows.Next() {
		e, err := scanHistoryRow(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, e)
		if e.ParentID == "" {
			ids = append(ids, e.OperationID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if f.TopLevelOnly && len(ids) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		cargs := make([]any, len(ids))
		for i, id := range ids {
			cargs[i] = id
		}
		crow, err := a.db.Query(`SELECT parent_id, COUNT(*) FROM operation_history WHERE parent_id IN (`+placeholders+`) GROUP BY parent_id`, cargs...)
		if err == nil {
			counts := map[string]int{}
			for crow.Next() {
				var id string
				var n int
				if crow.Scan(&id, &n) == nil {
					counts[id] = n
				}
			}
			crow.Close()
			for i := range items {
				items[i].ChildCount = counts[items[i].OperationID]
			}
		}
	}
	return items, total, nil
}

// trimHistory applies the retention policy: age limit, then row limit.
func (a *App) trimHistory() (int64, error) {
	days := a.kvInt(kvHistoryRetentionDays)
	maxRows := a.kvInt(kvHistoryMaxRows)
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano)
	res, err := a.db.Exec(`DELETE FROM operation_history WHERE ts_utc < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()
	res, err = a.db.Exec(`DELETE FROM operation_history WHERE id IN (
		SELECT id FROM operation_history ORDER BY ts_utc DESC, id DESC LIMIT -1 OFFSET ?)`, maxRows)
	if err != nil {
		return deleted, err
	}
	more, _ := res.RowsAffected()
	return deleted + more, nil
}

func (a *App) apiHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	f := parseHistoryFilter(r)
	items, total, err := a.queryHistory(f)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения истории: " + err.Error()})
		return
	}
	pages := (total + f.PerPage - 1) / f.PerPage
	if pages < 1 {
		pages = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "items": items, "total": total, "page": f.Page, "per_page": f.PerPage, "pages": pages,
		"types": historyTypeLabels, "statuses": historyStatusLabels,
	})
}

func historyExportRow(e HistoryEntry, loc *time.Location) []any {
	opt := func(v *int) any {
		if v == nil {
			return ""
		}
		return *v
	}
	details := ""
	if len(e.Details) > 0 {
		b, _ := json.Marshal(e.Details)
		details = string(b)
	}
	return []any{
		e.TimestampUTC.In(loc).Format("2006-01-02 15:04:05.000"), e.OperationID, e.ParentID, e.OperationLabel, e.StatusLabel,
		e.InverterName, e.Endpoint, e.Initiator, e.JobRef, opt(e.RegisterAddress), e.RegisterCode,
		opt(e.PreviousValue), opt(e.RequestedValue), opt(e.WrittenValue), opt(e.VerifiedValue), e.Attempt, e.DurationMS,
		e.Message, e.Error, details,
	}
}

var historyExportHeaders = []string{
	"Время", "ID операции", "Родительская операция", "Тип операции", "Статус", "Инвертор", "Адрес устройства", "Инициатор",
	"Задание / расписание", "Регистр", "Код регистра", "Предыдущее значение", "Запрошено", "Записано", "Прочитано после записи",
	"Попытка", "Длительность, мс", "Сообщение", "Ошибка", "Подробности (JSON)",
}

func (a *App) apiHistoryExportHandler(w http.ResponseWriter, r *http.Request) {
	f := parseHistoryFilter(r)
	f.Page, f.PerPage = 1, 100000
	items, _, err := a.queryHistory(f)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения истории: " + err.Error()})
		return
	}
	loc := time.Local
	if settings, err := a.getSettings(); err == nil {
		if l, err := time.LoadLocation(settings.Timezone); err == nil {
			loc = l
		}
	}
	stamp := time.Now().Format("20060102-150405")
	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "operation-history-"+stamp+".csv"))
		_, _ = w.Write([]byte("\xEF\xBB\xBF"))
		cw := csv.NewWriter(w)
		cw.Comma = ';'
		_ = cw.Write(historyExportHeaders)
		for _, e := range items {
			row := historyExportRow(e, loc)
			rec := make([]string, len(row))
			for i, v := range row {
				rec[i] = fmt.Sprint(v)
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
		return
	}
	rows := make([][]any, 0, len(items))
	for _, e := range items {
		rows = append(rows, historyExportRow(e, loc))
	}
	data, err := buildGenericXLSX("History", historyExportHeaders, rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка генерации XLSX"})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "operation-history-"+stamp+".xlsx"))
	_, _ = w.Write(data)
}

type HistoryPageData struct {
	Title     string
	Inverters []historyInverterOption
	Types     map[string]string
	Statuses  map[string]string
	Timezone  string
}

type historyInverterOption struct {
	ID   int64
	Name string
}

func (a *App) inverterOptions() []historyInverterOption {
	rows, err := a.db.Query(`SELECT id, COALESCE(NULLIF(TRIM(name), ''), 'Инвертор ' || id) FROM inverters ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []historyInverterOption{}
	for rows.Next() {
		var o historyInverterOption
		if rows.Scan(&o.ID, &o.Name) == nil {
			out = append(out, o)
		}
	}
	return out
}

func (a *App) historyPageHandler(w http.ResponseWriter, r *http.Request) {
	settings, _ := a.getSettings()
	data := HistoryPageData{Title: "История заданий и изменений", Inverters: a.inverterOptions(), Types: historyTypeLabels, Statuses: historyStatusLabels, Timezone: settings.Timezone}
	if err := a.tmplHistory.Execute(w, data); err != nil {
		a.appendAppLog("error", "history template error", map[string]any{"error": err.Error()})
	}
}

// historyRouteRule maps a mutating route to the history record it produces when
// the handler did not record a more detailed entry itself.
type historyRouteRule struct {
	Type    string
	Message string
}

var historyRouteRules = map[string]historyRouteRule{
	"/inverters":                     {opConfigChange, "Добавлен инвертор"},
	"/inverters/delete":              {opConfigChange, "Удалён инвертор"},
	"/inverters/settings":            {opConfigChange, "Изменены настройки инвертора"},
	"/settings/timezone":             {opConfigChange, "Изменён часовой пояс"},
	"/settings/scheduler":            {opConfigChange, "Переключён планировщик"},
	"/settings/file-logging":         {opConfigChange, "Переключено файловое логирование"},
	"/settings/runtime":              {opConfigChange, "Изменены настройки планировщика и логов"},
	"/settings/inverter-logging":     {opConfigChange, "Изменены настройки логирования инвертора"},
	"/settings/logs/clear":           {opManualOperation, "Очищены логи"},
	"/settings/db/clear":             {opManualOperation, "Очищена база данных"},
	"/settings/schedules/clear":      {opJobDelete, "Удалены все расписания"},
	"/schedules/toggle":              {opJobUpdate, "Переключён Use Timer расписаний"},
	"/schedules/save":                {opJobUpdate, "Сохранено расписание"},
	"/templates":                     {opJobCreate, "Создан шаблон"},
	"/templates/save":                {opJobUpdate, "Сохранён шаблон"},
	"/templates/delete":              {opJobDelete, "Удалён шаблон"},
	"/templates/apply-to-inverters":  {opScheduleAssign, "Шаблон применён к инверторам"},
	"/templates/import/upload":       {opJobCreate, "Импорт шаблонов из XLSX"},
	"/api/schedules/bind-template":   {opScheduleAssign, "Шаблон привязан к расписанию (API)"},
	"/tasks/run-now":                 {opManualOperation, "Ручной запуск планировщика"},
	"/api/tasks/run-now":             {opManualOperation, "Ручной запуск планировщика (API)"},
	"/tasks/restart-last":            {opManualOperation, "Повтор последнего цикла планировщика"},
	"/api/tasks/restart-last":        {opManualOperation, "Повтор последнего цикла планировщика (API)"},
	"/integration/save":              {opConfigChange, "Изменены настройки интеграции"},
	"/log-delivery/save":             {opConfigChange, "Изменены настройки уведомлений и рассылки"},
	"/settings/inverter-time":        {opManualOperation, "Запись времени инвертора"},
	"/api/inverter-time/set":         {opManualOperation, "Запись времени инвертора (API)"},
}

type statusCapturingWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (w *statusCapturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if len(w.body) < 4096 {
		n := 4096 - len(w.body)
		if n > len(b) {
			n = len(b)
		}
		w.body = append(w.body, b[:n]...)
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusCapturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// historyMiddleware records every configuration and job mutation that goes
// through the listed routes, including success/failure and duration.
func (a *App) historyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rule, ok := historyRouteRules[r.URL.Path]
		if !ok || r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		_ = r.ParseMultipartForm(32 << 20)
		_ = r.ParseForm()
		form := map[string]string{}
		for k, v := range r.Form {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "password") || strings.Contains(lk, "token") || strings.Contains(lk, "secret") {
				form[k] = "***"
				continue
			}
			val := strings.Join(v, ",")
			if len(val) > 300 {
				val = val[:300] + "…"
			}
			form[k] = val
		}
		sw := &statusCapturingWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		status := histApplied
		errText := ""
		if sw.status >= 400 {
			status = histError
			var resp struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(sw.body, &resp) == nil && resp.Message != "" {
				errText = resp.Message
			} else {
				errText = strings.TrimSpace(string(sw.body))
			}
		}
		if rule.Type == opManualOperation && strings.Contains(r.URL.Path, "run-now") && status == histApplied {
			status = histInfo
		}
		var inverterID int64
		for _, key := range []string{"inverter_id", "id"} {
			if v, err := strconv.ParseInt(form[key], 10, 64); err == nil && v > 0 && strings.HasPrefix(r.URL.Path, "/inverters") == (key == "id") {
				inverterID = v
				break
			}
		}
		jobRef := ""
		if strings.HasPrefix(r.URL.Path, "/templates") && form["id"] != "" {
			jobRef = "template:" + form["id"]
		}
		if form["ids"] != "" {
			jobRef = strings.TrimSpace(jobRef + " inverters:" + form["ids"])
		}
		if form["template_id"] != "" {
			jobRef = strings.TrimSpace(jobRef + " template:" + form["template_id"])
		}
		delete(form, "schedule_json")
		a.recordHistory(HistoryEntry{
			OperationType: rule.Type, Status: status, Initiator: requestInitiator(r), InverterID: inverterID,
			JobRef: jobRef, DurationMS: time.Since(started).Milliseconds(), Message: rule.Message, Error: errText,
			Details: map[string]any{"route": r.URL.Path, "http_status": sw.status, "form": form},
		})
	})
}

func parseRegisterNumber(raw string) (uint16, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	base := 10
	if strings.HasPrefix(raw, "0x") {
		raw, base = raw[2:], 16
	}
	n, err := strconv.ParseUint(raw, base, 16)
	if err != nil {
		return 0, fmt.Errorf("некорректный адрес регистра: %q", raw)
	}
	return uint16(n), nil
}
