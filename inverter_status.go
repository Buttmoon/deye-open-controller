package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/simonvetter/modbus"
)

type statusMetric struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Code      string   `json:"code,omitempty"`
	Address   int      `json:"address,omitempty"`
	Unit      string   `json:"unit,omitempty"`
	Value     *float64 `json:"value,omitempty"`
	Text      string   `json:"text,omitempty"`
	Supported bool     `json:"supported"`
	Error     string   `json:"error,omitempty"`
}

type InverterStatusSnapshot struct {
	InverterID          int64          `json:"inverter_id"`
	Name                string         `json:"name"`
	Endpoint            string         `json:"endpoint"`
	ModelKey            string         `json:"model_key"`
	ModelName           string         `json:"model_name"`
	ProfileStatus       string         `json:"profile_status"`
	State               string         `json:"state"` // online | unstable | offline | unknown
	StateLabel          string         `json:"state_label"`
	Source              string         `json:"source"` // live | cache | none
	SourceLabel         string         `json:"source_label"`
	Notice              string         `json:"notice,omitempty"`
	LastSuccessUTC      *time.Time     `json:"last_success_utc,omitempty"`
	LastAttemptUTC      *time.Time     `json:"last_attempt_utc,omitempty"`
	AgeSeconds          int64          `json:"age_seconds"`
	Stale               bool           `json:"stale"`
	ConsecutiveFailures int            `json:"consecutive_failures"`
	LastError           string         `json:"last_error,omitempty"`
	Metrics             []statusMetric `json:"metrics"`
	FaultWords          []string       `json:"fault_words,omitempty"`
	HasFault            bool           `json:"has_fault"`
	FaultText           string         `json:"fault_text,omitempty"`
	WorkMode            string         `json:"work_mode,omitempty"`
	UseTimerMask        *int           `json:"use_timer_mask,omitempty"`
	TOUEnabled          bool           `json:"tou_enabled"`
	TOUDays             []string       `json:"tou_days,omitempty"`
	ScheduleEnabled     bool           `json:"schedule_enabled"`
	ScheduleName        string         `json:"schedule_name,omitempty"`
	ScheduleTemplate    string         `json:"schedule_template,omitempty"`
	LastScheduleRun     string         `json:"last_schedule_run,omitempty"`
	LastScheduleStatus  string         `json:"last_schedule_status,omitempty"`
	Unsupported         []string       `json:"unsupported,omitempty"`
	ReadDurationMS      int64          `json:"read_duration_ms"`
	ReadRequests        int            `json:"read_requests"`
}

var statusStateLabels = map[string]string{
	"online":   "В сети",
	"unstable": "Связь нестабильна",
	"offline":  "Не в сети",
	"unknown":  "Нет данных",
}

// statusMetricSpecs lists dashboard metrics and the profile codes that provide
// them. A metric is shown only when the inverter's profile defines the code.
var statusMetricSpecs = []struct {
	Key, Label string
	Codes      []string
	Sum        bool
}{
	{"soc", "SOC батареи", []string{"battery_soc_3p"}, false},
	{"battery_power", "Мощность батареи", []string{"battery_power_3p"}, false},
	{"grid_power", "Мощность сети", []string{"grid_power_3p"}, false},
	{"load_power", "Мощность нагрузки", []string{"load_power_3p"}, false},
	{"pv_power", "Мощность PV (сумма MPPT)", []string{"pv1_power_3p", "pv2_power_3p", "pv3_power_3p", "pv4_power_3p"}, true},
	{"inverter_power", "Мощность инвертора", []string{"inverter_power_3p"}, false},
	{"battery_temperature", "Температура батареи", []string{"battery_temperature_3p"}, false},
	{"grid_frequency", "Частота сети", []string{"grid_frequency_3p"}, false},
	{"rated_power", "Номинальная мощность (из инвертора)", []string{"rated_power_3p"}, false},
}

var statusExtraCodes = []string{"inverter_work_mode", "use_timer", "fault_3p"}

type statusCall struct {
	done chan struct{}
	snap InverterStatusSnapshot
}

type inverterStatusMonitor struct {
	app          *App
	mu           sync.Mutex
	inflight     map[int64]*statusCall
	reconfigured chan struct{}
}

func newInverterStatusMonitor(app *App) *inverterStatusMonitor {
	return &inverterStatusMonitor{app: app, inflight: map[int64]*statusCall{}, reconfigured: make(chan struct{}, 1)}
}

