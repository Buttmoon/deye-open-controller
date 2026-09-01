package main

import (
	"encoding/json"
	"testing"
)

func TestExtendedChargeModes(t *testing.T) {
	cases := []struct {
		raw  any
		want int
	}{
		{"Sell", 32},
		{"Sell & Grid", 33},
		{"Sell & Gen", 34},
		{"Sell & Grid & Gen", 35},
		{32, 32},
		{33, 33},
		{34, 34},
		{35, 35},
	}
	for _, tc := range cases {
		got, ok := chargeModeFromAny(tc.raw)
		if !ok || got != tc.want {
			t.Fatalf("chargeModeFromAny(%v) = %d, %v; want %d, true", tc.raw, got, ok, tc.want)
		}
	}
}

func TestExtendedChargeModeSchedulerValue(t *testing.T) {
	payload := defaultSchedulePayload()
	payload.Days[0].Hours[4].ChargeMode = 35
	normalizeSchedulePayloadSellTimes(&payload)
	if got := payload.Days[0].Hours[4].ChargeMode; got != 35 {
		t.Fatalf("normalized charge mode = %d; want 35", got)
	}

	values := scheduleRegisterValues(payload.Days[0].Hours[4])
	for _, v := range values {
		if v.Code == "charge_mode_point_1" {
			if v.Value != 35 {
				t.Fatalf("scheduler charge_mode_point_1 value = %d; want 35", v.Value)
			}
			return
		}
	}
	t.Fatal("charge_mode_point_1 was not generated")
}

func TestChargeModeFromFlagsMapping(t *testing.T) {
	cases := []struct {
		sell bool
		grid bool
		gen  bool
		want int
	}{
		{false, false, false, 0},
		{false, true, false, 1},
		{false, false, true, 2},
		{false, true, true, 3},
		{true, false, false, 32},
		{true, true, false, 33},
		{true, false, true, 34},
		{true, true, true, 35},
	}
	for _, tc := range cases {
		if got := chargeModeFromFlags(tc.sell, tc.grid, tc.gen); got != tc.want {
			t.Fatalf("chargeModeFromFlags(sell=%v grid=%v gen=%v) = %d; want %d", tc.sell, tc.grid, tc.gen, got, tc.want)
		}
	}
}

func TestExtendedChargeModesAllowedWithoutDeviceParameterDependency(t *testing.T) {
	for _, value := range []int{0, 1, 2, 3, 32, 33, 34, 35} {
		if !isConfiguredChargeModeValue(value) {
			t.Fatalf("charge mode value %d must be allowed", value)
		}
	}
}

func TestPriorityLoadNumericMapping(t *testing.T) {
	cases := []struct {
		raw  any
		want int
	}{
		{false, 0},
		{true, 1},
		{0, 0},
		{1, 1},
		{7, 1},
		{"load first", 0},
		{"battery first", 1},
	}
	for _, tc := range cases {
		if got := priorityLoadValueFromAny(tc.raw); got != tc.want {
			t.Fatalf("priorityLoadValueFromAny(%v) = %d; want %d", tc.raw, got, tc.want)
		}
	}
}

func TestPriorityLoadSchedulerValueIsNumeric(t *testing.T) {
	cfg := defaultHourConfig(12)
	cfg.PriorityLoad = 1
	values := scheduleRegisterValues(cfg)
	for _, v := range values {
		if v.Code == "priority_load" {
			if v.Value != 1 {
				t.Fatalf("priority_load register value = %d; want 1", v.Value)
			}
			return
		}
	}
	t.Fatal("priority_load was not generated")
}

func TestDefaultCustomSlotDoesNotDifferFromHour(t *testing.T) {
	h := defaultHourConfig(10)
	h.Enabled = true
	h.SellModeKW = 50
	s := minuteSlotFromHourConfig(h, 5)
	s.SellModeKW = 70
	s.DefaultCustomSlot = true

	if minuteSlotDiffersFromHour(s, h) {
		t.Fatal("default custom slot must be treated as empty/inherited and not differ from hour")
	}
}

