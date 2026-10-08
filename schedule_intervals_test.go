package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseClockHM(t *testing.T) {
	m, err := parseClockHM("18:00", false)
	if err != nil || m != 18*60 {
		t.Fatalf("18:00 -> %d %v", m, err)
	}
	m, err = parseClockHM("24:00", true)
	if err != nil || m != 24*60 {
		t.Fatalf("24:00 -> %d %v", m, err)
	}
	if _, err := parseClockHM("24:00", false); err == nil {
		t.Fatal("24:00 without allow24 must fail")
	}
	if _, err := parseClockHM("06:07", false); err == nil {
		t.Fatal("non-5-minute step must fail")
	}
}

func TestNormalizeMidnightCrossing(t *testing.T) {
	r := SimpleRule{StartTime: "18:00", EndTime: "00:00"}
	if err := normalizeSimpleRuleTimes(&r); err != nil {
		t.Fatal(err)
	}
	if r.StartHour != 18 || r.EndHour != 0 {
		t.Fatalf("hours=%d/%d", r.StartHour, r.EndHour)
	}
	hours := r.hoursOn(time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local))
	if len(hours) != 6 { // 18..23
		t.Fatalf("hours=%v", hours)
	}
}

func TestNormalizeWrapMorning(t *testing.T) {
	r := SimpleRule{StartTime: "23:00", EndTime: "07:00"}
	if err := normalizeSimpleRuleTimes(&r); err != nil {
		t.Fatal(err)
	}
	d := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	hours := r.hoursOn(d)
	seen := map[int]bool{}
	for _, h := range hours {
		seen[h] = true
	}
	if !seen[23] {
		t.Fatalf("missing 23: %v", hours)
	}
	next := r.hoursOn(d.AddDate(0, 0, 1))
	seen2 := map[int]bool{}
	for _, h := range next {
		seen2[h] = true
	}
	for h := 0; h < 7; h++ {
		if !seen2[h] {
			t.Fatalf("next day missing morning hour %d: %v", h, next)
		}
	}
}

func TestZeroDurationRejected(t *testing.T) {
	r := SimpleRule{StartTime: "10:00", EndTime: "10:00"}
	if err := normalizeSimpleRuleTimes(&r); err == nil {
		t.Fatal("expected error")
	}
}

func TestOverlapAndHardwareLimit(t *testing.T) {
	gc := true
	rules := []SimpleRule{}
	for i := 0; i < 8; i++ {
		rules = append(rules, SimpleRule{
			StartTime: formatClockHM(i * 60), EndTime: formatClockHM((i + 1) * 60),
			GridChargeEnabled: &gc, LoadLimitMode: 0, PriorityLoad: 0,
			PowerW: i * 100, BatterySOC: 20, Mode: "custom",
		})
	}
	_, issues, slots := analyzeSimpleIntervals(rules, false)
	if slots != 8 {
		t.Fatalf("slots=%d", slots)
	}
	foundHW := false
	for _, iss := range issues {
		if iss.Kind == "hardware" {
			foundHW = true
		}
	}
	if !foundHW {
		t.Fatal("expected hardware warning")
	}
}

func TestOverlapDetection(t *testing.T) {
	gc := false
	rules := []SimpleRule{
		{StartTime: "00:00", EndTime: "08:00", GridChargeEnabled: &gc, Mode: "custom", PowerW: 0, BatterySOC: 10},
		{StartTime: "06:00", EndTime: "12:00", GridChargeEnabled: &gc, Mode: "custom", PowerW: 100, BatterySOC: 10},
	}
	_, issues, _ := analyzeSimpleIntervals(rules, false)
	found := false
	for _, iss := range issues {
		if iss.Kind == "overlap" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected overlap")
	}
}

func TestMergeAdjacentEquivalent(t *testing.T) {
	gc := true
	rules := []SimpleRule{
		{StartTime: "00:00", EndTime: "06:00", GridChargeEnabled: &gc, LoadLimitMode: 1, PriorityLoad: 0, PowerW: 500, BatterySOC: 30, Mode: "custom"},
		{StartTime: "06:00", EndTime: "12:00", GridChargeEnabled: &gc, LoadLimitMode: 1, PriorityLoad: 0, PowerW: 500, BatterySOC: 30, Mode: "custom"},
		{StartTime: "12:00", EndTime: "18:00", GridChargeEnabled: &gc, LoadLimitMode: 0, PriorityLoad: 0, PowerW: 500, BatterySOC: 30, Mode: "custom"},
	}
	out := mergeAdjacentEquivalentRules(rules)
	if len(out) != 2 {
		t.Fatalf("merged len=%d, want 2", len(out))
	}
	if out[0].StartTime != "00:00" || out[0].EndTime != "12:00" {
		t.Fatalf("first start/end=%s/%s", out[0].StartTime, out[0].EndTime)
	}
}

func TestValidatePrimaryFields(t *testing.T) {
	gc := true
	r := SimpleRule{StartTime: "00:00", EndTime: "06:00", GridChargeEnabled: &gc, LoadLimitMode: 2, PriorityLoad: 1, PowerW: 1000, BatterySOC: 40, Mode: "custom"}
	if err := validateSimpleRule(0, r); err != nil {
		t.Fatal(err)
	}
	r.LoadLimitMode = 9
	if err := validateSimpleRule(0, r); err == nil {
		t.Fatal("bad load limit must fail")
	}
}