func (m *inverterStatusMonitor) reconfigure() {
	if m == nil {
		return
	}
	select {
	case m.reconfigured <- struct{}{}:
	default:
	}
}

// run polls all inverters in the background when enabled in settings.
func (m *inverterStatusMonitor) run(stop <-chan struct{}) {
	for {
		interval := time.Duration(m.app.kvInt(kvStatusPollIntervalSeconds)) * time.Second
		if interval < 10*time.Second {
			interval = 10 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-m.reconfigured:
			timer.Stop()
			continue
		case <-timer.C:
		}
		if !m.app.kvBool(kvStatusPollEnabled) {
			continue
		}
		for _, inv := range m.app.inverterOptions() {
			select {
			case <-stop:
				return
			default:
			}
			m.refresh(inv.ID)
		}
	}
}

// refresh reads live data for one inverter; concurrent callers share a read.
func (m *inverterStatusMonitor) refresh(inverterID int64) InverterStatusSnapshot {
	m.mu.Lock()
	if call, ok := m.inflight[inverterID]; ok {
		m.mu.Unlock()
		<-call.done
		return call.snap
	}
	call := &statusCall{done: make(chan struct{})}
	m.inflight[inverterID] = call
	m.mu.Unlock()

	call.snap = m.readLive(inverterID)

	m.mu.Lock()
	delete(m.inflight, inverterID)
	m.mu.Unlock()
	close(call.done)
	return call.snap
}

type statusInverterRow struct {
	ID       int64
	Name     string
	IP       string
	Port     int
	ModelKey string
}