func TestCompactScheduleDropsDefaultCustomSlots(t *testing.T) {
	payload := defaultSchedulePayload()
	payload.UseTimerMask = 255
	day := &payload.Days[0]
	day.Enabled = true
	day.Hours[8].Enabled = true
	day.Hours[8].SellModeKW = 10
	idx := slotIndex(8, 5)
	day.Slots[idx] = minuteSlotFromHourConfig(day.Hours[8], 5)
	day.Slots[idx].SellModeKW = 99
	day.Slots[idx].DefaultCustomSlot = true
	day.CustomSlots = []MinuteSlot{day.Slots[idx]}

	compact := compactSchedulePayloadForEditor(payload)
	if len(compact.Days[0].CustomSlots) != 0 {
		t.Fatalf("default custom slots must be removed from compact schedule, got %d", len(compact.Days[0].CustomSlots))
	}
}

func configuredTestDay(dayNumber int) DayItem {
	day := defaultDayItem(dayNumber)
	day.Enabled = true
	for i := range day.Hours {
		day.Hours[i].Enabled = true
		day.Hours[i].SellModeKW = (i + 1) * 100
		day.Hours[i].SellModeBattCapacity = 100 - i
		day.Hours[i].ChargeMode = i % 4
		day.Hours[i].GridExportLimit = 1000 + i
		for minute := 0; minute < 60; minute += 5 {
			idx := slotIndex(i, minute)
			day.Slots[idx] = minuteSlotFromHourConfig(day.Hours[i], minute)
		}
	}
	return day
}

func pointSellTimes(points map[int]HourConfig) []int {
	out := make([]int, 0, 6)
	for point := 1; point <= 6; point++ {
		out = append(out, points[point].SellTime)
	}
	return out
}

func assertPointSellTimes(t *testing.T, got map[int]HourConfig, want []int) {
	t.Helper()
	actual := pointSellTimes(got)
	if len(actual) != len(want) {
		t.Fatalf("point count = %d; want %d; values=%v", len(actual), len(want), actual)
	}
	for i := range want {
		if actual[i] != want[i] {
			t.Fatalf("point sell times = %v; want %v", actual, want)
		}
	}
}

func TestBuildPointConfigsCurrentInMiddleWithoutDayWrap(t *testing.T) {
	day := configuredTestDay(6)
	points := buildPointConfigs(&day, day.Hours[16])
	assertPointSellTimes(t, points, []int{1400, 1500, 1600, 1700, 1800, 1900})
}

func TestBuildPointConfigsStartOfDayDoesNotUsePreviousDayTail(t *testing.T) {
	day := configuredTestDay(6)
	points := buildPointConfigs(&day, day.Hours[0])
	assertPointSellTimes(t, points, []int{0, 100, 200, 300, 400, 500})
}

func TestBuildPointConfigsEndOfDayDoesNotUseNextDayHead(t *testing.T) {
	day := configuredTestDay(6)
	points := buildPointConfigs(&day, day.Hours[23])
	assertPointSellTimes(t, points, []int{1800, 1900, 2000, 2100, 2200, 2300})
}

func TestBuildPointConfigsUsesCurrentCustomSlotBetweenSameDayPoints(t *testing.T) {
	day := configuredTestDay(6)
	idx := slotIndex(16, 30)
	day.Slots[idx] = minuteSlotFromHourConfig(day.Hours[16], 30)
	day.Slots[idx].Enabled = true
	day.Slots[idx].SellModeKW = 9999
	current := minuteSlotToHourConfig(day.Slots[idx])
	points := buildPointConfigs(&day, current)
	assertPointSellTimes(t, points, []int{1400, 1500, 1630, 1700, 1800, 1900})
}

func writableTestParam(code string, address uint16) DeviceParameterFields {
	return DeviceParameterFields{
		Code:          code,
		ModbusAddress: address,
		RegisterType:  "holding",
		IsWritable:    true,
	}
}

