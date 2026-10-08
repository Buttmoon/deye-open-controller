package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func mustProfileField(t *testing.T, modelKey, code string) DeviceParameterFields {
	t.Helper()
	_, field, err := findDeviceParameterByCodeForModel(modelKey, code)
	if err != nil {
		t.Fatalf("model=%s code=%s: %v", modelKey, code, err)
	}
	return field
}

func TestInverterModelProfilesAreValid(t *testing.T) {
	models, err := availableInverterModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 5 {
		t.Fatalf("ожидалось 5 профилей/вариантов, получено %d", len(models))
	}
	defaults := 0
	for _, model := range models {
		if model.Default {
			defaults++
		}
		loaded, params, err := loadDeviceParametersForModel(model.Key)
		if err != nil {
			t.Fatalf("profile %s: %v", model.Key, err)
		}
		if loaded.Key != model.Key || len(params) < 190 {
			t.Fatalf("profile %s loaded incorrectly: key=%s params=%d", model.Key, loaded.Key, len(params))
		}
	}
	if defaults != 1 {
		t.Fatalf("ожидалась одна модель по умолчанию, получено %d", defaults)
	}
}

func TestExactTOURegisterMapForAllProfiles(t *testing.T) {
	models, err := availableInverterModels()
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]uint16{
		"priority_load": 141, "inverter_work_mode": 142, "grid_export_limit": 143,
		"solar_sell": 145, "use_timer": 146,
	}
	for point := 1; point <= 6; point++ {
		expected["sell_time_point_"+string(rune('0'+point))] = uint16(147 + point)
		expected["sell_mode_kw_point_"+string(rune('0'+point))] = uint16(153 + point)
		expected["sell_mode_batt_capacity_"+string(rune('0'+point))] = uint16(165 + point)
		expected["charge_mode_point_"+string(rune('0'+point))] = uint16(171 + point)
	}
	for _, model := range models {
		for code, address := range expected {
			field := mustProfileField(t, model.Key, code)
			if field.ModbusAddress != address {
				t.Fatalf("model=%s code=%s address=%d expected=%d", model.Key, code, field.ModbusAddress, address)
			}
		}
	}
}

func TestSchedulePowerEncodingUsesTechnicalUint16Limit(t *testing.T) {
	cases := []struct {
		modelKey      string
		scheduleValue int
		logicalW      float64
		raw           uint16
	}{
		{"deye_hybrid_60kw_legacy", 2500, 25000, 2500},
		{"deye_hybrid_60kw_v1054_hv_x10_experimental", 6000, 60000, 6000},
		{"deye_sun_25k_sg01hp3_eu_am2_v104", 2500, 25000, 2500},
		{"deye_sun_25k_sg01hp3_eu_am2_v1054", 2500, 25000, 2500},
		{"deye_sun_30k_sg02hp3_eu_am3_v1054", 3000, 30000, 3000},
	}
	for _, tc := range cases {
		field := mustProfileField(t, tc.modelKey, "sell_mode_kw_point_1")
		raw, logical, err := encodeScheduleDeviceParameterValue(tc.scheduleValue, field)
		if err != nil {
			t.Fatalf("model=%s: %v", tc.modelKey, err)
		}
		if raw != tc.raw || math.Abs(logical-tc.logicalW) > 0.001 {
			t.Fatalf("model=%s logical=%f raw=%d", tc.modelKey, logical, raw)
		}
		if _, _, err := encodeScheduleDeviceParameterValue(65535, field); err != nil {
			t.Fatalf("model=%s должен принимать технический максимум 655350 W: %v", tc.modelKey, err)
		}
		if _, _, err := encodeScheduleDeviceParameterValue(65536, field); err == nil {
			t.Fatalf("model=%s должен отклонять значение выше uint16", tc.modelKey)
		}
	}
}

func TestPriorityAndTimerAreMaskedWrites(t *testing.T) {
	models, _ := availableInverterModels()
	for _, model := range models {
		priority := mustProfileField(t, model.Key, "priority_load")
		if priority.WriteMode != "mapped_masked_bits" || priority.WriteBitmask == nil || *priority.WriteBitmask != 3 || priority.WriteValues["0"] != 3 || priority.WriteValues["1"] != 2 {
			t.Fatalf("model=%s unsafe priority_load mapping", model.Key)
		}
		timer := mustProfileField(t, model.Key, "use_timer")
		if timer.WriteMode != "masked_bits" || timer.WriteBitmask == nil || *timer.WriteBitmask != 255 || timer.Max == nil || *timer.Max != 255 {
			t.Fatalf("model=%s unsafe use_timer mapping", model.Key)
		}
	}
}

