package main

import (
	"encoding/json"
	"testing"
)

// legacyUseTimerProfile is register 146 exactly as shipped before the fix:
// allowed_values [0,1] made every day-of-week mask (e.g. 255) unwritable.
const legacyUseTimerProfile = `[
 {"model":"app.deviceparameter","fields":{"code":"use_timer","register_type":"holding","modbus_address":146,"register_count":1,"bitmask":255,"name":"Use Timer","type":"int",
  "enum_values":[{"value":0,"label":"Off"},{"value":1,"label":"On"}],"min":0,"max":255,"step":1,"is_writable":true,"is_active":true,
  "raw_min":0,"raw_max":255,"enforce_bounds":true,"write_mode":"masked_bits","write_bitmask":255,"allowed_values":[0,1]}},
 {"model":"app.deviceparameter","fields":{"code":"prog_monday_enabled","name":"Prog Monday","bitmask":2,"type":"int","register_type":"holding","modbus_address":146,"register_count":1,"is_writable":false,"is_active":true}},
 {"model":"app.deviceparameter","fields":{"code":"prog_sunday_enabled","name":"Prog Sunday","bitmask":128,"type":"int","register_type":"holding","modbus_address":146,"register_count":1,"is_writable":false,"is_active":true}}
]`

func TestRegister146LegacyProfileIsTreatedAsBitmask(t *testing.T) {
	var params []DeviceParameterFixture
	if err := json.Unmarshal([]byte(legacyUseTimerProfile), &params); err != nil {
		t.Fatal(err)
	}
	enrichProfileBitLayouts(params)
	f := params[0].Fields
	if kind := inferRegisterValueKind(f); kind != kindBitmask {
		t.Fatalf("kind=%s, want bitmask", kind)
	}
	if !allowedValuesSupersededByBitLayout(f) {
		t.Fatal("allowed_values [0,1] must be superseded by the bit layout")
	}
	for _, v := range []float64{0, 1, 3, 254, 255} {
		raw, err := encodeDeviceParameterValue(v, f)
		if err != nil || float64(raw) != v {
			t.Fatalf("encode(%v)=%d,%v; legacy [0,1] must not restrict a bitmask", v, raw, err)
		}
	}
	if _, err := encodeDeviceParameterValue(256, f); err == nil {
		t.Fatal("256 exceeds the documented 8-bit mask and must be rejected")
	}
	derived := map[int]bool{}
	for _, b := range f.Bits {
		derived[b.Bit] = b.Writable
	}
	if !derived[1] || !derived[7] || len(f.Bits) != 2 {
		t.Fatalf("bits must be derived only from documented sibling fields: %+v", f.Bits)
	}
	if issues := validateProfileStructure(params); profileIssuesHaveErrors(issues) {
		t.Fatalf("legacy profile must remain structurally valid: %+v", issues)
	}
}