func TestResolveScheduleWritePlanUsesDeviceParameterAddresses(t *testing.T) {
	values := []ScheduleRegisterValue{
		{Code: "use_timer", Value: 255},
		{Code: "sell_mode_kw_point_2", Value: 8000},
		{Code: "grid_export_limit", Value: 0},
		{Code: "sell_time_point_1", Value: 1900},
		{Code: "sell_time_point_2", Value: 2000},
		{Code: "sell_mode_kw_point_1", Value: 0},
	}
	params := map[string]DeviceParameterFields{
		"use_timer":            writableTestParam("use_timer", 146),
		"grid_export_limit":    writableTestParam("grid_export_limit", 143),
		"sell_time_point_1":    writableTestParam("sell_time_point_1", 148),
		"sell_time_point_2":    writableTestParam("sell_time_point_2", 149),
		"sell_mode_kw_point_1": writableTestParam("sell_mode_kw_point_1", 154),
		"sell_mode_kw_point_2": writableTestParam("sell_mode_kw_point_2", 155),
	}

	plan, skipped := resolveScheduleWritePlan(values, params)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %d; want 0", len(skipped))
	}
	gotCodes := make([]string, 0, len(plan))
	gotAddresses := make([]uint16, 0, len(plan))
	for _, item := range plan {
		gotCodes = append(gotCodes, item.Code)
		gotAddresses = append(gotAddresses, item.Address)
	}
	wantCodes := []string{"grid_export_limit", "sell_time_point_1", "sell_mode_kw_point_1", "sell_time_point_2", "sell_mode_kw_point_2", "use_timer"}
	wantAddresses := []uint16{143, 148, 154, 149, 155, 146}
	for i := range wantCodes {
		if gotCodes[i] != wantCodes[i] || gotAddresses[i] != wantAddresses[i] {
			t.Fatalf("plan[%d] = %s/%d; want %s/%d; full codes=%v addresses=%v", i, gotCodes[i], gotAddresses[i], wantCodes[i], wantAddresses[i], gotCodes, gotAddresses)
		}
	}
}

func TestResolveScheduleWritePlanWritesPointFieldsTogetherAndChargeLast(t *testing.T) {
	values := []ScheduleRegisterValue{
		{Code: "charge_mode_point_2", Value: 33},
		{Code: "sell_mode_batt_capacity_1", Value: 11},
		{Code: "sell_time_point_2", Value: 2000},
		{Code: "sell_mode_kw_point_1", Value: 8000},
		{Code: "sell_time_point_1", Value: 1900},
		{Code: "charge_mode_point_1", Value: 0},
		{Code: "sell_mode_kw_point_2", Value: 0},
		{Code: "sell_mode_batt_capacity_2", Value: 11},
	}
	params := map[string]DeviceParameterFields{
		"sell_time_point_1":         writableTestParam("sell_time_point_1", 148),
		"sell_time_point_2":         writableTestParam("sell_time_point_2", 149),
		"sell_mode_kw_point_1":      writableTestParam("sell_mode_kw_point_1", 154),
		"sell_mode_kw_point_2":      writableTestParam("sell_mode_kw_point_2", 155),
		"sell_mode_batt_capacity_1": writableTestParam("sell_mode_batt_capacity_1", 166),
		"sell_mode_batt_capacity_2": writableTestParam("sell_mode_batt_capacity_2", 167),
		"charge_mode_point_1":       writableTestParam("charge_mode_point_1", 172),
		"charge_mode_point_2":       writableTestParam("charge_mode_point_2", 173),
	}
	plan, skipped := resolveScheduleWritePlan(values, params)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %d; want 0", len(skipped))
	}
	got := make([]string, 0, len(plan))
	for _, item := range plan {
		got = append(got, item.Code)
	}
	want := []string{
		"sell_time_point_1",
		"sell_mode_kw_point_1",
		"sell_mode_batt_capacity_1",
		"charge_mode_point_1",
		"sell_time_point_2",
		"sell_mode_kw_point_2",
		"sell_mode_batt_capacity_2",
		"charge_mode_point_2",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("plan order = %v; want %v", got, want)
		}
	}
}

func TestGroupScheduleWritePlanByContiguousDeviceAddressesAndPhase(t *testing.T) {
	plan := []scheduleResolvedWrite{
		{Code: "grid_export_limit", Address: 143, Phase: "global_settings"},
		{Code: "sell_time_point_1", Address: 148, Phase: "schedule_points"},
		{Code: "sell_time_point_2", Address: 149, Phase: "schedule_points"},
		{Code: "sell_mode_kw_point_1", Address: 154, Phase: "schedule_points"},
		{Code: "sell_mode_kw_point_2", Address: 155, Phase: "schedule_points"},
		{Code: "use_timer", Address: 146, Phase: "apply_timer_last"},
	}
	batches := groupScheduleWritePlan(plan)
	if len(batches) != 4 {
		t.Fatalf("batch count = %d; want 4", len(batches))
	}
	if batches[1].Start != 148 || len(batches[1].Items) != 2 {
		t.Fatalf("time batch = start %d count %d; want start 148 count 2", batches[1].Start, len(batches[1].Items))
	}
	if batches[2].Start != 154 || len(batches[2].Items) != 2 {
		t.Fatalf("kw batch = start %d count %d; want start 154 count 2", batches[2].Start, len(batches[2].Items))
	}
	if batches[3].Start != 146 || batches[3].Items[0].Code != "use_timer" {
		t.Fatalf("last batch = start %d code %s; want use_timer at 146", batches[3].Start, batches[3].Items[0].Code)
	}
}

