package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"inverter-schedule/internal/models"
)

// SimpleRule is one business-level interval of the simplified editor. Rules
// expand into the existing hourly day-of-month schedule model; no new scheduler
// semantics are introduced.
//
// Primary operator-facing fields are GridChargeEnabled, LoadLimitMode and
// PriorityLoad (mapped to registers 130, 142 and 141). PowerW / BatterySOC are
// still required for TOU encoding (registers 154–159 / 166–171).
type SimpleRule struct {
	Name              string `json:"name"`
	DateFrom          string `json:"date_from,omitempty"` // YYYY-MM-DD, inclusive
	DateTo            string `json:"date_to,omitempty"`   // YYYY-MM-DD, inclusive
	Weekdays          []int  `json:"weekdays,omitempty"`  // 1=Пн … 7=Вс; empty = every day
	StartTime         string `json:"start_time,omitempty"` // HH:MM inclusive
	EndTime           string `json:"end_time,omitempty"`   // HH:MM or 24:00 exclusive; End<=Start wraps midnight
	StartHour         int    `json:"start_hour"`           // 0..23 (legacy / derived)
	EndHour           int    `json:"end_hour"`             // 0..24 exclusive; EndHour<=StartHour wraps past midnight
	Mode              string `json:"mode"`                 // self | grid_charge | sell | sell_grid | custom
	ChargeMode        *int   `json:"charge_mode,omitempty"`
	PowerW            int    `json:"power_w"`
	BatterySOC        int    `json:"battery_soc"`
	GridChargeEnabled *bool  `json:"grid_charge_enabled,omitempty"`
	GridExportLimitW  int    `json:"grid_export_limit_w"`
	LoadLimitMode     int    `json:"load_limit_mode"` // inverter_work_mode / «Load Limit»: 0..2
	PriorityLoad      int    `json:"priority_load"`   // 0=Load First, 1=Battery First
	SolarExport       bool   `json:"solar_export"`

	// Cached minute bounds after normalizeSimpleRuleTimes (not serialized).
	startMin int `json:"-"`
	endMin   int `json:"-"`
}

type simpleModePreset struct {
	Label      string
	ChargeMode int
	GridCharge bool
}

// Presets only choose the TOU charge-mode code (documented in the profiles'
// charge_mode_point_* enum) and the grid-charge flag; every other value is
// taken from the rule as entered by the operator.
var simpleModePresets = map[string]simpleModePreset{
	"self":        {"Самопотребление (без сети)", 0, false},
	"grid_charge": {"Заряд от сети", 1, true},
	"sell":        {"Продажа в сеть", 32, false},
	"sell_grid":   {"Продажа и заряд от сети", 33, true},
}

func simpleModeLabel(r SimpleRule) string {
	if p, ok := simpleModePresets[r.Mode]; ok {
		return p.Label
	}
	if r.ChargeMode != nil {
		return "Пользовательский: " + chargeModeLabelFromValue(*r.ChargeMode)
	}
	return "Пользовательский"
}

type SimpleScheduleRequest struct {
	InverterIDs        []int64      `json:"inverter_ids"`
	Month              string       `json:"month"` // YYYY-MM
	Rules              []SimpleRule `json:"rules"`
	BaseMode           string       `json:"base_mode"` // replace | merge
	UseTimerMask       *int         `json:"use_timer_mask,omitempty"`
	Name               string       `json:"name"`
	AutoRenew          bool         `json:"auto_renew"`
	ConfirmDestructive bool         `json:"confirm_destructive"`
	TemplateName       string       `json:"template_name,omitempty"`
	TemplateID         int64        `json:"template_id,omitempty"`
	SaveAsTemplate     bool         `json:"save_as_template,omitempty"`
}

type simpleInterval struct {
	Start      int    `json:"start"`                // hour floor of start (compat)
	End        int    `json:"end"`                  // hour ceil of exclusive end (compat)
	StartMin   int    `json:"start_min"`            // minutes from midnight, inclusive
	EndMin     int    `json:"end_min"`              // minutes from midnight, exclusive
	Rule       int    `json:"rule"`
	RuleName   string `json:"rule_name"`
	Mode       string `json:"mode"`
	ModeLabel  string `json:"mode_label"`
	PowerW     int    `json:"power_w"`
	SOC        int    `json:"soc"`
	ChargeMode int    `json:"charge_mode"`
	PartialHour bool  `json:"partial_hour,omitempty"`
}

type simpleDayPreview struct {
	Day       int              `json:"day"`
	Date      string           `json:"date"`
	Weekday   string           `json:"weekday"`
	Intervals []simpleInterval `json:"intervals"`
	Kept      []int            `json:"kept_hours,omitempty"`
}

type simpleConflict struct {
	Date    string `json:"date"`
	Hour    int    `json:"hour"`
	RuleA   int    `json:"rule_a"`
	RuleB   int    `json:"rule_b"`
	Message string `json:"message"`
}

type SimpleScheduleResult struct {
	Month            string             `json:"month"`
	Days             []simpleDayPreview `json:"days"`
	Conflicts        []simpleConflict   `json:"conflicts"`
	Warnings         []string           `json:"warnings"`
	Destructive      []string           `json:"destructive"`
	Descriptions     []string           `json:"descriptions"`
	ScheduleJSON     string             `json:"schedule_json,omitempty"`
	EnabledHours     int                `json:"enabled_hours"`
	ValidationErrs   []string           `json:"validation_errors,omitempty"`
	IntervalSpans    []intervalSpan     `json:"interval_spans,omitempty"`
	IntervalIssues   []intervalIssue    `json:"interval_issues,omitempty"`
	HardwareSlots    int                `json:"hardware_slots"`
	LogicalDaySlots  int                `json:"logical_day_slots"`
	ExceedsHardware  bool               `json:"exceeds_hardware_slots"`
	SoftwareManaged  bool               `json:"software_managed_ok"`
}

var weekdayShort = []string{"", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}

func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