func forEachModelField(t *testing.T, code string, fn func(model InverterModelDefinition, f DeviceParameterFields)) {
	t.Helper()
	models, err := availableInverterModels()
	if err != nil || len(models) == 0 {
		t.Fatalf("models: %v", err)
	}
	for _, model := range models {
		_, params, err := loadDeviceParametersForModel(model.Key)
		if err != nil {
			t.Fatalf("%s: %v", model.Key, err)
		}
		found := false
		for _, p := range params {
			if p.Fields.Code == code {
				fn(model, p.Fields)
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: code %s missing", model.Key, code)
		}
	}
}

func TestRegister146AllProfiles(t *testing.T) {
	forEachModelField(t, "use_timer", func(model InverterModelDefinition, f DeviceParameterFields) {
		if f.ModbusAddress != 146 || inferRegisterValueKind(f) != kindBitmask || f.WriteBitmask == nil || *f.WriteBitmask != 0xFF {
			t.Fatalf("%s: register 146 must be a bitmask with write mask 0xFF: %+v", model.Key, f)
		}
		for _, v := range []float64{0, 1, 0x7F, 0xFF} {
			if raw, err := encodeDeviceParameterValue(v, f); err != nil || float64(raw) != v {
				t.Fatalf("%s: encode(%v)=%d,%v", model.Key, v, raw, err)
			}
		}
		if _, err := encodeDeviceParameterValue(256, f); err == nil {
			t.Fatalf("%s: 256 must be rejected", model.Key)
		}
		if got := mergeMaskedRegisterValue(0x0100, 0xFF, *f.WriteBitmask); got != 0x01FF {
			t.Fatalf("%s: bit 8 must be preserved, got 0x%04X", model.Key, got)
		}
		decoded := decodeRegisterValue([]uint16{0x01FF}, &f)
		if len(decoded.Violations) != 0 {
			t.Fatalf("%s: bit 8 outside the write mask must not be a violation: %v", model.Key, decoded.Violations)
		}
		for _, b := range f.Bits {
			if b.Bit == 8 && b.Writable {
				t.Fatalf("%s: bit 8 must be read-only", model.Key)
			}
			if b.Bit <= 7 && !b.Writable {
				t.Fatalf("%s: bit %d must be writable", model.Key, b.Bit)
			}
		}
	})
}

func TestRegister178AllProfiles(t *testing.T) {
	forEachModelField(t, "grid_peak_shaving_enabled", func(model InverterModelDefinition, f DeviceParameterFields) {
		if f.ModbusAddress != 178 || f.WriteMode != "mapped_masked_bits" || f.WriteBitmask == nil || *f.WriteBitmask != 0x30 {
			t.Fatalf("%s: unexpected register 178 definition", model.Key)
		}
		if f.WriteValues["0"] != 0x10 || f.WriteValues["1"] != 0x30 || len(f.WriteValues) != 2 {
			t.Fatalf("%s: write_values=%v", model.Key, f.WriteValues)
		}
		if _, err := encodeDeviceParameterValue(2, f); err == nil {
			t.Fatalf("%s: logical value 2 must be rejected", model.Key)
		}
		if got := mergeMaskedRegisterValue(0xFFCF, f.WriteValues["1"], *f.WriteBitmask); got != 0xFFFF {
			t.Fatalf("%s: enable must touch only bits 4–5, got 0x%04X", model.Key, got)
		}
		if got := mergeMaskedRegisterValue(0x0F3F, f.WriteValues["0"], *f.WriteBitmask); got != 0x0F1F {
			t.Fatalf("%s: disable must touch only bits 4–5, got 0x%04X", model.Key, got)
		}
		for raw, want := range map[uint16]string{0x0F1F: "0", 0x0F3F: "1"} {
			d := decodeRegisterValue([]uint16{raw}, &f)
			if len(d.Violations) != 0 || d.EnumLabel == "" || d.EnumLabel[:1] != want {
				t.Fatalf("%s: decode(0x%04X)=%q violations=%v", model.Key, raw, d.EnumLabel, d.Violations)
			}
		}
		if d := decodeRegisterValue([]uint16{0x0000}, &f); len(d.Violations) == 0 {
			t.Fatalf("%s: bits 4–5 = 00 is undocumented and must be reported", model.Key)
		}
	})
}

func TestRegister191AllProfiles(t *testing.T) {
	forEachModelField(t, "grid_peak_shaving_power", func(model InverterModelDefinition, f DeviceParameterFields) {
		if f.ModbusAddress != 191 || f.Max == nil || f.RawMax == nil || deviceParameterWriteScale(f) != 10 {
			t.Fatalf("%s: unexpected register 191 definition", model.Key)
		}
		if raw, err := encodeDeviceParameterValue(*f.Max, f); err != nil || float64(raw) != *f.RawMax {
			t.Fatalf("%s: encode(max %v)=%d,%v", model.Key, *f.Max, raw, err)
		}
		if _, err := encodeDeviceParameterValue(*f.Max+10, f); err == nil {
			t.Fatalf("%s: value above the model limit must be rejected", model.Key)
		}
		if _, err := encodeDeviceParameterValue(5005, f); err == nil {
			t.Fatalf("%s: value not multiple of 10 W must be rejected", model.Key)
		}
	})
}

func TestGridPeakRegistersBlockedOnlyWhenFeatureDisabled(t *testing.T) {
	a := newTestApp(t)
	_, params, err := loadDeviceParametersForModel(defaultInverterModelKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []uint16{178, 191} {
		if blocked, _ := a.gridPeakRegisterBlocked(params, addr); !blocked {
			t.Fatalf("register %d must be blocked while the feature is disabled", addr)
		}
	}
	if blocked, _ := a.gridPeakRegisterBlocked(params, 146); blocked {
		t.Fatal("register 146 must not be affected by the grid peak flag")
	}
	if err := a.kvSet(kvFeatureGridPeakShaving, "1"); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []uint16{178, 191} {
		if blocked, _ := a.gridPeakRegisterBlocked(params, addr); blocked {
			t.Fatalf("register %d must be writable when the feature is enabled", addr)
		}
	}
}

func TestObservationAnalysisIgnoresBitsOutsideWriteMask(t *testing.T) {
	_, params, err := loadDeviceParametersForModel(defaultInverterModelKey)
	if err != nil {
		t.Fatal(err)
	}
	f := deviceParametersByCode(params)["use_timer"]
	an := analyzeObservations(146, []registerObservation{{Raw: 0x01FF, Kind: "read"}, {Raw: 0x00FF, Kind: "write_verified"}}, &f)
	if len(an.Contradictions) != 0 {
		t.Fatalf("bit 8 must not be a contradiction: %v", an.Contradictions)
	}
	an = analyzeObservations(146, []registerObservation{{Raw: 5, Kind: "write_verified"}, {Raw: 5, Kind: "write_rejected"}}, &f)
	if an.Confidence != "conflict" || len(an.Contradictions) != 1 {
		t.Fatalf("accepted and rejected value must be a conflict: %+v", an)
	}
}