func TestFirmwareVariantRegister144Meaning(t *testing.T) {
	v104 := mustProfileField(t, "deye_sun_25k_sg01hp3_eu_am2_v104", "external_ct_clamp_phase_v104")
	v105 := mustProfileField(t, "deye_sun_25k_sg01hp3_eu_am2_v1054", "default_max_sell_to_grid_power_v105")
	if v104.ModbusAddress != 144 || v105.ModbusAddress != 144 || v104.IsWritable || v105.IsWritable {
		t.Fatal("register 144 variants must be separate read-only interpretations")
	}
}

func TestHighVoltageAndWordPairDecoding(t *testing.T) {
	voltage := mustProfileField(t, "deye_sun_25k_sg01hp3_eu_am2_v104", "battery_voltage_3p")
	decodedVoltage, err := decodeDeviceParameterRegisters([]uint16{5120}, voltage)
	if err != nil || math.Abs(decodedVoltage-512.0) > 0.001 {
		t.Fatalf("battery voltage=%f err=%v", decodedVoltage, err)
	}
	gridPower := mustProfileField(t, "deye_sun_25k_sg01hp3_eu_am2_v104", "grid_power_3p")
	if len(gridPower.ModbusAddresses) != 2 || !strings.EqualFold(gridPower.WordOrder, "low_high") {
		t.Fatalf("grid_power_3p должен использовать пару low/high: %+v", gridPower)
	}
	decodedPower, err := decodeDeviceParameterRegisters([]uint16{0x86A0, 0x0001}, gridPower)
	if err != nil || decodedPower != -100000 {
		t.Fatalf("grid power=%f err=%v", decodedPower, err)
	}
}

func TestScheduleValidationRejectsBadTimeChargeModeAndPower(t *testing.T) {
	model, _ := findInverterModel("deye_sun_25k_sg01hp3_eu_am2_v104")
	payload := defaultSchedulePayload()
	payload.Days[0].Enabled = true
	payload.Days[0].Hours[0].Enabled = true
	payload.Days[0].Hours[0].SellTime = 0
	payload.Days[0].Hours[0].SellModeKW = 2500
	payload.Days[0].Hours[0].GridExportLimit = 2500
	payload.Days[0].Hours[0].SellModeBattCapacity = 50
	payload.Days[0].Hours[0].ChargeMode = 3
	payload.Days[0].Hours[0].LoadLimitMode = 2
	payload.Days[0].Hours[0].PriorityLoad = 1
	marshal := func() string { b, _ := json.Marshal(payload); return string(b) }
	if err := validateScheduleJSONForModels(marshal(), []InverterModelDefinition{model}); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	payload.Days[0].Hours[0].SellTime = 2360
	if err := validateScheduleJSONForModels(marshal(), []InverterModelDefinition{model}); err == nil {
		t.Fatal("2360 must be rejected")
	}
	payload.Days[0].Hours[0].SellTime = 1257
	if err := validateScheduleJSONForModels(marshal(), []InverterModelDefinition{model}); err == nil {
		t.Fatal("minute not divisible by 5 must be rejected")
	}
	payload.Days[0].Hours[0].SellTime = 1255
	payload.Days[0].Hours[0].ChargeMode = 4
	if err := validateScheduleJSONForModels(marshal(), []InverterModelDefinition{model}); err == nil {
		t.Fatal("charge mode 4 must be rejected")
	}
	payload.Days[0].Hours[0].ChargeMode = 3
	payload.Days[0].Hours[0].SellModeKW = 65535
	payload.Days[0].Hours[0].GridExportLimit = 65535
	if err := validateScheduleJSONForModels(marshal(), []InverterModelDefinition{model}); err != nil {
		t.Fatalf("технический максимум должен приниматься независимо от номинала модели: %v", err)
	}
	payload.Days[0].Hours[0].SellModeKW = 65536
	if err := validateScheduleJSONForModels(marshal(), []InverterModelDefinition{model}); err == nil {
		t.Fatal("значение выше технического uint16-предела должно отклоняться")
	}
}