func parseMonth(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local), nil
	}
	t, err := time.ParseInLocation("2006-01", raw, time.Local)
	if err != nil {
		return t, fmt.Errorf("месяц должен быть в формате ГГГГ-ММ")
	}
	return t, nil
}

func validateSimpleRule(i int, r SimpleRule) error {
	label := fmt.Sprintf("интервал %d", i+1)
	if r.Name != "" {
		label += " «" + r.Name + "»"
	}
	if err := normalizeSimpleRuleTimes(&r); err != nil {
		return fmt.Errorf("%s: %v", label, err)
	}
	if r.StartHour < 0 || r.StartHour > 23 {
		return fmt.Errorf("%s: час начала 0–23", label)
	}
	// EndHour 0 with StartHour>0 means wrap ending at midnight (evening-only part).
	if r.EndHour < 0 || r.EndHour > 24 {
		return fmt.Errorf("%s: час окончания 0–24", label)
	}
	if r.StartHour == r.EndHour && r.EndHour != 24 && r.endMin == r.startMin {
		return fmt.Errorf("%s: пустой интервал", label)
	}
	for _, wd := range r.Weekdays {
		if wd < 1 || wd > 7 {
			return fmt.Errorf("%s: день недели должен быть 1 (Пн) … 7 (Вс)", label)
		}
	}
	var from, to time.Time
	var err error
	if r.DateFrom != "" {
		if from, err = time.ParseInLocation("2006-01-02", r.DateFrom, time.Local); err != nil {
			return fmt.Errorf("%s: дата начала в формате ГГГГ-ММ-ДД", label)
		}
	}
	if r.DateTo != "" {
		if to, err = time.ParseInLocation("2006-01-02", r.DateTo, time.Local); err != nil {
			return fmt.Errorf("%s: дата окончания в формате ГГГГ-ММ-ДД", label)
		}
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return fmt.Errorf("%s: дата окончания раньше даты начала", label)
	}
	if r.Mode == "" {
		r.Mode = "custom"
	}
	if _, ok := simpleModePresets[r.Mode]; !ok {
		if r.Mode != "custom" {
			return fmt.Errorf("%s: неизвестный режим %q", label, r.Mode)
		}
		// Custom mode without charge_mode is allowed when GridChargeEnabled is set:
		// charge mode defaults to grid bit from the switch (0 or 1).
		if r.ChargeMode != nil && !isCanonicalChargeModeValue(*r.ChargeMode) {
			return fmt.Errorf("%s: недопустимый режим заряда TOU", label)
		}
	}
	if r.PowerW < 0 || r.PowerW%10 != 0 {
		return fmt.Errorf("%s: мощность должна быть неотрицательной и кратной 10 Вт", label)
	}
	if r.GridExportLimitW < 0 || r.GridExportLimitW%10 != 0 {
		return fmt.Errorf("%s: лимит экспорта должен быть неотрицательным и кратным 10 Вт", label)
	}
	if r.BatterySOC < 0 || r.BatterySOC > 100 {
		return fmt.Errorf("%s: SOC 0–100%%", label)
	}
	if r.LoadLimitMode < 0 || r.LoadLimitMode > 2 {
		return fmt.Errorf("%s: «Ограничение нагрузки» (регистр 142) принимает значения 0–2", label)
	}
	if r.PriorityLoad < 0 || r.PriorityLoad > 1 {
		return fmt.Errorf("%s: «Приоритет нагрузки» (регистр 141) принимает 0 (нагрузка) или 1 (батарея)", label)
	}
	return nil
}

func (r SimpleRule) appliesOn(date time.Time) bool {
	d := date.Format("2006-01-02")
	if r.DateFrom != "" && d < r.DateFrom {
		return false
	}
	if r.DateTo != "" && d > r.DateTo {
		return false
	}
	if len(r.Weekdays) == 0 {
		return true
	}
	wd := isoWeekday(date)
	for _, w := range r.Weekdays {
		if w == wd {
			return true
		}
	}
	return false
}