func (m *inverterStatusMonitor) loadInverter(id int64) (statusInverterRow, error) {
	var r statusInverterRow
	err := m.app.db.QueryRow(`SELECT id, COALESCE(NULLIF(TRIM(name), ''), 'Инвертор ' || id), ip, port, model_key FROM inverters WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.IP, &r.Port, &r.ModelKey)
	return r, err
}

func (m *inverterStatusMonitor) baseSnapshot(inv statusInverterRow) InverterStatusSnapshot {
	s := InverterStatusSnapshot{
		InverterID: inv.ID, Name: inv.Name, Endpoint: fmt.Sprintf("%s:%d", inv.IP, inv.Port), ModelKey: normalizeInverterModelKey(inv.ModelKey),
		State: "unknown", Source: "none", Metrics: []statusMetric{},
	}
	if model, err := findInverterModel(inv.ModelKey); err == nil {
		s.ModelName = model.Name
		s.ProfileStatus = model.ValidationStatus
	}
	m.fillScheduleInfo(&s)
	return s
}

func (m *inverterStatusMonitor) fillScheduleInfo(s *InverterStatusSnapshot) {
	var enabled int
	var name, tmpl string
	if err := m.app.db.QueryRow(`SELECT s.is_enabled, s.name, COALESCE(t.name, '') FROM schedules s LEFT JOIN schedule_templates t ON t.id = s.applied_template_id WHERE s.inverter_id = ?`, s.InverterID).
		Scan(&enabled, &name, &tmpl); err == nil {
		s.ScheduleEnabled, s.ScheduleName, s.ScheduleTemplate = enabled == 1, name, tmpl
	}
	var ts, status string
	if err := m.app.db.QueryRow(`SELECT executed_at_utc, status FROM task_runs WHERE inverter_id = ? ORDER BY id DESC LIMIT 1`, s.InverterID).Scan(&ts, &status); err == nil {
		s.LastScheduleRun, s.LastScheduleStatus = ts, status
	}
}

type statusReadPlan struct {
	fields map[string]DeviceParameterFields
	blocks []modbusBlockRead
}

// buildStatusReadPlan merges the addresses of all requested fields into as few
// Modbus requests as possible (gaps up to 16 registers are read through).
func buildStatusReadPlan(params []DeviceParameterFixture) statusReadPlan {
	byCode := deviceParametersByCode(params)
	plan := statusReadPlan{fields: map[string]DeviceParameterFields{}}
	addrs := map[modbus.RegType]map[uint16]bool{}
	want := append([]string{}, statusExtraCodes...)
	for _, spec := range statusMetricSpecs {
		want = append(want, spec.Codes...)
	}
	for _, code := range want {
		f, ok := byCode[code]
		if !ok {
			continue
		}
		rt, ok := getRegisterType(f.RegisterType)
		if !ok {
			continue
		}
		plan.fields[code] = f
		if addrs[rt] == nil {
			addrs[rt] = map[uint16]bool{}
		}
		for _, a := range deviceParameterAddresses(f) {
			addrs[rt][a] = true
		}
	}
	for rt, set := range addrs {
		list := make([]int, 0, len(set))
		for a := range set {
			list = append(list, int(a))
		}
		sort.Ints(list)
		start, end := -1, -1
		flush := func() {
			if start >= 0 {
				plan.blocks = append(plan.blocks, modbusBlockRead{Address: uint16(start), Count: uint16(end - start + 1), RegisterType: rt})
			}
		}
		for _, a := range list {
			if start >= 0 && a-end <= 16 && a-start+1 <= modbusMaxReadRegisters {
				end = a
				continue
			}
			flush()
			start, end = a, a
		}
		flush()
	}
	sort.Slice(plan.blocks, func(i, j int) bool {
		if plan.blocks[i].RegisterType != plan.blocks[j].RegisterType {
			return plan.blocks[i].RegisterType < plan.blocks[j].RegisterType
		}
		return plan.blocks[i].Address < plan.blocks[j].Address
	})
	return plan
}

func (p statusReadPlan) values(f DeviceParameterFields) ([]uint16, bool) {
	rt, _ := getRegisterType(f.RegisterType)
	out := []uint16{}
	for _, a := range deviceParameterAddresses(f) {
		found := false
		for _, b := range p.blocks {
			if b.RegisterType == rt && b.Err == nil && a >= b.Address && int(a) < int(b.Address)+len(b.Values) {
				out = append(out, b.Values[a-b.Address])
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return out, true
}

var touDayNames = []string{"Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}

func (m *inverterStatusMonitor) readLive(inverterID int64) InverterStatusSnapshot {
	inv, err := m.loadInverter(inverterID)
	if err != nil {
		return InverterStatusSnapshot{InverterID: inverterID, State: "unknown", StateLabel: statusStateLabels["unknown"], Source: "none", LastError: "инвертор не найден"}
	}
	snap := m.baseSnapshot(inv)
	_, params, err := loadDeviceParametersForModelStructural(inv.ModelKey)
	if err != nil {
		cached := m.cached(inv)
		cached.Notice = "профиль не загружен: " + err.Error()
		return cached
	}
	plan := buildStatusReadPlan(params)
	timeout := time.Duration(m.app.kvInt(kvStatusTimeoutMS)) * time.Millisecond
	if !m.app.tryLockModbus(1500 * time.Millisecond) {
		cached := m.cached(inv)
		cached.Notice = "канал Modbus занят (выполняется запись или опрос) — показаны сохранённые данные"
		return cached
	}
	started := time.Now()
	plan.blocks = readModbusBlocks(inv.IP, inv.Port, 1, timeout, 1, 0, plan.blocks)
	m.app.modbusMu.Unlock()
	now := time.Now().UTC()
	snap.ReadDurationMS = time.Since(started).Milliseconds()
	snap.ReadRequests = len(plan.blocks)
	snap.LastAttemptUTC = &now

	okBlocks := 0
	var lastErr error
	for _, b := range plan.blocks {
		if b.Err == nil {
			okBlocks++
		} else {
			lastErr = b.Err
		}
	}
	if okBlocks == 0 {
		failures := m.recordFailure(inv.ID, now, lastErr)
		cached := m.cached(inv)
		cached.ReadDurationMS, cached.ReadRequests = snap.ReadDurationMS, snap.ReadRequests
		cached.Notice = "опрос не удался: " + classifyModbusError(lastErr)
		if failures < m.app.kvInt(kvStatusOfflineAfterFailures) {
			cached.State = "unstable"
			cached.StateLabel = statusStateLabels["unstable"] + fmt.Sprintf(" (неудачных опросов подряд: %d)", failures)
		}
		return cached
	}

	fillStatusMetrics(&snap, plan)
	snap.LastSuccessUTC = &now
	snap.Source, snap.SourceLabel = "live", "Получено с инвертора сейчас"
	snap.State, snap.StateLabel = "online", statusStateLabels["online"]
	if okBlocks < len(plan.blocks) {
		snap.Notice = fmt.Sprintf("прочитано %d из %d блоков регистров: %s", okBlocks, len(plan.blocks), classifyModbusError(lastErr))
	}
	m.storeSuccess(snap)
	return snap
}

func fillStatusMetrics(snap *InverterStatusSnapshot, plan statusReadPlan) {
	snap.Metrics = []statusMetric{}
	for _, spec := range statusMetricSpecs {
		metric := statusMetric{Key: spec.Key, Label: spec.Label}
		var sum float64
		got := 0
		for _, code := range spec.Codes {
			f, ok := plan.fields[code]
			if !ok {
				continue
			}
			metric.Supported = true
			if metric.Code == "" {
				metric.Code, metric.Address = code, int(f.ModbusAddress)
				if f.Suffix != nil {
					metric.Unit = *f.Suffix
				}
			}
			raw, ok := plan.values(f)
			if !ok {
				metric.Error = "регистр не прочитан"
				continue
			}
			v, err := decodeDeviceParameterRegisters(raw, f)
			if err != nil {
				metric.Error = err.Error()
				continue
			}
			sum += v
			got++
			if !spec.Sum {
				break
			}
		}
		if got > 0 {
			metric.Value = &sum
			metric.Error = ""
		}
		if !metric.Supported {
			metric.Text = "не поддерживается профилем"
		}
		snap.Metrics = append(snap.Metrics, metric)
	}
	if f, ok := plan.fields["inverter_work_mode"]; ok {
		if raw, ok := plan.values(f); ok {
			d := decodeRegisterValue(raw, &f)
			snap.WorkMode = d.DecodedStr
		}
	}
	if f, ok := plan.fields["use_timer"]; ok {
		if raw, ok := plan.values(f); ok {
			mask := int(raw[0])
			snap.UseTimerMask = &mask
			snap.TOUEnabled = mask&1 == 1
			for i, name := range touDayNames {
				if mask&(1<<(i+1)) != 0 {
					snap.TOUDays = append(snap.TOUDays, name)
				}
			}
		}
	}
	if f, ok := plan.fields["fault_3p"]; ok {
		if raw, ok := plan.values(f); ok {
			active := []string{}
			for i, w := range raw {
				snap.FaultWords = append(snap.FaultWords, fmt.Sprintf("0x%04X", w))
				for b := 0; b < 16; b++ {
					if w&(1<<b) != 0 {
						active = append(active, fmt.Sprintf("слово %d бит %d", i+1, b))
					}
				}
			}
			if len(active) > 0 {
				snap.HasFault = true
				snap.FaultText = "активные биты неисправностей: " + strings.Join(active, ", ") + " (расшифровка кодов в профиле отсутствует — см. документацию Deye)"
			} else {
				snap.FaultText = "неисправностей не зарегистрировано"
			}
		}
	} else {
		snap.Unsupported = append(snap.Unsupported, "коды неисправностей")
	}
	snap.Unsupported = append(snap.Unsupported, "серийный номер", "версия прошивки")
}

func (m *inverterStatusMonitor) recordFailure(id int64, now time.Time, err error) int {
	msg := classifyModbusError(err)
	_, _ = m.app.db.Exec(`INSERT INTO inverter_status_cache (inverter_id, snapshot_json, last_success_utc, last_attempt_utc, consecutive_failures, last_error)
		VALUES (?, '', '', ?, 1, ?)
		ON CONFLICT(inverter_id) DO UPDATE SET last_attempt_utc = excluded.last_attempt_utc, consecutive_failures = consecutive_failures + 1, last_error = excluded.last_error`,
		id, now.Format(time.RFC3339Nano), msg)
	var n int
	_ = m.app.db.QueryRow(`SELECT consecutive_failures FROM inverter_status_cache WHERE inverter_id = ?`, id).Scan(&n)
	return n
}

func (m *inverterStatusMonitor) storeSuccess(s InverterStatusSnapshot) {
	b, _ := json.Marshal(s)
	ts := s.LastSuccessUTC.Format(time.RFC3339Nano)
	_, _ = m.app.db.Exec(`INSERT INTO inverter_status_cache (inverter_id, snapshot_json, last_success_utc, last_attempt_utc, consecutive_failures, last_error)
		VALUES (?, ?, ?, ?, 0, '')
		ON CONFLICT(inverter_id) DO UPDATE SET snapshot_json = excluded.snapshot_json, last_success_utc = excluded.last_success_utc,
		last_attempt_utc = excluded.last_attempt_utc, consecutive_failures = 0, last_error = ''`, s.InverterID, string(b), ts, ts)
}

// cached returns the last persisted snapshot, always labelled as cached and
// with connection state derived from the failure counter.
func (m *inverterStatusMonitor) cached(inv statusInverterRow) InverterStatusSnapshot {
	base := m.baseSnapshot(inv)
	var raw, lastSuccess, lastAttempt, lastErr string
	var failures int
	err := m.app.db.QueryRow(`SELECT snapshot_json, last_success_utc, last_attempt_utc, consecutive_failures, last_error FROM inverter_status_cache WHERE inverter_id = ?`, inv.ID).
		Scan(&raw, &lastSuccess, &lastAttempt, &failures, &lastErr)
	if err == sql.ErrNoRows || err != nil {
		base.StateLabel = statusStateLabels["unknown"]
		base.SourceLabel = "Данные ещё не запрашивались"
		return base
	}
	snap := base
	if raw != "" {
		var stored InverterStatusSnapshot
		if json.Unmarshal([]byte(raw), &stored) == nil {
			snap.Metrics, snap.FaultWords, snap.HasFault, snap.FaultText = stored.Metrics, stored.FaultWords, stored.HasFault, stored.FaultText
			snap.WorkMode, snap.UseTimerMask, snap.TOUEnabled, snap.TOUDays = stored.WorkMode, stored.UseTimerMask, stored.TOUEnabled, stored.TOUDays
			snap.Unsupported, snap.ReadDurationMS, snap.ReadRequests = stored.Unsupported, stored.ReadDurationMS, stored.ReadRequests
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, lastSuccess); err == nil {
		snap.LastSuccessUTC = &t
		snap.AgeSeconds = int64(time.Since(t).Seconds())
		snap.Stale = snap.AgeSeconds > int64(m.app.kvInt(kvStatusStaleAfterSeconds))
		snap.Source, snap.SourceLabel = "cache", "Сохранённые данные (не в реальном времени)"
	} else {
		snap.SourceLabel = "Успешных опросов ещё не было"
	}
	if t, err := time.Parse(time.RFC3339Nano, lastAttempt); err == nil {
		snap.LastAttemptUTC = &t
	}
	snap.ConsecutiveFailures, snap.LastError = failures, lastErr
	switch {
	case failures >= m.app.kvInt(kvStatusOfflineAfterFailures):
		snap.State = "offline"
	case failures > 0:
		snap.State = "unstable"
	case snap.LastSuccessUTC != nil:
		snap.State = "online"
	}
	snap.StateLabel = statusStateLabels[snap.State]
	if snap.State == "online" && snap.Stale {
		snap.StateLabel = "В сети (данные устарели)"
	}
	return snap
}

func (m *inverterStatusMonitor) allCached() []InverterStatusSnapshot {
	out := []InverterStatusSnapshot{}
	rows, err := m.app.db.Query(`SELECT id, COALESCE(NULLIF(TRIM(name), ''), 'Инвертор ' || id), ip, port, model_key FROM inverters ORDER BY id`)
	if err != nil {
		return out
	}
	list := []statusInverterRow{}
	for rows.Next() {
		var r statusInverterRow
		if rows.Scan(&r.ID, &r.Name, &r.IP, &r.Port, &r.ModelKey) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	for _, r := range list {
		out = append(out, m.cached(r))
	}
	return out
}

type StatusPageData struct {
	Title             string
	BackgroundPolling bool
	PollInterval      int
	StaleAfter        int
	OfflineAfter      int
}

func (a *App) statusPageHandler(w http.ResponseWriter, r *http.Request) {
	data := StatusPageData{
		Title: "Состояние инверторов", BackgroundPolling: a.kvBool(kvStatusPollEnabled), PollInterval: a.kvInt(kvStatusPollIntervalSeconds),
		StaleAfter: a.kvInt(kvStatusStaleAfterSeconds), OfflineAfter: a.kvInt(kvStatusOfflineAfterFailures),
	}
	if err := a.tmplStatus.Execute(w, data); err != nil {
		a.appendAppLog("error", "status template error", map[string]any{"error": err.Error()})
	}
}

// apiInvertersStatusHandler returns stored snapshots only; it never contacts
// an inverter, so it is safe to call on every page load.
func (a *App) apiInvertersStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "items": a.status.allCached(), "generated_utc": time.Now().UTC(),
		"background_polling": a.kvBool(kvStatusPollEnabled), "poll_interval_seconds": a.kvInt(kvStatusPollIntervalSeconds),
	})
}

// apiInverterStatusRefreshHandler performs a live, read-only poll.
func (a *App) apiInverterStatusRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	_ = r.ParseForm()
	idRaw := strings.TrimSpace(r.FormValue("inverter_id"))
	if idRaw == "" || idRaw == "all" {
		items := []InverterStatusSnapshot{}
		for _, inv := range a.inverterOptions() {
			items = append(items, a.status.refresh(inv.ID))
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
		return
	}
	id, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный inverter_id"})
		return
	}
	snap := a.status.refresh(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": []InverterStatusSnapshot{snap}})
}