func TestSimpleTemplateVersionRoundTrip(t *testing.T) {
	a := newTestApp(t)
	gc := false
	id, err := a.upsertSimpleTemplateVersioned(SimpleTemplate{
		Name: "Ночной заряд", Description: "тест",
		Rules: []SimpleRule{{StartTime: "23:00", EndTime: "07:00", GridChargeEnabled: &gc, Mode: "custom", PowerW: 0, BatterySOC: 40, LoadLimitMode: 0, PriorityLoad: 0}},
	}, "tester", "создание")
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.getSimpleTemplate(id)
	if err != nil || got.Version != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	got.Description = "обновлено"
	got.Rules[0].PowerW = 500
	if _, err := a.upsertSimpleTemplateVersioned(got, "tester", "правка мощности"); err != nil {
		t.Fatal(err)
	}
	got2, _ := a.getSimpleTemplate(id)
	if got2.Version != 2 {
		t.Fatalf("version=%d", got2.Version)
	}
	vers, err := a.listSimpleTemplateVersions(id)
	if err != nil || len(vers) < 2 {
		t.Fatalf("versions=%d err=%v", len(vers), err)
	}
	if err := a.restoreSimpleTemplateVersion(id, 1, "tester"); err != nil {
		t.Fatal(err)
	}
	got3, _ := a.getSimpleTemplate(id)
	if got3.Version != 3 {
		t.Fatalf("after restore version=%d", got3.Version)
	}
	if got3.Rules[0].PowerW != 0 {
		t.Fatalf("restored power=%d", got3.Rules[0].PowerW)
	}
}

func TestBuildSimpleScheduleUnlimitedIntervals(t *testing.T) {
	gc := true
	rules := make([]SimpleRule, 0, 10)
	for i := 0; i < 10; i++ {
		rules = append(rules, SimpleRule{
			StartTime: formatClockHM(i * 60), EndTime: formatClockHM((i + 1) * 60),
			GridChargeEnabled: &gc, Mode: "custom", PowerW: (i + 1) * 100, BatterySOC: 20,
		})
	}
	res, _, err := buildSimpleSchedule(rules, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), nil, 255)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ExceedsHardware || res.LogicalDaySlots != 10 {
		t.Fatalf("exceeds=%v slots=%d want exceeds with 10 distinct blocks", res.ExceedsHardware, res.LogicalDaySlots)
	}
	if res.EnabledHours <= 0 {
		t.Fatal("expected enabled hours")
	}
}

func TestExtractSimpleRulesFromBuiltSchedule(t *testing.T) {
	gc := true
	rules := []SimpleRule{{
		Name: "night", Mode: "custom",
		StartTime: "00:00", EndTime: "06:00",
		GridChargeEnabled: &gc, PowerW: 1000, BatterySOC: 40,
		LoadLimitMode: 1, PriorityLoad: 0,
	}}
	res, _, err := buildSimpleSchedule(rules, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), nil, 255)
	if err != nil {
		t.Fatal(err)
	}
	an := analyzeScheduleForSimpleMode(res.ScheduleJSON)
	got := extractSimpleRulesFromAnalysis(an)
	if len(got) == 0 {
		t.Fatal("expected extracted rules")
	}
	if got[0].StartHour != 0 || got[0].EndHour != 6 {
		t.Fatalf("hours %d-%d", got[0].StartHour, got[0].EndHour)
	}
	if got[0].PowerW != 1000 || got[0].BatterySOC != 40 {
		t.Fatalf("power/soc %d/%d", got[0].PowerW, got[0].BatterySOC)
	}
	if got[0].GridChargeEnabled == nil || !*got[0].GridChargeEnabled {
		t.Fatal("expected grid charge enabled")
	}
	if got[0].LoadLimitMode != 1 {
		t.Fatalf("load limit %d", got[0].LoadLimitMode)
	}
}

func TestSimpleSchedulePageLayoutMarkers(t *testing.T) {
	html := string(tmplSimpleScheduleHTML)
	for _, marker := range []string{
		"simple-schedule-page",
		"1. Шаблоны XLSX",
		"2. Период расписания",
		"3. Параметры работы",
		"4. Временные интервалы",
		"5. Предпросмотр расписания",
		"6. Сохранение",
		"ssUploadTemplateBtn",
		"ssDownloadXlsxBtn",
		"ssSaveTemplateBtn",
		"ssApplyTemplateBtn",
	} {
		if !strings.Contains(html, marker) {
			t.Fatalf("simple schedule page missing %q", marker)
		}
	}
	for _, banned := range []string{
		"ssInverterSelect",
		"multiple inverters",
		"Выберите инверторы",
	} {
		if strings.Contains(html, banned) {
			t.Fatalf("simple schedule page should not contain %q", banned)
		}
	}
}