// hoursOn returns whole hours touched on a calendar day (compat / preview).
func (r SimpleRule) hoursOn(date time.Time) []int {
	seen := map[int]bool{}
	for _, m := range r.minutesOn(date) {
		seen[m/60] = true
	}
	out := make([]int, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Ints(out)
	return out
}

// minutesOn returns 5-minute slot starts (0,5,…,1435) covered on a calendar day.
// Midnight-wrapping intervals contribute the evening part to the start day and
// the morning part to the following day.
func (r SimpleRule) minutesOn(date time.Time) []int {
	out := []int{}
	add := func(from, to int) {
		if from < 0 {
			from = 0
		}
		if to > 24*60 {
			to = 24 * 60
		}
		for m := from; m < to; m += 5 {
			out = append(out, m)
		}
	}
	if r.endMin > r.startMin {
		if r.appliesOn(date) {
			add(r.startMin, r.endMin)
		}
		return out
	}
	if r.appliesOn(date) {
		add(r.startMin, 24*60)
	}
	if r.appliesOn(date.AddDate(0, 0, -1)) {
		add(0, r.endMin)
	}
	return out
}

func (r SimpleRule) hourConfig(hour, mask int) HourConfig {
	h := defaultHourConfig(hour)
	h.Enabled = true
	if p, ok := simpleModePresets[r.Mode]; ok {
		h.ChargeMode = p.ChargeMode
		h.GridChargeEnabled = p.GridCharge
	} else if r.ChargeMode != nil {
		h.ChargeMode = *r.ChargeMode
	} else if r.GridChargeEnabled != nil && *r.GridChargeEnabled {
		h.ChargeMode = 1 // Allow Grid
		h.GridChargeEnabled = true
	}
	if r.GridChargeEnabled != nil {
		h.GridChargeEnabled = *r.GridChargeEnabled
		// Keep charge-mode grid bit consistent with the operator-facing switch
		// when using the simplified three-parameter editor.
		if r.Mode == "" || r.Mode == "custom" {
			if *r.GridChargeEnabled {
				h.ChargeMode = h.ChargeMode | 1
			} else {
				h.ChargeMode = h.ChargeMode &^ 1
			}
		}
	}
	h.SellModeKW = r.PowerW / 10
	h.SellModeBattCapacity = r.BatterySOC
	h.GridExportLimit = r.GridExportLimitW / 10
	h.LoadLimitMode = r.LoadLimitMode
	h.PriorityLoad = r.PriorityLoad
	h.SolarExport = r.SolarExport
	h.UseTimerMask = mask
	h.UseTimer = mask&1 != 0
	return h
}

func describeSimpleRule(r SimpleRule) string {
	_ = normalizeSimpleRuleTimes(&r)
	days := "каждый день"
	if len(r.Weekdays) > 0 && len(r.Weekdays) < 7 {
		names := []string{}
		ws := append([]int{}, r.Weekdays...)
		sort.Ints(ws)
		for _, w := range ws {
			names = append(names, weekdayShort[w])
		}
		days = "по дням: " + strings.Join(names, ", ")
	}
	period := ""
	switch {
	case r.DateFrom != "" && r.DateTo != "":
		period = fmt.Sprintf(", с %s по %s", r.DateFrom, r.DateTo)
	case r.DateFrom != "":
		period = ", начиная с " + r.DateFrom
	case r.DateTo != "":
		period = ", до " + r.DateTo
	}
	wrap := ""
	if r.endMin <= r.startMin {
		wrap = " (через полночь)"
	}
	name := r.Name
	if name == "" {
		name = "Интервал"
	}
	gc := "выкл"
	if r.GridChargeEnabled != nil && *r.GridChargeEnabled {
		gc = "вкл"
	} else if r.GridChargeEnabled == nil {
		if p, ok := simpleModePresets[r.Mode]; ok && p.GridCharge {
			gc = "вкл"
		}
	}
	pl := "нагрузка"
	if r.PriorityLoad == 1 {
		pl = "батарея"
	}
	ll := loadLimitModeLabel(r.LoadLimitMode)
	return fmt.Sprintf("%s: %s, %s–%s%s%s — заряд сети %s, ограничение нагрузки «%s», приоритет «%s», мощность %d Вт, SOC %d%%",
		name, days, r.StartTime, r.EndTime, wrap, period, gc, ll, pl, r.PowerW, r.BatterySOC)
}

func loadLimitOptions() []map[string]any {
	return []map[string]any{
		{"value": 0, "label": "Selling first", "description": "Приоритет продажи (регистр 142 = 0)"},
		{"value": 1, "label": "Zero export load", "description": "Нулевой экспорт по нагрузке (регистр 142 = 1)"},
		{"value": 2, "label": "Zero export CT", "description": "Нулевой экспорт по CT (регистр 142 = 2)"},
	}
}

func priorityLoadOptions() []map[string]any {
	return []map[string]any{
		{"value": 0, "label": "Приоритет нагрузки (Load First)", "description": "Регистр 141, логическое 0"},
		{"value": 1, "label": "Приоритет батареи (Battery First)", "description": "Регистр 141, логическое 1"},
	}
}

// buildSimpleSchedule expands rules for one month on top of an optional base
// payload. Later rules take precedence; overlaps with different values are
// reported as conflicts.
func buildSimpleSchedule(rules []SimpleRule, month time.Time, base *SchedulePayload, mask int) (SimpleScheduleResult, SchedulePayload, error) {
	res := SimpleScheduleResult{
		Month: month.Format("2006-01"), Days: []simpleDayPreview{}, Conflicts: []simpleConflict{},
		Warnings: []string{}, Destructive: []string{}, Descriptions: []string{},
		HardwareSlots: hardwareTOUSlots, SoftwareManaged: true,
	}
	normalized := make([]SimpleRule, len(rules))
	for i, r := range rules {
		if err := validateSimpleRule(i, r); err != nil {
			return res, SchedulePayload{}, err
		}
		_ = normalizeSimpleRuleTimes(&r)
		if r.Mode == "" {
			r.Mode = "custom"
		}
		normalized[i] = r
		res.Descriptions = append(res.Descriptions, describeSimpleRule(r))
	}
	rules = normalized
	spans, issues, daySlots := analyzeSimpleIntervals(rules, false)
	res.IntervalSpans = spans
	res.IntervalIssues = issues
	res.LogicalDaySlots = daySlots
	res.ExceedsHardware = daySlots > hardwareTOUSlots
	for _, iss := range issues {
		if iss.Kind == "hardware" {
			res.Warnings = append(res.Warnings, iss.Message)
		}
		if iss.Kind == "overlap" {
			res.Conflicts = append(res.Conflicts, simpleConflict{RuleA: iss.RuleA, RuleB: iss.RuleB, Message: iss.Message})
		}
	}
	var payload SchedulePayload
	if base != nil {
		// The compact form turns 5-minute values stored only in Slots into
		// explicit CustomSlots, so they survive the regeneration of Slots below.
		payload = compactSchedulePayloadForEditor(*base)
		normalizeSchedulePayloadSellTimes(&payload)
	} else {
		payload = defaultSchedulePayload()
	}
	payload.UseTimerMask = mask
	daysInMonth := month.AddDate(0, 1, -1).Day()
	removedCustom := 0
	overwritten := 0
	partialHours := 0
	for day := 1; day <= defaultScheduleDays; day++ {
		di := day - 1
		dayItem := &payload.Days[di]
		if day > daysInMonth {
			continue
		}
		date := time.Date(month.Year(), month.Month(), day, 0, 0, 0, 0, time.Local)
		preview := simpleDayPreview{Day: day, Date: date.Format("2006-01-02"), Weekday: weekdayShort[isoWeekday(date)], Intervals: []simpleInterval{}}
		owner := map[int]int{} // minute-of-day → rule index
		overwrittenHours := map[int]bool{}
		for ri, r := range rules {
			for _, m := range r.minutesOn(date) {
				cfg := r.hourConfig(m/60, mask)
				if prev, ok := owner[m]; ok {
					pc := rules[prev].hourConfig(m/60, mask)
					if pc != cfg {
						res.Conflicts = append(res.Conflicts, simpleConflict{Date: preview.Date, Hour: m / 60, RuleA: prev + 1, RuleB: ri + 1,
							Message: fmt.Sprintf("%s %s — правила %d и %d задают разные значения; применяется правило %d", preview.Date, formatClockHM(m), prev+1, ri+1, ri+1)})
					}
				} else if base != nil && dayItem.Hours[m/60].Enabled {
					overwrittenHours[m/60] = true
				}
				owner[m] = ri
			}
		}
		overwritten += len(overwrittenHours)
		if base == nil {
			for h := range dayItem.Hours {
				dayItem.Hours[h] = defaultHourConfig(h)
				dayItem.Hours[h].UseTimerMask, dayItem.Hours[h].UseTimer = mask, mask&1 != 0
			}
		}
		touchedHour := map[int]bool{}
		for m := range owner {
			touchedHour[m/60] = true
		}
		keptCustom := []MinuteSlot{}
		for _, cs := range dayItem.CustomSlots {
			m := cs.Hour*60 + cs.Minute
			if _, owned := owner[m]; owned {
				removedCustom++
				continue
			}
			if touchedHour[cs.Hour] {
				// Hour regenerated from rules — drop stale customs in that hour.
				removedCustom++
				continue
			}
			if base == nil {
				removedCustom++
				continue
			}
			keptCustom = append(keptCustom, cs)
		}
		newCustoms := []MinuteSlot{}
		for h := 0; h < 24; h++ {
			if !touchedHour[h] {
				if base != nil && dayItem.Hours[h].Enabled {
					preview.Kept = append(preview.Kept, h)
				}
				continue
			}
			minuteOwner := [12]int{}
			for i := range minuteOwner {
				minuteOwner[i] = -1
			}
			covered := 0
			for mi := 0; mi < 12; mi++ {
				if ri, ok := owner[h*60+mi*5]; ok {
					minuteOwner[mi] = ri
					covered++
				}
			}
			if covered == 0 {
				continue
			}
			partial := covered < 12
			if partial {
				partialHours++
			}
			if minuteOwner[0] >= 0 {
				dayItem.Hours[h] = rules[minuteOwner[0]].hourConfig(h, mask)
			} else if base == nil {
				dayItem.Hours[h] = defaultHourConfig(h)
				dayItem.Hours[h].UseTimerMask, dayItem.Hours[h].UseTimer = mask, mask&1 != 0
			}
			hourCfg := dayItem.Hours[h]
			for mi := 1; mi < 12; mi++ {
				minute := mi * 5
				if ri := minuteOwner[mi]; ri >= 0 {
					slot := minuteSlotFromHourConfig(rules[ri].hourConfig(h, mask), minute)
					if minuteSlotDiffersFromHour(slot, hourCfg) {
						newCustoms = append(newCustoms, slot)
					}
				} else if hourCfg.Enabled {
					// Punch a hole so the hour baseline does not spill past the interval end.
					slot := minuteSlotFromHourConfig(hourCfg, minute)
					slot.Enabled = false
					if minuteSlotDiffersFromHour(slot, hourCfg) {
						newCustoms = append(newCustoms, slot)
					}
				}
			}
		}
		dayItem.CustomSlots = append(keptCustom, newCustoms...)
		dayItem.Slots = nil
		// Contiguous 5-minute ownership blocks → preview intervals.
		for m := 0; m < 24*60; {
			ri, ok := owner[m]
			if !ok {
				m += 5
				continue
			}
			start := m
			for m < 24*60 {
				if r2, ok2 := owner[m]; !ok2 || r2 != ri {
					break
				}
				m += 5
			}
			r := rules[ri]
			cfg := r.hourConfig(start/60, mask)
			preview.Intervals = append(preview.Intervals, simpleInterval{
				Start: start / 60, End: (m + 59) / 60, StartMin: start, EndMin: m,
				Rule: ri + 1, RuleName: r.Name, Mode: r.Mode, ModeLabel: simpleModeLabel(r),
				PowerW: r.PowerW, SOC: r.BatterySOC, ChargeMode: cfg.ChargeMode,
				PartialHour: start%60 != 0 || m%60 != 0,
			})
		}
		res.Days = append(res.Days, preview)
	}
	res.EnabledHours = 0
	for _, d := range res.Days {
		seen := map[int]bool{}
		for _, iv := range d.Intervals {
			for m := iv.StartMin; m < iv.EndMin; m += 5 {
				seen[m/60] = true
			}
		}
		res.EnabledHours += len(seen)
	}
	if partialHours > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"интервалы с точностью 5 минут затрагивают частичные часы (%d): значения сохраняются как часовой базис HH:00 плюс 5-минутные переопределения (customSlots), совместимые с расширенным режимом и XLSX",
			partialHours,
		))
	}
	normalizeSchedulePayloadSellTimes(&payload)
	if removedCustom > 0 {
		res.Destructive = append(res.Destructive, fmt.Sprintf("будут удалены 5-минутные переопределения: %d (в часах, которые задают правила)", removedCustom))
	}
	if base != nil && overwritten > 0 {
		res.Destructive = append(res.Destructive, fmt.Sprintf("будут перезаписаны ранее настроенные часы: %d", overwritten))
	}
	if base == nil {
		res.Warnings = append(res.Warnings, "режим «Заменить»: часы, не покрытые правилами, будут выключены (планировщик не будет в них ничего записывать)")
	}
	if daysInMonth < defaultScheduleDays {
		res.Warnings = append(res.Warnings, fmt.Sprintf("в месяце %d дней: дни %d–31 расписания не выполняются", daysInMonth, daysInMonth+1))
	}
	res.Warnings = append(res.Warnings, "расписание хранится по числам месяца: дни недели пересчитываются для выбранного месяца; для следующих месяцев включите автопродление")
	b, err := json.Marshal(compactSchedulePayloadForEditor(payload))
	if err != nil {
		return res, payload, err
	}
	res.ScheduleJSON = string(b)
	return res, payload, nil
}

