package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// hardwareTOUSlots is the number of native Time-Of-Use programs on supported
// Deye hybrids (registers sell_time_point_1…6). Application templates may hold
// more logical intervals; the existing software scheduler applies the hourly
// schedule and writes at most these six hardware slots per execution.
const hardwareTOUSlots = 6

// parseClockHM parses "HH:MM" or "H:MM" into minutes from midnight.
// Allow "24:00" only when allow24 is true (exclusive end of day).
func parseClockHM(raw string, allow24 bool) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("пустое время")
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("ожидается ЧЧ:ММ")
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, fmt.Errorf("некорректный час")
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, fmt.Errorf("некорректные минуты")
	}
	if m < 0 || m > 59 || m%5 != 0 {
		return 0, fmt.Errorf("минуты должны быть кратны 5 (0, 5, …, 55)")
	}
	if allow24 && h == 24 && m == 0 {
		return 24 * 60, nil
	}
	if h < 0 || h > 23 {
		return 0, fmt.Errorf("час должен быть 0–23")
	}
	return h*60 + m, nil
}

func formatClockHM(minutes int) string {
	if minutes >= 24*60 {
		return "24:00"
	}
	if minutes < 0 {
		minutes = 0
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// normalizeSimpleRuleTimes fills StartHour/EndHour from StartTime/EndTime when
// present, and synthesises StartTime/EndTime from hours for older payloads.
// Convention: start inclusive, end exclusive. End <= start means midnight wrap
// (e.g. 18:00–00:00 or 23:00–07:00).
func normalizeSimpleRuleTimes(r *SimpleRule) error {
	if r == nil {
		return nil
	}
	if strings.TrimSpace(r.StartTime) != "" || strings.TrimSpace(r.EndTime) != "" {
		if strings.TrimSpace(r.StartTime) == "" || strings.TrimSpace(r.EndTime) == "" {
			return fmt.Errorf("укажите и начало, и окончание интервала")
		}
		start, err := parseClockHM(r.StartTime, false)
		if err != nil {
			return fmt.Errorf("начало: %v", err)
		}
		end, err := parseClockHM(r.EndTime, true)
		if err != nil {
			return fmt.Errorf("окончание: %v", err)
		}
		if start == end {
			return fmt.Errorf("нулевая длительность интервала")
		}
		r.startMin, r.endMin = start, end
		r.StartHour = start / 60
		if end > start {
			// Ceil exclusive end to hour boundary for the hourly expansion model.
			r.EndHour = (end + 59) / 60
			if r.EndHour > 24 {
				r.EndHour = 24
			}
		} else {
			// Wrap: EndHour is exclusive morning bound (0 = ends at midnight).
			r.EndHour = (end + 59) / 60
			if end == 0 {
				r.EndHour = 0
			}
		}
		r.StartTime = formatClockHM(start)
		r.EndTime = formatClockHM(end)
		return nil
	}
	// Legacy hour fields.
	if r.StartHour < 0 || r.StartHour > 23 {
		return fmt.Errorf("час начала 0–23")
	}
	if r.EndHour < 0 || r.EndHour > 24 {
		return fmt.Errorf("час окончания 0–24")
	}
	if r.EndHour == r.StartHour && r.EndHour != 24 {
		return fmt.Errorf("нулевая длительность интервала")
	}
	r.startMin = r.StartHour * 60
	if r.EndHour == 24 {
		r.endMin = 24 * 60
		r.EndTime = "24:00"
	} else {
		r.endMin = r.EndHour * 60
		r.EndTime = formatClockHM(r.endMin)
	}
	r.StartTime = formatClockHM(r.startMin)
	return nil
}

type intervalSpan struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	StartMin  int    `json:"start_min"`
	EndMin    int    `json:"end_min"`
	Crosses   bool   `json:"crosses_midnight"`
	DurationM int    `json:"duration_min"`
}

func ruleSpan(i int, r SimpleRule) (intervalSpan, error) {
	cp := r
	if err := normalizeSimpleRuleTimes(&cp); err != nil {
		return intervalSpan{}, err
	}
	start, end := cp.startMin, cp.endMin
	cross := end <= start
	dur := end - start
	if cross {
		dur = (24*60 - start) + end
	}
	if dur <= 0 {
		return intervalSpan{}, fmt.Errorf("нулевая длительность")
	}
	return intervalSpan{Index: i, Name: r.Name, StartMin: start, EndMin: end, Crosses: cross, DurationM: dur}, nil
}

type intervalIssue struct {
	Kind    string `json:"kind"` // overlap | gap | empty | hardware
	Message string `json:"message"`
	RuleA   int    `json:"rule_a,omitempty"`
	RuleB   int    `json:"rule_b,omitempty"`
}

// analyzeSimpleIntervals reports overlaps, optional coverage gaps, and whether
// a typical day would exceed native TOU hardware slots.
func analyzeSimpleIntervals(rules []SimpleRule, requireContinuous bool) (spans []intervalSpan, issues []intervalIssue, maxDaySlots int) {
	type seg struct {
		start, end, idx int
	}
	var segs []seg
	for i, r := range rules {
		sp, err := ruleSpan(i, r)
		if err != nil {
			issues = append(issues, intervalIssue{Kind: "empty", Message: fmt.Sprintf("интервал %d: %v", i+1, err), RuleA: i + 1})
			continue
		}
		spans = append(spans, sp)
		if sp.Crosses {
			segs = append(segs, seg{sp.StartMin, 24 * 60, i}, seg{0, sp.EndMin, i})
		} else {
			segs = append(segs, seg{sp.StartMin, sp.EndMin, i})
		}
	}
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].start == segs[j].start {
			return segs[i].end < segs[j].end
		}
		return segs[i].start < segs[j].start
	})
	for i := 0; i+1 < len(segs); i++ {
		a, b := segs[i], segs[i+1]
		if a.idx == b.idx {
			continue
		}
		if b.start < a.end {
			issues = append(issues, intervalIssue{
				Kind:    "overlap",
				RuleA:   a.idx + 1,
				RuleB:   b.idx + 1,
				Message: fmt.Sprintf("интервалы %d и %d пересекаются (%s–%s и %s–%s); при сохранении действует интервал ниже по списку", a.idx+1, b.idx+1, formatClockHM(a.start), formatClockHM(a.end), formatClockHM(b.start), formatClockHM(b.end)),
			})
		} else if requireContinuous && b.start > a.end {
			issues = append(issues, intervalIssue{
				Kind:    "gap",
				RuleA:   a.idx + 1,
				RuleB:   b.idx + 1,
				Message: fmt.Sprintf("разрыв между интервалами %d и %d: %s–%s без настроек", a.idx+1, b.idx+1, formatClockHM(a.end), formatClockHM(b.start)),
			})
		}
	}
	// Count distinct contiguous parameter groups for a "every day" projection.
	maxDaySlots = countDistinctDailyIntervals(rules)
	if maxDaySlots > hardwareTOUSlots {
		issues = append(issues, intervalIssue{
			Kind: "hardware",
			Message: fmt.Sprintf(
				"на один день приходится %d различных интервалов, а у инвертора только %d аппаратных программ TOU (регистры 148–153). Приложение не обрезает интервалы: расписание сохраняется полностью, а существующий программный планировщик применяет почасовые значения и при каждом запуске записывает не более %d слотов. Для прямой записи всех интервалов в аппаратные слоты сократите число различных блоков до %d (можно объединить соседние с одинаковыми параметрами).",
				maxDaySlots, hardwareTOUSlots, hardwareTOUSlots, hardwareTOUSlots,
			),
		})
	}
	return spans, issues, maxDaySlots
}