func TestChargeModeVariantsAndTimeEncoder(t *testing.T) {
	v104, err := findInverterModel("deye_sun_25k_sg01hp3_eu_am2_v104")
	if err != nil {
		t.Fatal(err)
	}
	v105, err := findInverterModel("deye_sun_25k_sg01hp3_eu_am2_v1054")
	if err != nil {
		t.Fatal(err)
	}
	payload := defaultSchedulePayload()
	payload.Days[0].Enabled = true
	payload.Days[0].Hours[0].Enabled = true
	payload.Days[0].Hours[0].SellTime = 1255
	payload.Days[0].Hours[0].SellModeKW = 100
	payload.Days[0].Hours[0].GridExportLimit = 100
	payload.Days[0].Hours[0].SellModeBattCapacity = 50
	payload.Days[0].Hours[0].ChargeMode = 32
	payload.Days[0].Hours[0].LoadLimitMode = 2
	b, _ := json.Marshal(payload)
	if err := validateScheduleJSONForModels(string(b), []InverterModelDefinition{v104}); err == nil {
		t.Fatal("V104 must reject Sell bit charge mode 32")
	}
	if err := validateScheduleJSONForModels(string(b), []InverterModelDefinition{v105}); err != nil {
		t.Fatalf("V105 must accept Sell bit charge mode 32: %v", err)
	}

	timeField := mustProfileField(t, v104.Key, "sell_time_point_1")
	if _, err := encodeDeviceParameterValue(2360, timeField); err == nil {
		t.Fatal("time encoder must reject 2360")
	}
	if _, err := encodeDeviceParameterValue(1257, timeField); err == nil {
		t.Fatal("time encoder must reject a minute not divisible by 5")
	}
	if raw, err := encodeDeviceParameterValue(1255, timeField); err != nil || raw != 1255 {
		t.Fatalf("time encoder rejected valid 12:55: raw=%d err=%v", raw, err)
	}
}

func TestHighVoltageProgramVoltageBounds(t *testing.T) {
	field := mustProfileField(t, "deye_sun_30k_sg02hp3_eu_am3_v1054", "prog1_voltage")
	if got := decodeDeviceParameterValue(6300, field); math.Abs(got-630) > 0.001 {
		t.Fatalf("raw 6300 decoded as %f; want 630V", got)
	}
	if _, err := encodeDeviceParameterValue(700, field); err == nil {
		t.Fatal("700V must be rejected because protocol raw maximum is 6300")
	}
	if raw, err := encodeDeviceParameterValue(630, field); err != nil || raw != 6300 {
		t.Fatalf("630V encoding raw=%d err=%v", raw, err)
	}
}

func TestSafetyCriticalRegisterBounds(t *testing.T) {
	models, err := availableInverterModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		for _, code := range []string{"battery_equalization_voltage", "battery_absorption_voltage", "battery_float_voltage"} {
			field := mustProfileField(t, model.Key, code)
			if field.IsWritable || deviceParameterReadScale(field) != 0.01 || field.RawMin == nil || *field.RawMin != 3800 || field.RawMax == nil || *field.RawMax != 6100 {
				t.Fatalf("model=%s code=%s unsafe lead-acid/HV mapping", model.Key, code)
			}
		}
		if field := mustProfileField(t, model.Key, "zero_export_power"); field.IsWritable || deviceParameterReadScale(field) != 10 {
			t.Fatalf("model=%s register 104 must be read-only HV 10 W/raw", model.Key)
		}
		for _, code := range []string{"battery_max_charge_current", "battery_max_discharge_current", "generator_charge_battery_current", "grid_charge_battery_current"} {
			field := mustProfileField(t, model.Key, code)
			if field.Max == nil || int(*field.Max) != model.BatteryCurrentMaxA || field.RawMax == nil || int(*field.RawMax) != model.BatteryCurrentMaxA || !field.EnforceBounds {
				t.Fatalf("model=%s code=%s current cap is not %dA", model.Key, code, model.BatteryCurrentMaxA)
			}
		}
		for _, code := range []string{"generator_charge_start_voltage", "grid_charge_start_voltage"} {
			field := mustProfileField(t, model.Key, code)
			if deviceParameterReadScale(field) != 0.1 || field.Min == nil || *field.Min != 160 || field.Max == nil || *field.Max != 630 || field.RawMin == nil || *field.RawMin != 1600 || field.RawMax == nil || *field.RawMax != 6300 {
				t.Fatalf("model=%s code=%s invalid HV voltage bounds", model.Key, code)
			}
			if model.RatedPowerW == 60000 && field.IsWritable {
				t.Fatalf("model=%s code=%s must stay read-only without exact 60kW model code", model.Key, code)
			}
		}
		for _, code := range []string{"grid_peak_shaving_power", "max_solar_power"} {
			field := mustProfileField(t, model.Key, code)
			if deviceParameterReadScale(field) != 10 || deviceParameterWriteScale(field) != 10 || field.Max == nil || int(*field.Max) != model.RatedPowerW || field.RawMax == nil || int(*field.RawMax) != model.RatedPowerW/10 {
				t.Fatalf("model=%s code=%s must be capped at rated power %dW", model.Key, code, model.RatedPowerW)
			}
		}
	}
}