func decodeSimpleRequest(r *http.Request) (SimpleScheduleRequest, error) {
	var req SimpleScheduleRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 512<<10)).Decode(&req); err != nil {
		return req, fmt.Errorf("некорректный JSON: %v", err)
	}
	if len(req.Rules) == 0 {
		return req, fmt.Errorf("добавьте хотя бы один интервал")
	}
	if len(req.Rules) > 500 {
		return req, fmt.Errorf("не более 500 интервалов в одном запросе")
	}
	for i := range req.Rules {
		_ = normalizeSimpleRuleTimes(&req.Rules[i])
		if req.Rules[i].Mode == "" {
			req.Rules[i].Mode = "custom"
		}
	}
	return req, nil
}

func (req SimpleScheduleRequest) mask() int {
	if req.UseTimerMask != nil {
		return *req.UseTimerMask
	}
	return 255
}

// resolveSimpleSchedule builds the schedule for one inverter (or a generic
// preview when inverterID == 0) and validates it against the inverter models.
func (a *App) resolveSimpleSchedule(req SimpleScheduleRequest, inverterID int64, now time.Time) (SimpleScheduleResult, error) {
	month, err := parseMonth(req.Month, now)
	if err != nil {
		return SimpleScheduleResult{}, err
	}
	if m := req.mask(); m < 0 || m > 255 {
		return SimpleScheduleResult{}, fmt.Errorf("маска Use Timer 0–255")
	}
	var base *SchedulePayload
	if req.BaseMode == "merge" && inverterID > 0 {
		if s, err := a.getScheduleByInverterID(inverterID); err == nil && strings.TrimSpace(s.ScheduleJSON) != "" {
			if p, err := parseSchedulePayloadLoose(s.ScheduleJSON); err == nil {
				base = &p
			}
		}
		if base == nil {
			p := defaultSchedulePayload()
			base = &p
		}
	}
	res, _, err := buildSimpleSchedule(req.Rules, month, base, req.mask())
	if err != nil {
		return res, err
	}
	if req.BaseMode != "merge" && inverterID > 0 {
		if s, err := a.getScheduleByInverterID(inverterID); err == nil && strings.TrimSpace(s.ScheduleJSON) != "" {
			if sum := summarizeSchedule(s.ScheduleJSON); sum.EnabledHours > 0 || sum.CustomSlots > 0 {
				res.Destructive = append(res.Destructive, fmt.Sprintf("существующее расписание «%s» будет полностью заменено (включённых часов: %d, 5-минутных переопределений: %d)", s.Name, sum.EnabledHours, sum.CustomSlots))
			}
		}
	}
	models := []InverterModelDefinition{}
	ids := req.InverterIDs
	if inverterID > 0 {
		ids = []int64{inverterID}
	}
	if len(ids) > 0 {
		invs, err := a.getBasicInvertersByIDs(ids)
		if err != nil {
			return res, err
		}
		for _, inv := range invs {
			if m, err := findInverterModel(inv.ModelKey); err == nil {
				models = append(models, m)
			}
		}
	}
	if err := validateScheduleJSONForModels(res.ScheduleJSON, models); err != nil {
		res.ValidationErrs = append(res.ValidationErrs, err.Error())
	}
	return res, nil
}

func (a *App) apiSimpleSchedulePreviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 512<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения тела запроса"})
		return
	}
	var req SimpleScheduleRequest
	var extras struct {
		IncludeJSON bool `json:"include_json"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON: " + err.Error()})
		return
	}
	_ = json.Unmarshal(body, &extras)
	if len(req.Rules) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "добавьте хотя бы один интервал"})
		return
	}
	for i := range req.Rules {
		_ = normalizeSimpleRuleTimes(&req.Rules[i])
		if req.Rules[i].Mode == "" {
			req.Rules[i].Mode = "custom"
		}
	}
	var first int64
	if len(req.InverterIDs) > 0 {
		first = req.InverterIDs[0]
	} else if target, _, err := a.defaultModbusTarget(); err == nil {
		first = target.InverterID
		req.InverterIDs = []int64{first}
	}
	res, err := a.resolveSimpleSchedule(req, first, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if !extras.IncludeJSON {
		res.ScheduleJSON = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": len(res.ValidationErrs) == 0, "result": res})
}

func scheduleHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type recurrenceState struct {
	Request       SimpleScheduleRequest `json:"request"`
	GeneratedHash string                `json:"generated_hash"`
}

func (a *App) apiSimpleScheduleApplyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	req, err := decodeSimpleRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if len(req.InverterIDs) == 0 {
		if target, _, err := a.defaultModbusTarget(); err == nil {
			req.InverterIDs = []int64{target.InverterID}
		}
	}
	if len(req.InverterIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Инвертор не настроен"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Упрощённое расписание"
	}
	now := time.Now()
	results := map[int64]SimpleScheduleResult{}
	for _, id := range req.InverterIDs {
		res, err := a.resolveSimpleSchedule(req, id, now)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		if len(res.ValidationErrs) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "Расписание не прошло проверку: " + strings.Join(res.ValidationErrs, "; "), "inverter_id": id})
			return
		}
		if len(res.Destructive) > 0 && !req.ConfirmDestructive {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "needs_confirmation": true, "message": "Изменение удалит или перезапишет существующие настройки — подтвердите", "destructive": res.Destructive, "inverter_id": id})
			return
		}
		results[id] = res
	}
	opID := newOperationID()
	for _, id := range req.InverterIDs {
		res := results[id]
		prevJSON := ""
		if s, err := a.getScheduleByInverterID(id); err == nil {
			prevJSON = s.ScheduleJSON
		}
		if err := a.saveScheduleForInverter(id, name, "list", res.ScheduleJSON); err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения расписания: " + err.Error()})
			return
		}
		state := recurrenceState{Request: req, GeneratedHash: scheduleHash(res.ScheduleJSON)}
		state.Request.InverterIDs = []int64{id}
		state.Request.ConfirmDestructive = false
		sb, _ := json.Marshal(state)
		if req.AutoRenew {
			_, _ = a.db.Exec(`INSERT INTO schedule_recurrences (inverter_id, rules_json, auto_renew, last_generated_month, updated_at) VALUES (?, ?, 1, ?, ?)
				ON CONFLICT(inverter_id) DO UPDATE SET rules_json = excluded.rules_json, auto_renew = 1, last_generated_month = excluded.last_generated_month, updated_at = excluded.updated_at`,
				id, string(sb), res.Month, time.Now().UTC().Format(time.RFC3339))
		} else {
			_, _ = a.db.Exec(`DELETE FROM schedule_recurrences WHERE inverter_id = ?`, id)
		}
		a.recordHistory(HistoryEntry{OperationID: newOperationID(), ParentID: opID, OperationType: opJobUpdate, Status: histApplied, InverterID: id, Initiator: requestInitiator(r),
			JobRef: "schedule:" + name, Message: fmt.Sprintf("Упрощённый режим: расписание на %s сохранено (%d ч.)", res.Month, res.EnabledHours),
			Details: map[string]any{"rules": res.Descriptions, "conflicts": len(res.Conflicts), "destructive": res.Destructive, "auto_renew": req.AutoRenew, "previous_hash": scheduleHash(prevJSON)}})
	}
	if req.SaveAsTemplate && strings.TrimSpace(req.TemplateName) != "" {
		rb, _ := json.Marshal(req.Rules)
		_, _ = a.db.Exec(`INSERT INTO simple_templates (name, description, rules_json, created_at, updated_at) VALUES (?, '', ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET rules_json = excluded.rules_json, updated_at = excluded.updated_at`, strings.TrimSpace(req.TemplateName), string(rb), now.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	a.recordHistory(HistoryEntry{OperationID: opID, OperationType: opScheduleAssign, Status: histApplied, Initiator: requestInitiator(r), JobRef: "schedule:" + name,
		Message: fmt.Sprintf("Упрощённое расписание назначено инверторам: %d", len(req.InverterIDs))})
	if tid := req.TemplateID; tid > 0 {
		nowUTC := time.Now().UTC().Format(time.RFC3339)
		for _, id := range req.InverterIDs {
			_, _ = a.db.Exec(`UPDATE simple_templates SET last_applied_inverter_id = ?, last_applied_utc = ?, last_applied_status = ? WHERE id = ?`,
				id, nowUTC, "saved_pending_scheduler", tid)
		}
	}
	a.recreateActiveSchedulerTask("simple schedule saved")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Расписание сохранено в приложении, задача планировщика пересоздана. Запись регистров выполнит планировщик; успех подтверждается контрольным чтением в истории. Это не то же самое, что «Сохранить шаблон».", "operation_id": opID})
}

// renewRecurringSchedules regenerates auto-renewed simplified schedules when a
// new month starts. Schedules edited afterwards in the advanced editor are left
// untouched and the skip is recorded in history.
func (a *App) renewRecurringSchedules(now time.Time) {
	if a.db == nil {
		return
	}
	month := now.Format("2006-01")
	rows, err := a.db.Query(`SELECT inverter_id, rules_json FROM schedule_recurrences WHERE auto_renew = 1 AND last_generated_month <> ?`, month)
	if err != nil {
		return
	}
	type item struct {
		id   int64
		raw  string
	}
	items := []item{}
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.raw) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	changed := false
	for _, it := range items {
		var st recurrenceState
		if json.Unmarshal([]byte(it.raw), &st) != nil {
			continue
		}
		current := ""
		name := "Упрощённое расписание"
		if s, err := a.getScheduleByInverterID(it.id); err == nil {
			current = s.ScheduleJSON
			if s.Name != "" {
				name = s.Name
			}
		}
		if current != "" && scheduleHash(current) != st.GeneratedHash {
			_, _ = a.db.Exec(`UPDATE schedule_recurrences SET auto_renew = 0, updated_at = ? WHERE inverter_id = ?`, now.UTC().Format(time.RFC3339), it.id)
			a.recordHistory(HistoryEntry{OperationType: opJobUpdate, Status: histSkipped, InverterID: it.id, Initiator: "система (автопродление)",
				Message: "Автопродление отключено: расписание было изменено в расширенном режиме после генерации"})
			continue
		}
		req := st.Request
		req.Month = month
		req.ConfirmDestructive = true
		res, err := a.resolveSimpleSchedule(req, it.id, now)
		if err != nil || len(res.ValidationErrs) > 0 {
			msg := ""
			if err != nil {
				msg = err.Error()
			} else {
				msg = strings.Join(res.ValidationErrs, "; ")
			}
			a.recordHistory(HistoryEntry{OperationType: opJobUpdate, Status: histError, InverterID: it.id, Initiator: "система (автопродление)", Message: "Автопродление расписания не выполнено", Error: msg})
			continue
		}
		if err := a.saveScheduleForInverter(it.id, name, "list", res.ScheduleJSON); err != nil {
			continue
		}
		st.GeneratedHash = scheduleHash(res.ScheduleJSON)
		sb, _ := json.Marshal(st)
		_, _ = a.db.Exec(`UPDATE schedule_recurrences SET rules_json = ?, last_generated_month = ?, updated_at = ? WHERE inverter_id = ?`, string(sb), month, now.UTC().Format(time.RFC3339), it.id)
		a.recordHistory(HistoryEntry{OperationType: opJobUpdate, Status: histApplied, InverterID: it.id, Initiator: "система (автопродление)",
			Message: fmt.Sprintf("Повторяющееся расписание пересчитано на %s (%d ч.)", month, res.EnabledHours)})
		changed = true
	}
	if changed {
		a.recreateActiveSchedulerTask("recurring schedules renewed")
	}
}

// analyzeScheduleForSimpleMode reports whether an existing schedule can be
// shown in the simplified editor without losing information.
func analyzeScheduleForSimpleMode(raw string) map[string]any {
	out := map[string]any{"representable": true, "reasons": []string{}, "days": []map[string]any{}}
	reasons := []string{}
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		out["representable"] = false
		out["reasons"] = []string{"расписание не разобрано: " + err.Error()}
		return out
	}
	normalizeSchedulePayloadSellTimes(&payload)
	custom := 0
	days := []map[string]any{}
	for _, d := range payload.Days {
		custom += len(d.CustomSlots)
		type cell struct {
			active bool
			cfg    HourConfig
		}
		cells := make([]cell, 24*12)
		for h := 0; h < 24; h++ {
			hourCfg := d.Hours[h]
			for mi := 0; mi < 12; mi++ {
				minute := mi * 5
				idx := h*12 + mi
				if minute == 0 {
					cells[idx] = cell{active: hourCfg.Enabled, cfg: hourCfg}
					continue
				}
				si := slotIndex(h, minute)
				if si >= 0 && si < len(d.Slots) {
					slot := d.Slots[si]
					if minuteSlotDiffersFromHour(slot, hourCfg) {
						if slot.Enabled {
							cells[idx] = cell{active: true, cfg: minuteSlotToHourConfig(slot)}
						} else {
							cells[idx] = cell{active: false}
						}
						continue
					}
				}
				cells[idx] = cell{active: hourCfg.Enabled, cfg: hourCfg}
			}
		}
		intervals := []map[string]any{}
		for i := 0; i < len(cells); {
			if !cells[i].active {
				i++
				continue
			}
			start := i
			ref := cells[i].cfg
			for i < len(cells) && cells[i].active && sameHourValues(cells[i].cfg, ref) {
				i++
			}
			startMin := start * 5
			endMin := i * 5
			intervals = append(intervals, map[string]any{
				"start": startMin / 60, "end": (endMin + 59) / 60,
				"start_min": startMin, "end_min": endMin,
				"start_time": formatClockHM(startMin), "end_time": formatClockHM(endMin),
				"charge_mode": ref.ChargeMode, "charge_mode_label": chargeModeLabelFromValue(ref.ChargeMode),
				"power_w": ref.SellModeKW * 10, "soc": ref.SellModeBattCapacity, "grid_charge": ref.GridChargeEnabled,
				"export_w": ref.GridExportLimit * 10, "load_limit_mode": ref.LoadLimitMode, "priority_load": ref.PriorityLoad,
				"partial_hour": startMin%60 != 0 || endMin%60 != 0,
			})
		}
		if len(intervals) > 0 {
			days = append(days, map[string]any{"day": d.Day, "intervals": intervals})
		}
	}
	if custom > 0 {
		// 5-minute overrides are first-class in the simplified editor now; only
		// warn when they cannot be collapsed into contiguous intervals (already
		// handled by interval extraction). Keep a soft note for operators.
		reasons = append(reasons, fmt.Sprintf("есть 5-минутные переопределения (%d) — отображаются как интервалы с шагом 5 минут", custom))
	}
	mask := payload.UseTimerMask
	if mask != 0 && mask != 255 {
		reasons = append(reasons, fmt.Sprintf("маска Use Timer %d отличается от «все дни» (255)", mask))
	}
	// Custom-slot note alone should not block editing in simplified mode.
	filtered := []string{}
	for _, r := range reasons {
		if strings.Contains(r, "5-минутные переопределения") {
			continue
		}
		filtered = append(filtered, r)
	}
	out["representable"] = len(filtered) == 0
	out["reasons"] = filtered
	if custom > 0 {
		out["five_minute_note"] = fmt.Sprintf("5-минутные переопределения: %d", custom)
	}
	out["days"] = days
	out["use_timer_mask"] = mask
	return out
}

func sameHourValues(a, b HourConfig) bool {
	return a.ChargeMode == b.ChargeMode && a.SellModeKW == b.SellModeKW && a.SellModeBattCapacity == b.SellModeBattCapacity && a.GridExportLimit == b.GridExportLimit &&
		a.GridChargeEnabled == b.GridChargeEnabled && a.LoadLimitMode == b.LoadLimitMode && a.PriorityLoad == b.PriorityLoad && a.SolarExport == b.SolarExport
}

func (a *App) apiSimpleScheduleAnalyzeHandler(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("inverter_id"), 10, 64)
	s, err := a.getScheduleByInverterID(id)
	if err != nil || strings.TrimSpace(s.ScheduleJSON) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "exists": false})
		return
	}
	an := analyzeScheduleForSimpleMode(s.ScheduleJSON)
	var rec struct {
		raw   string
		auto  int
		month string
	}
	recurrence := map[string]any{}
	if a.db.QueryRow(`SELECT rules_json, auto_renew, last_generated_month FROM schedule_recurrences WHERE inverter_id = ?`, id).Scan(&rec.raw, &rec.auto, &rec.month) == nil {
		var st recurrenceState
		if json.Unmarshal([]byte(rec.raw), &st) == nil {
			recurrence = map[string]any{"auto_renew": rec.auto == 1, "month": rec.month, "rules": st.Request.Rules, "base_mode": st.Request.BaseMode,
				"modified_since": scheduleHash(s.ScheduleJSON) != st.GeneratedHash}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "exists": true, "name": s.Name, "enabled": s.IsEnabled, "analysis": an, "recurrence": recurrence})
}

type SimpleSchedulePageData struct {
	Title              string
	InverterID         int64
	InverterName       string
	InverterEndpoint   string
	InverterModelKey   string
	InverterModelName  string
	Templates          []models.ScheduleTemplate
	SchedulePowerMaxW  int
	GridExportMaxW     int
	ModelsJSON         string
	ChargeModesJSON    string
	DefaultMonth       string
	SimpleEnabled      bool
	ExportURL          string
	HasInverter        bool
}

func (a *App) simpleSchedulePageHandler(w http.ResponseWriter, r *http.Request) {
	data := SimpleSchedulePageData{
		Title:         "Упрощённый режим расписания",
		DefaultMonth:  time.Now().Format("2006-01"),
		SimpleEnabled: a.kvBool(kvFeatureSimpleScheduler),
	}
	if target, _, err := a.defaultModbusTarget(); err == nil {
		data.HasInverter = true
		data.InverterID = target.InverterID
		data.InverterName = target.InverterName
		data.InverterEndpoint = fmt.Sprintf("%s:%d", target.IP, target.Port)
		data.InverterModelKey = target.ModelKey
		data.InverterModelName = target.ModelName
		data.ExportURL = fmt.Sprintf("/schedules/export?ids=%d", target.InverterID)
		if m, err := findInverterModel(target.ModelKey); err == nil {
			data.SchedulePowerMaxW = m.SchedulePowerMaxW
			data.GridExportMaxW = m.GridExportMaxW
			data.InverterModelName = m.Name
		}
	}
	if tpls, err := a.getTemplateOptions(); err == nil {
		data.Templates = tpls
	}
	modelsList, _ := availableInverterModels()
	type modelLimits struct {
		Key        string             `json:"key"`
		Name       string             `json:"name"`
		PowerMaxW  int                `json:"power_max_w"`
		ExportMaxW int                `json:"export_max_w"`
		ChargeModes []ChargeModeOption `json:"charge_modes"`
	}
	ml := []modelLimits{}
	for _, m := range modelsList {
		ml = append(ml, modelLimits{Key: m.Key, Name: m.Name, PowerMaxW: m.SchedulePowerMaxW, ExportMaxW: m.GridExportMaxW, ChargeModes: chargeModeOptionsForModels([]InverterModelDefinition{m})})
	}
	mb, _ := json.Marshal(ml)
	data.ModelsJSON = string(mb)
	cb, _ := json.Marshal(defaultChargeModeOptions())
	data.ChargeModesJSON = string(cb)
	if err := a.tmplSimpleSchedule.Execute(w, data); err != nil {
		a.appendAppLog("error", "simple schedule template error", map[string]any{"error": err.Error()})
	}
}

// apiSimpleScheduleExtractRulesHandler converts a full schedule_json into
// simplified intervals (from the first populated day) without writing anything.
func (a *App) apiSimpleScheduleExtractRulesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	var req struct {
		ScheduleJSON string `json:"schedule_json"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
		return
	}
	an := analyzeScheduleForSimpleMode(req.ScheduleJSON)
	rules := extractSimpleRulesFromAnalysis(an)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "rules": rules, "analysis": an,
		"advanced_preserved": !boolFromAny(an["representable"]),
		"message":            "Интервалы извлечены для упрощённого редактора. Параметры расширенного режима сохраняются при экспорте исходного JSON.",
	})
}

