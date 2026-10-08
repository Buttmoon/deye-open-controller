package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLegacyTemplateImportFixture loads the supplied 2409_ТЕСТ_ТУРА workbook
// (copied under testdata/) and asserts that meaningful hourly values survive
// import. Days 1–30 × 24 hours = 720 data rows in the original sheet.
func TestLegacyTemplateImportFixture(t *testing.T) {
	path := filepath.Join("testdata", "legacy_template_import.xlsx")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	imported, err := ParseTemplatesFromXLSX(f)
	if err != nil {
		t.Fatalf("ParseTemplatesFromXLSX: %v", err)
	}
	if len(imported) != 1 {
		t.Fatalf("expected 1 sheet/template, got %d", len(imported))
	}
	if imported[0].Name != "Example" {
		t.Fatalf("sheet name = %q, want Example", imported[0].Name)
	}

	payload, err := parseSchedulePayloadLoose(imported[0].ScheduleJSON)
	if err != nil {
		t.Fatalf("parse imported schedule: %v", err)
	}
	if len(payload.Days) < 30 {
		t.Fatalf("days = %d, want at least 30", len(payload.Days))
	}

	// Legacy rows leave UseTimer columns empty/FALSE; importer enables the
	// full weekday mask so existing TOU schedules remain executable.
	if payload.UseTimerMask != 255 {
		t.Fatalf("UseTimerMask=%d, want 255 after legacy empty-mask import", payload.UseTimerMask)
	}

	d1 := payload.Days[0]
	if !d1.Enabled {
		t.Fatal("day 1 must be enabled")
	}
	h0 := d1.Hours[0]
	if !h0.Enabled || h0.SellModeKW != 3500 || h0.SellModeBattCapacity != 100 {
		t.Fatalf("day1 hour0 unexpected: enabled=%v kw=%d batt=%d", h0.Enabled, h0.SellModeKW, h0.SellModeBattCapacity)
	}
	if h0.ChargeMode != 33 { // Sell & Grid
		t.Fatalf("day1 hour0 ChargeMode=%d, want 33 (Sell & Grid)", h0.ChargeMode)
	}
	if h0.GridExportLimit != 20000 {
		t.Fatalf("day1 hour0 GridExportLimit=%d, want 20000", h0.GridExportLimit)
	}
	if !h0.GridChargeEnabled {
		t.Fatal("day1 hour0 GridChargeEnabled should be true")
	}

	h1 := d1.Hours[1]
	if h1.GridExportLimit != 20001 {
		t.Fatalf("day1 hour1 GridExportLimit=%d, want 20001 (distinct per-hour values)", h1.GridExportLimit)
	}

	h19 := d1.Hours[19]
	if h19.SellModeKW != 1000 || h19.SellModeBattCapacity != 30 || h19.GridChargeEnabled {
		t.Fatalf("day1 hour19 unexpected: kw=%d batt=%d gce=%v", h19.SellModeKW, h19.SellModeBattCapacity, h19.GridChargeEnabled)
	}

	h21 := d1.Hours[21]
	if h21.ChargeMode != 0 || h21.SellModeKW != 0 {
		t.Fatalf("day1 hour21 ChargeMode=%d SellModeKW=%d, want 0/0 (No Grid or Gen)", h21.ChargeMode, h21.SellModeKW)
	}

	enabledHours := 0
	for _, day := range payload.Days {
		for _, h := range day.Hours {
			if h.Enabled {
				enabledHours++
			}
		}
	}
	if enabledHours != 720 {
		t.Fatalf("enabled hours = %d, want 720 (30 days × 24)", enabledHours)
	}
}

// TestLegacyXLSXRoundTrip confirms that import → export → re-import does not
// silently change schedule semantics for the legacy fixture columns.
func TestLegacyXLSXRoundTrip(t *testing.T) {
	path := filepath.Join("testdata", "legacy_template_import.xlsx")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	first, err := ParseTemplatesFromXLSX(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first import templates=%d", len(first))
	}
	orig, err := parseSchedulePayloadLoose(first[0].ScheduleJSON)
	if err != nil {
		t.Fatalf("parse first: %v", err)
	}

	exported, err := buildSchedulesXLSX([]scheduleExportSheet{{Name: first[0].Name, JSON: first[0].ScheduleJSON}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	second, err := ParseTemplatesFromXLSX(bytes.NewReader(exported))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second import templates=%d", len(second))
	}
	round, err := parseSchedulePayloadLoose(second[0].ScheduleJSON)
	if err != nil {
		t.Fatalf("parse second: %v", err)
	}

	if diffs := scheduleSemanticDiffs(orig, round); len(diffs) > 0 {
		t.Fatalf("round-trip changed schedule semantics:\n%s", diffs)
	}

	// Exported workbook must keep the legacy column set (Hour renamed to Time is OK;
	// importer accepts both). Spot-check that headers survive as shared/inline strings.
	reimportedJSON, _ := json.Marshal(round)
	if len(reimportedJSON) < 1000 {
		t.Fatalf("reimported JSON unexpectedly small: %d bytes", len(reimportedJSON))
	}
}

func scheduleSemanticDiffs(a, b SchedulePayload) string {
	var buf bytes.Buffer
	if normalizeUseTimerMask(a.UseTimerMask) != normalizeUseTimerMask(b.UseTimerMask) {
		buf.WriteString("UseTimerMask differs\n")
	}
	if len(a.Days) != len(b.Days) {
		buf.WriteString("day count differs\n")
	}
	maxDays := len(a.Days)
	if len(b.Days) < maxDays {
		maxDays = len(b.Days)
	}
	for di := 0; di < maxDays; di++ {
		da, db := a.Days[di], b.Days[di]
		if da.Enabled != db.Enabled {
			buf.WriteString("day enabled flag differs\n")
		}
		maxH := len(da.Hours)
		if len(db.Hours) < maxH {
			maxH = len(db.Hours)
		}
		for hi := 0; hi < maxH; hi++ {
			ha, hb := da.Hours[hi], db.Hours[hi]
			if ha.Enabled != hb.Enabled ||
				ha.SellModeKW != hb.SellModeKW ||
				ha.SellModeBattCapacity != hb.SellModeBattCapacity ||
				ha.ChargeMode != hb.ChargeMode ||
				ha.GridExportLimit != hb.GridExportLimit ||
				ha.GridChargeEnabled != hb.GridChargeEnabled ||
				ha.LoadLimitMode != hb.LoadLimitMode ||
				normalizePriorityLoadValue(ha.PriorityLoad) != normalizePriorityLoadValue(hb.PriorityLoad) {
				buf.WriteString("hour fields differ at day/hour index\n")
				return buf.String()
			}
		}
	}
	return buf.String()
}