func TestScheduleTimerMustNotBeAppliedAfterAnyWriteFailure(t *testing.T) {
	if scheduleWriteResultsHaveFailure([]ScheduleWriteResult{{Status: "ok"}, {Status: "error"}}) != true {
		t.Fatal("error must prevent final use_timer application")
	}
	if scheduleWriteResultsHaveFailure([]ScheduleWriteResult{{Status: "ok"}, {Status: "skipped"}}) != true {
		t.Fatal("skipped register must prevent final use_timer application")
	}
	if scheduleWriteResultsHaveFailure([]ScheduleWriteResult{{Status: "ok"}, {Status: "ok"}}) {
		t.Fatal("all successful writes must allow final use_timer application")
	}
}

func TestSixProgramPlanUsesSelectedProfileBoundsAndAddresses(t *testing.T) {
	models, err := availableInverterModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		_, params, err := loadDeviceParametersForModel(model.Key)
		if err != nil {
			t.Fatalf("%s: %v", model.Key, err)
		}
		byCode := deviceParametersByCode(params)
		values := make([]ScheduleRegisterValue, 0, 24)
		for point := 1; point <= 6; point++ {
			values = append(values,
				ScheduleRegisterValue{Code: "sell_time_point_" + string(rune('0'+point)), Value: (point - 1) * 400},
				ScheduleRegisterValue{Code: "sell_mode_kw_point_" + string(rune('0'+point)), Value: model.SchedulePowerMaxW / 10},
				ScheduleRegisterValue{Code: "sell_mode_batt_capacity_" + string(rune('0'+point)), Value: 50},
				ScheduleRegisterValue{Code: "charge_mode_point_" + string(rune('0'+point)), Value: 3},
			)
		}
		plan, skipped := resolveScheduleWritePlan(values, byCode)
		if len(skipped) != 0 {
			t.Fatalf("%s: schedule plan skipped=%v", model.Key, skipped)
		}
		if len(plan) != 24 {
			t.Fatalf("%s: plan length=%d want=24", model.Key, len(plan))
		}
		for point := 1; point <= 6; point++ {
			powerCode := "sell_mode_kw_point_" + string(rune('0'+point))
			found := false
			for _, item := range plan {
				if item.Code == powerCode {
					found = true
					if item.Address != uint16(153+point) || item.Value != uint16(model.SchedulePowerMaxW/10) {
						t.Fatalf("%s/%s: address=%d raw=%d", model.Key, powerCode, item.Address, item.Value)
					}
				}
			}
			if !found {
				t.Fatalf("%s: %s not found", model.Key, powerCode)
			}
		}
	}
}

func TestTemplateValidationUsesStrictestSelectedModel(t *testing.T) {
	v104, err := findInverterModel("deye_sun_25k_sg01hp3_eu_am2_v104")
	if err != nil {
		t.Fatal(err)
	}
	v105, err := findInverterModel("deye_sun_30k_sg02hp3_eu_am3_v1054")
	if err != nil {
		t.Fatal(err)
	}
	payload := defaultSchedulePayload()
	payload.Days[0].Enabled = true
	payload.Days[0].Hours[0].Enabled = true
	payload.Days[0].Hours[0].SellModeKW = 2500
	payload.Days[0].Hours[0].GridExportLimit = 2500
	payload.Days[0].Hours[0].ChargeMode = 3
	raw, _ := json.Marshal(payload)
	if err := validateScheduleJSONForModels(string(raw), []InverterModelDefinition{v104, v105}); err != nil {
		t.Fatalf("strictest compatible values rejected: %v", err)
	}
	payload.Days[0].Hours[0].SellModeKW = 2501
	raw, _ = json.Marshal(payload)
	if err := validateScheduleJSONForModels(string(raw), []InverterModelDefinition{v104, v105}); err == nil {
		t.Fatal("power above 25 kW must be rejected when template targets 25 and 30 kW models")
	}
	payload.Days[0].Hours[0].SellModeKW = 2500
	payload.Days[0].Hours[0].ChargeMode = 32
	raw, _ = json.Marshal(payload)
	if err := validateScheduleJSONForModels(string(raw), []InverterModelDefinition{v104, v105}); err == nil {
		t.Fatal("Sell bit must be rejected when one selected profile is V104")
	}
}