func extractSimpleRulesFromAnalysis(an map[string]any) []SimpleRule {
	dayMaps := []map[string]any{}
	switch days := an["days"].(type) {
	case []map[string]any:
		dayMaps = days
	case []any:
		for _, item := range days {
			if m, ok := item.(map[string]any); ok {
				dayMaps = append(dayMaps, m)
			}
		}
	}
	if len(dayMaps) == 0 {
		return nil
	}
	var intervalMaps []map[string]any
	for _, d := range dayMaps {
		switch iv := d["intervals"].(type) {
		case []map[string]any:
			if len(iv) > 0 {
				intervalMaps = iv
			}
		case []any:
			for _, item := range iv {
				if m, ok := item.(map[string]any); ok {
					intervalMaps = append(intervalMaps, m)
				}
			}
		}
		if len(intervalMaps) > 0 {
			break
		}
	}
	out := []SimpleRule{}
	for _, m := range intervalMaps {
		startMin, okStart := intFromAny(m["start_min"])
		endMin, okEnd := intFromAny(m["end_min"])
		if !okStart || !okEnd {
			startH, _ := intFromAny(m["start"])
			endH, _ := intFromAny(m["end"])
			startMin, endMin = startH*60, endH*60
		}
		if endMin <= startMin {
			continue
		}
		gc := false
		switch v := m["grid_charge"].(type) {
		case bool:
			gc = v
		}
		pw, _ := intFromAny(m["power_w"])
		soc, _ := intFromAny(m["soc"])
		ll, _ := intFromAny(m["load_limit_mode"])
		pl, _ := intFromAny(m["priority_load"])
		name, _ := m["charge_mode_label"].(string)
		if st, ok := m["start_time"].(string); ok && strings.TrimSpace(st) != "" {
			if em, ok2 := m["end_time"].(string); ok2 && strings.TrimSpace(em) != "" {
				r := SimpleRule{
					Name: name, Mode: "custom", StartTime: st, EndTime: em,
					GridChargeEnabled: &gc, PowerW: pw, BatterySOC: soc,
					LoadLimitMode: ll, PriorityLoad: normalizePriorityLoadValue(pl),
				}
				_ = normalizeSimpleRuleTimes(&r)
				out = append(out, r)
				continue
			}
		}
		r := SimpleRule{
			Name: name, Mode: "custom",
			StartTime: formatClockHM(startMin), EndTime: formatClockHM(min(endMin, 24*60)),
			GridChargeEnabled: &gc, PowerW: pw, BatterySOC: soc,
			LoadLimitMode: ll, PriorityLoad: normalizePriorityLoadValue(pl),
		}
		_ = normalizeSimpleRuleTimes(&r)
		out = append(out, r)
	}
	return out
}

// apiSimpleScheduleExportXLSXHandler builds a legacy-compatible XLSX workbook
// from the current simplified rules (same columns as the advanced editor).
func (a *App) apiSimpleScheduleExportXLSXHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	req, err := decodeSimpleRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	var invID int64
	if len(req.InverterIDs) > 0 {
		invID = req.InverterIDs[0]
	} else if target, _, err := a.defaultModbusTarget(); err == nil {
		invID = target.InverterID
		req.InverterIDs = []int64{invID}
	}
	res, err := a.resolveSimpleSchedule(req, invID, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if len(res.ValidationErrs) > 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: strings.Join(res.ValidationErrs, "; ")})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Упрощённое расписание"
	}
	data, err := buildSchedulesXLSX([]scheduleExportSheet{{Name: name, JSON: res.ScheduleJSON}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка генерации XLSX"})
		return
	}
	fileName := "simple-schedule-" + time.Now().Format("20060102-150405") + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	_, _ = w.Write(data)
}