func countDistinctDailyIntervals(rules []SimpleRule) int {
	// Simulate one generic weekday with all rules that have empty weekdays or include Monday.
	type key struct {
		gc, llm, pl, cm, pw, soc int
	}
	owner := map[int]key{}
	for _, r := range rules {
		cp := r
		_ = normalizeSimpleRuleTimes(&cp)
		if len(r.Weekdays) > 0 {
			ok := false
			for _, w := range r.Weekdays {
				if w == 1 {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		cfg := cp.hourConfig(0, 255)
		k := key{
			gc: boolToInt(cfg.GridChargeEnabled), llm: cfg.LoadLimitMode, pl: cfg.PriorityLoad,
			cm: cfg.ChargeMode, pw: cfg.SellModeKW, soc: cfg.SellModeBattCapacity,
		}
		hours := []int{}
		if cp.endMin > cp.startMin {
			for h := cp.StartHour; h < cp.EndHour && h < 24; h++ {
				hours = append(hours, h)
			}
		} else {
			for h := cp.StartHour; h < 24; h++ {
				hours = append(hours, h)
			}
			for h := 0; h < cp.EndHour; h++ {
				hours = append(hours, h)
			}
		}
		for _, h := range hours {
			owner[h] = k
		}
	}
	if len(owner) == 0 {
		return 0
	}
	count := 0
	var prev key
	havePrev := false
	for h := 0; h < 24; h++ {
		k, ok := owner[h]
		if !ok {
			havePrev = false
			continue
		}
		if !havePrev || prev != k {
			count++
			prev = k
			havePrev = true
		}
	}
	return count
}

func mergeAdjacentEquivalentRules(rules []SimpleRule) []SimpleRule {
	if len(rules) < 2 {
		return rules
	}
	out := []SimpleRule{}
	for _, r := range rules {
		cp := r
		_ = normalizeSimpleRuleTimes(&cp)
		if len(out) == 0 {
			out = append(out, cp)
			continue
		}
		prev := &out[len(out)-1]
		same := prev.Mode == cp.Mode &&
			((prev.GridChargeEnabled == nil && cp.GridChargeEnabled == nil) || (prev.GridChargeEnabled != nil && cp.GridChargeEnabled != nil && *prev.GridChargeEnabled == *cp.GridChargeEnabled)) &&
			prev.LoadLimitMode == cp.LoadLimitMode &&
			prev.PriorityLoad == cp.PriorityLoad &&
			prev.PowerW == cp.PowerW &&
			prev.BatterySOC == cp.BatterySOC &&
			prev.GridExportLimitW == cp.GridExportLimitW &&
			sameIntSlice(prev.Weekdays, cp.Weekdays) &&
			prev.DateFrom == cp.DateFrom && prev.DateTo == cp.DateTo &&
			!prevWraps(*prev) && !prevWraps(cp) &&
			prev.endMin == cp.startMin
		if same {
			prev.EndTime = cp.EndTime
			prev.EndHour = cp.EndHour
			prev.endMin = cp.endMin
			continue
		}
		out = append(out, cp)
	}
	return out
}

func prevWraps(r SimpleRule) bool {
	_ = normalizeSimpleRuleTimes(&r)
	return r.endMin <= r.startMin
}

func sameIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]int{}, a...)
	bb := append([]int{}, b...)
	sort.Ints(aa)
	sort.Ints(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