func TestGridConfigurationRegistersRemainReadOnly(t *testing.T) {
	models, err := availableInverterModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		for _, tc := range []struct {
			code    string
			address uint16
		}{
			{"grid_standard", 182},
			{"configured_grid_frequency", 183},
			{"configured_grid_phases", 184},
		} {
			field := mustProfileField(t, model.Key, tc.code)
			if field.ModbusAddress != tc.address || field.IsWritable {
				t.Fatalf("model=%s code=%s address=%d writable=%v; want address=%d read-only", model.Key, tc.code, field.ModbusAddress, field.IsWritable, tc.address)
			}
		}
	}
}

func TestRawWriteCannotBypassKnownReadOnlyOrBounds(t *testing.T) {
	_, params, err := loadDeviceParametersForModel("deye_sun_25k_sg01hp3_eu_am2_v104")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, matched, err := validateRawProfileWrite(params, 104, 100); !matched || err == nil {
		t.Fatalf("known read-only register 104 must be blocked: matched=%v err=%v", matched, err)
	}
	code, logical, matched, err := validateRawProfileWrite(params, 154, 65535)
	if err != nil || !matched || code != "sell_mode_kw_point_1" || logical != 655350 {
		t.Fatalf("technical uint16 maximum rejected: code=%s logical=%v matched=%v err=%v", code, logical, matched, err)
	}
	if _, _, matched, err := validateRawProfileWrite(params, 65530, 1); matched || err != nil {
		t.Fatalf("unknown engineering address should remain unmatched: matched=%v err=%v", matched, err)
	}
}

func TestEveryWritableProfileFieldIsBoundedAndBitfieldsAreMasked(t *testing.T) {
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
		for _, fixture := range params {
			field := fixture.Fields
			if !field.IsActive || !field.IsWritable {
				continue
			}
			if !field.EnforceBounds || field.Min == nil || field.Max == nil || field.RawMin == nil || field.RawMax == nil {
				t.Fatalf("%s/%s: writable field is not fully bounded", model.Key, field.Code)
			}
		}
		wake := byCode["battery_wake_up"]
		if wake.WriteMode != "masked_bits" || wake.WriteBitmask == nil || *wake.WriteBitmask != 1 {
			t.Fatalf("%s: register 112 must preserve all bits except bit0", model.Key)
		}
		offGrid := byCode["off_grid_mode"]
		if offGrid.WriteMode != "mapped_masked_bits" || offGrid.WriteBitmask == nil || *offGrid.WriteBitmask != 12 || offGrid.WriteValues["0"] != 8 || offGrid.WriteValues["1"] != 12 {
			t.Fatalf("%s: register 179 forced off-grid bitfield is unsafe", model.Key)
		}
		gridPeak := byCode["grid_peak_shaving_enabled"]
		if gridPeak.ModbusAddress != 178 || gridPeak.WriteMode != "mapped_masked_bits" || gridPeak.WriteBitmask == nil || *gridPeak.WriteBitmask != 0x30 || gridPeak.WriteValues["0"] != 0x10 || gridPeak.WriteValues["1"] != 0x30 {
			t.Fatalf("%s: register 178 grid peak shaving bitfield is unsafe", model.Key)
		}
	}
}

func TestMergeMaskedRegisterValuePreservesUnrelatedBits(t *testing.T) {
	if got := mergeMaskedRegisterValue(0x0103, 0x0000, 0x00FF); got != 0x0100 {
		t.Fatalf("timer mask must preserve bit8: got 0x%04X", got)
	}
	if got := mergeMaskedRegisterValue(0x0003, 0x000C, 0x000C); got != 0x000F {
		t.Fatalf("off-grid mask must preserve CT bits0-1: got 0x%04X", got)
	}
	if got := mergeMaskedRegisterValue(0x0101, 0x0000, 0x0001); got != 0x0100 {
		t.Fatalf("battery wake-up must preserve battery2 bit8: got 0x%04X", got)
	}
	if got := mergeMaskedRegisterValue(0x010F, 0x0030, 0x0030); got != 0x013F {
		t.Fatalf("grid peak enable must preserve bits outside 4-5: got 0x%04X", got)
	}
	if got := mergeMaskedRegisterValue(0x013F, 0x0010, 0x0030); got != 0x011F {
		t.Fatalf("grid peak disable must preserve bit4 and unrelated bits: got 0x%04X", got)
	}
}