func TestBuildSimpleScheduleFiveMinutePrecision(t *testing.T) {
	gc := true
	rules := []SimpleRule{{
		Mode: "custom", StartTime: "00:15", EndTime: "06:30",
		GridChargeEnabled: &gc, PowerW: 2000, BatterySOC: 35,
		LoadLimitMode: 1, PriorityLoad: 1,
	}}
	res, payload, err := buildSimpleSchedule(rules, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), nil, 255)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Days) == 0 || len(res.Days[0].Intervals) == 0 {
		t.Fatal("expected preview intervals")
	}
	iv := res.Days[0].Intervals[0]
	if iv.StartMin != 15 || iv.EndMin != 6*60+30 {
		t.Fatalf("preview minutes %d-%d", iv.StartMin, iv.EndMin)
	}
	day := payload.Days[0]
	if day.Hours[0].Enabled {
		t.Fatal("hour 0 baseline should stay disabled when interval starts at :15")
	}
	if !day.Hours[1].Enabled || day.Hours[1].SellModeKW != 200 {
		t.Fatalf("hour 1 full coverage: enabled=%v kw=%d", day.Hours[1].Enabled, day.Hours[1].SellModeKW)
	}
	if !day.Hours[6].Enabled {
		t.Fatal("hour 6 should be enabled for :00–:25")
	}
	foundStart := false
	foundHole := false
	for _, cs := range day.CustomSlots {
		if cs.Hour == 0 && cs.Minute == 15 && cs.Enabled && cs.SellModeKW == 200 {
			foundStart = true
		}
		if cs.Hour == 6 && cs.Minute == 30 && !cs.Enabled {
			foundHole = true
		}
	}
	if !foundStart {
		t.Fatal("expected custom slot at 00:15")
	}
	if !foundHole {
		t.Fatal("expected disabled hole at 06:30")
	}
	cfg, src, ok := getScheduleConfigForExecution(&day, 0, 15)
	if !ok || cfg.SellModeKW != 200 {
		t.Fatalf("exec 00:15 ok=%v kw=%d src=%s", ok, cfg.SellModeKW, src)
	}
	if _, _, ok := getScheduleConfigForExecution(&day, 0, 5); ok {
		t.Fatal("00:05 should not resolve to interval when hour disabled and no earlier candidate")
	}
	if _, _, ok := getScheduleConfigForExecution(&day, 6, 30); ok {
		t.Fatal("06:30 hole should skip execution")
	}
	cfg, _, ok = getScheduleConfigForExecution(&day, 6, 0)
	if !ok || cfg.SellModeKW != 200 {
		t.Fatalf("06:00 should use hour baseline kw=%d ok=%v", cfg.SellModeKW, ok)
	}
}

func TestExtractFiveMinuteRulesRoundTrip(t *testing.T) {
	gc := false
	rules := []SimpleRule{{
		Mode: "custom", StartTime: "08:05", EndTime: "09:20",
		GridChargeEnabled: &gc, PowerW: 500, BatterySOC: 50,
	}}
	res, _, err := buildSimpleSchedule(rules, time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local), nil, 255)
	if err != nil {
		t.Fatal(err)
	}
	an := analyzeScheduleForSimpleMode(res.ScheduleJSON)
	got := extractSimpleRulesFromAnalysis(an)
	if len(got) == 0 {
		t.Fatal("no rules extracted")
	}
	if got[0].StartTime != "08:05" || got[0].EndTime != "09:20" {
		t.Fatalf("got %s-%s", got[0].StartTime, got[0].EndTime)
	}
}

func TestSimpleScheduleRejectsOverlappingIntervals(t *testing.T) {
	gc := false
	rules := []SimpleRule{
		{StartTime: "00:00", EndTime: "06:00", Mode: "custom", GridChargeEnabled: &gc, BatterySOC: 20},
		{StartTime: "05:45", EndTime: "12:00", Mode: "custom", GridChargeEnabled: &gc, BatterySOC: 20},
	}
	_, _, err := buildSimpleSchedule(rules, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), nil, 255)
	if err == nil || !strings.Contains(err.Error(), "пересекаются") {
		t.Fatalf("expected overlap rejection, got %v", err)
	}
}

func TestSimpleSchedulePreservesFiveMinuteGapAsDisabled(t *testing.T) {
	gc := false
	rules := []SimpleRule{
		{StartTime: "00:00", EndTime: "05:00", Mode: "custom", GridChargeEnabled: &gc, BatterySOC: 20, PowerW: 1000},
		{StartTime: "05:45", EndTime: "12:00", Mode: "custom", GridChargeEnabled: &gc, BatterySOC: 20, PowerW: 1000},
	}
	_, schedule, err := buildSimpleSchedule(rules, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), nil, 255)
	if err != nil {
		t.Fatal(err)
	}
	day := schedule.Days[0]
	if day.Hours[5].Enabled {
		t.Fatal("gap hour baseline must remain disabled")
	}
	for _, slot := range day.CustomSlots {
		if slot.Hour == 5 && slot.Minute < 45 && (slot.Enabled || slot.SellModeKW != 0 || slot.GridChargeEnabled) {
			t.Fatalf("stale command in gap at 05:%02d: %+v", slot.Minute, slot)
		}
	}
}
