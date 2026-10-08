package main

import "testing"

func TestBuildInverterScreenSettingsUsesGridPeakRegisters(t *testing.T) {
	settings := buildInverterScreenSettings(map[string]any{
		"grid_export_limit":          float64(12340),
		"grid_peak_shaving_power":   float64(8400),
		"grid_peak_shaving_enabled": float64(0x30),
		"grid_charge_enable":         float64(0),
	})

	if settings.GridPeakValue != 8400 {
		t.Fatalf("GridPeakValue=%d, want 8400 from grid_peak_shaving_power", settings.GridPeakValue)
	}
	if !settings.GridPeakEnabled {
		t.Fatal("GridPeakEnabled=false, want true for register 178 bit 5")
	}
	if settings.GridPeakEnableRaw != 0x30 {
		t.Fatalf("GridPeakEnableRaw=%d, want 48", settings.GridPeakEnableRaw)
	}
}

func TestBuildInverterScreenSettingsGridPeakDisabled(t *testing.T) {
	settings := buildInverterScreenSettings(map[string]any{
		"grid_peak_shaving_power":   float64(5000),
		"grid_peak_shaving_enabled": float64(0x10),
	})

	if settings.GridPeakEnabled {
		t.Fatal("GridPeakEnabled=true, want false when only register 178 bit 4 is set")
	}
}

func TestUsableGridPeakLogRecordRejectsConnectionZeroRecord(t *testing.T) {
	if usableGridPeakLogRecord(map[string]any{
		"grid_peak_shaving_power": float64(0),
		"inverter_time_status":    "no_connection",
		"logger_error":            "dial tcp: connection refused",
	}) {
		t.Fatal("connection-error zero record must not replace the saved initial value")
	}
	if !usableGridPeakLogRecord(map[string]any{
		"grid_peak_shaving_power": float64(8000),
		"inverter_time_status":    "ok",
	}) {
		t.Fatal("valid saved grid peak value must be accepted")
	}
}
