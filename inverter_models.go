package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	inverterModelsFilePath   = "inverter_models.json"
	defaultInverterModelKey  = "deye_hybrid_60kw_legacy"
	legacyParametersFilePath = "device_parameters.json"
)

type InverterModelDefinition struct {
	Key                  string `json:"key"`
	Manufacturer         string `json:"manufacturer"`
	Name                 string `json:"name"`
	ModelCode            string `json:"model_code"`
	ParametersFile       string `json:"parameters_file"`
	RatedPowerW          int    `json:"rated_power_w"`
	SchedulePowerMaxW    int    `json:"schedule_power_max_w"`
	GridExportMaxW       int    `json:"grid_export_max_w"`
	PhaseCount           int    `json:"phase_count"`
	MPPTCount            int    `json:"mppt_count"`
	BatteryCurrentMaxA   int    `json:"battery_current_max_a,omitempty"`
	Protocol             string `json:"protocol"`
	ProfileVariant       string `json:"profile_variant,omitempty"`
	ValidationStatus     string `json:"validation_status"`
	Experimental         bool   `json:"experimental,omitempty"`
	WriteRequiresConfirm bool   `json:"write_requires_confirmation,omitempty"`
	Documentation        string `json:"documentation,omitempty"`
	Notes                string `json:"notes,omitempty"`
	Default              bool   `json:"default"`
}

type inverterModelRegistry struct {
	SchemaVersion int                       `json:"schema_version"`
	Models        []InverterModelDefinition `json:"models"`
}

type inverterModelRegistryCacheEntry struct {
	modTime time.Time
	size    int64
	models  []InverterModelDefinition
}

var inverterModelsCache struct {
	sync.RWMutex
	entries map[string]inverterModelRegistryCacheEntry
}

func loadInverterModels(path string) ([]InverterModelDefinition, error) {
	if strings.TrimSpace(path) == "" {
		path = inverterModelsFilePath
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	inverterModelsCache.RLock()
	entry, ok := inverterModelsCache.entries[path]
	if ok && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
		models := append([]InverterModelDefinition(nil), entry.models...)
		inverterModelsCache.RUnlock()
		return models, nil
	}
	inverterModelsCache.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var registry inverterModelRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return nil, fmt.Errorf("не удалось разобрать %s: %w", path, err)
	}
	if registry.SchemaVersion <= 0 {
		return nil, fmt.Errorf("%s: schema_version должен быть больше 0", path)
	}
	if len(registry.Models) == 0 {
		return nil, fmt.Errorf("%s: список models пуст", path)
	}

	baseDir := filepath.Dir(path)
	seen := make(map[string]struct{}, len(registry.Models))
	defaultCount := 0
	for i := range registry.Models {
		model := &registry.Models[i]
		model.Key = strings.TrimSpace(model.Key)
		model.Name = strings.TrimSpace(model.Name)
		model.ModelCode = strings.TrimSpace(model.ModelCode)
		model.ParametersFile = strings.TrimSpace(model.ParametersFile)
		if model.Key == "" || model.Name == "" || model.ParametersFile == "" {
			return nil, fmt.Errorf("%s: model[%d] должен содержать key, name и parameters_file", path, i)
		}
		if model.RatedPowerW <= 0 {
			return nil, fmt.Errorf("%s: model[%d] rated_power_w должен быть > 0", path, i)
		}
		if model.SchedulePowerMaxW <= 0 {
			model.SchedulePowerMaxW = 655350
		}
		if model.GridExportMaxW <= 0 {
			model.GridExportMaxW = 655350
		}
		if model.SchedulePowerMaxW > 655350 || model.GridExportMaxW > 655350 {
			return nil, fmt.Errorf("%s: model[%d] Power/Grid Export Limit не могут превышать технический uint16-предел 655350 W", path, i)
		}
		if model.BatteryCurrentMaxA <= 0 || model.BatteryCurrentMaxA > 185 {
			return nil, fmt.Errorf("%s: model[%d] battery_current_max_a должен быть 1–185 A", path, i)
		}
		if _, exists := seen[model.Key]; exists {
			return nil, fmt.Errorf("%s: повторяющийся model key=%s", path, model.Key)
		}
		seen[model.Key] = struct{}{}
		if model.Default {
			defaultCount++
		}
		clean := filepath.Clean(model.ParametersFile)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s: небезопасный parameters_file для model=%s", path, model.Key)
		}
		model.ParametersFile = filepath.Join(baseDir, clean)
	}
	if defaultCount != 1 {
		return nil, fmt.Errorf("%s: ровно одна модель должна иметь default=true, сейчас %d", path, defaultCount)
	}

	sort.SliceStable(registry.Models, func(i, j int) bool {
		if registry.Models[i].Default != registry.Models[j].Default {
			return registry.Models[i].Default
		}
		return registry.Models[i].Name < registry.Models[j].Name
	})

	inverterModelsCache.Lock()
	if inverterModelsCache.entries == nil {
		inverterModelsCache.entries = make(map[string]inverterModelRegistryCacheEntry)
	}
	inverterModelsCache.entries[path] = inverterModelRegistryCacheEntry{
		modTime: info.ModTime(),
		size:    info.Size(),
		models:  append([]InverterModelDefinition(nil), registry.Models...),
	}
	inverterModelsCache.Unlock()
	return append([]InverterModelDefinition(nil), registry.Models...), nil
}

func availableInverterModels() ([]InverterModelDefinition, error) {
	return loadInverterModels(inverterModelsFilePath)
}

func defaultInverterModel() (InverterModelDefinition, error) {
	models, err := availableInverterModels()
	if err != nil {
		return InverterModelDefinition{}, err
	}
	for _, model := range models {
		if model.Default {
			return model, nil
		}
	}
	return InverterModelDefinition{}, fmt.Errorf("модель по умолчанию не найдена")
}

func normalizeInverterModelKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return defaultInverterModelKey
	}
	// Совместимость с ключами первой multimodel-версии.
	switch key {
	case "deye_sun_25k_sg01hp3_eu_am2":
		return "deye_sun_25k_sg01hp3_eu_am2_v104"
	case "deye_sun_30k_sg02hp3_eu_am3":
		return "deye_sun_30k_sg02hp3_eu_am3_v1054"
	default:
		return key
	}
}

func findInverterModel(key string) (InverterModelDefinition, error) {
	key = normalizeInverterModelKey(key)
	models, err := availableInverterModels()
	if err != nil {
		return InverterModelDefinition{}, err
	}
	for _, model := range models {
		if model.Key == key {
			return model, nil
		}
	}
	return InverterModelDefinition{}, fmt.Errorf("неизвестный тип инвертора: %s", key)
}

func loadDeviceParametersForModel(modelKey string) (InverterModelDefinition, []DeviceParameterFixture, error) {
	model, err := findInverterModel(modelKey)
	if err != nil {
		return InverterModelDefinition{}, nil, err
	}
	params, err := LoadDeviceParameters(model.ParametersFile)
	if err != nil {
		return model, nil, fmt.Errorf("ошибка чтения профиля %s (%s): %w", model.Name, model.ParametersFile, err)
	}
	if err := validateDeviceParameterProfile(model, params); err != nil {
		return model, nil, err
	}
	return model, params, nil
}

func validateDeviceParameterProfile(model InverterModelDefinition, params []DeviceParameterFixture) error {
	if len(params) == 0 {
		return fmt.Errorf("профиль %s пуст", model.Name)
	}
	seenCodes := make(map[string]uint16, len(params))
	for _, fixture := range params {
		f := fixture.Fields
		code := strings.ToLower(strings.TrimSpace(f.Code))
		if code == "" {
			return fmt.Errorf("профиль %s содержит параметр без code", model.Name)
		}
		if old, ok := seenCodes[code]; ok {
			return fmt.Errorf("профиль %s содержит повтор code=%s (адреса %d и %d)", model.Name, code, old, f.ModbusAddress)
		}
		seenCodes[code] = f.ModbusAddress
		if f.RegisterCount <= 0 {
			return fmt.Errorf("профиль %s code=%s: register_count должен быть > 0", model.Name, code)
		}
		if _, ok := getRegisterType(f.RegisterType); !ok {
			return fmt.Errorf("профиль %s code=%s: неподдерживаемый register_type=%s", model.Name, code, f.RegisterType)
		}
		if f.IsActive && f.IsWritable {
			if !f.EnforceBounds || f.Min == nil || f.Max == nil || f.RawMin == nil || f.RawMax == nil {
				return fmt.Errorf("профиль %s code=%s: каждый writable-параметр должен иметь enforce_bounds, min/max и raw_min/raw_max", model.Name, code)
			}
		}
	}

	requiredWritable := []string{
		"grid_export_limit", "grid_charge_enable", "solar_sell", "inverter_work_mode", "use_timer", "priority_load",
	}
	for point := 1; point <= 6; point++ {
		requiredWritable = append(requiredWritable,
			fmt.Sprintf("sell_time_point_%d", point),
			fmt.Sprintf("sell_mode_kw_point_%d", point),
			fmt.Sprintf("sell_mode_batt_capacity_%d", point),
			fmt.Sprintf("charge_mode_point_%d", point),
		)
	}
	byCode := deviceParametersByCode(params)
	for _, code := range requiredWritable {
		address, ok := seenCodes[code]
		if !ok {
			return fmt.Errorf("профиль %s: отсутствует обязательный параметр расписания code=%s", model.Name, code)
		}
		field := byCode[code]
		if !field.IsWritable || !strings.EqualFold(field.RegisterType, "holding") {
			return fmt.Errorf("профиль %s code=%s address=%d должен быть writable holding", model.Name, code, address)
		}
	}

	expected := map[string]uint16{
		"priority_load": 141, "inverter_work_mode": 142, "grid_export_limit": 143,
		"solar_sell": 145, "use_timer": 146,
	}
	for point := 1; point <= 6; point++ {
		expected[fmt.Sprintf("sell_time_point_%d", point)] = uint16(147 + point)
		expected[fmt.Sprintf("sell_mode_kw_point_%d", point)] = uint16(153 + point)
		expected[fmt.Sprintf("sell_mode_batt_capacity_%d", point)] = uint16(165 + point)
		expected[fmt.Sprintf("charge_mode_point_%d", point)] = uint16(171 + point)
	}
	for code, expectedAddress := range expected {
		field := byCode[code]
		if field.ModbusAddress != expectedAddress {
			return fmt.Errorf("профиль %s code=%s: адрес %d, ожидается %d по TOU-карте Deye", model.Name, code, field.ModbusAddress, expectedAddress)
		}
	}
	v104ChargeModes := []int{0, 1, 2, 3}
	v105ChargeModes := []int{0, 1, 2, 3, 32, 33, 34, 35}
	expectedChargeModes := v105ChargeModes
	if strings.HasSuffix(model.Key, "_v104") {
		expectedChargeModes = v104ChargeModes
	}
	equalIntSlices := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	for point := 1; point <= 6; point++ {
		timeField := byCode[fmt.Sprintf("sell_time_point_%d", point)]
		if timeField.Min == nil || timeField.Max == nil || *timeField.Min != 0 || *timeField.Max != 2355 || !timeField.EnforceBounds {
			return fmt.Errorf("профиль %s: диапазон sell_time_point_%d должен быть 0..2355 с enforce_bounds", model.Name, point)
		}
		powerField := byCode[fmt.Sprintf("sell_mode_kw_point_%d", point)]
		if powerField.Max == nil || int(*powerField.Max) != model.SchedulePowerMaxW || !powerField.EnforceBounds {
			return fmt.Errorf("профиль %s: sell_mode_kw_point_%d max должен быть %d W", model.Name, point, model.SchedulePowerMaxW)
		}
		if deviceParameterScheduleInputScale(powerField) != 10 || deviceParameterWriteScale(powerField) != 10 {
			return fmt.Errorf("профиль %s: sell_mode_kw_point_%d должен использовать schedule/write scale=10", model.Name, point)
		}
		chargeField := byCode[fmt.Sprintf("charge_mode_point_%d", point)]
		if !equalIntSlices(chargeField.AllowedValues, expectedChargeModes) {
			return fmt.Errorf("профиль %s: charge_mode_point_%d allowed_values=%v, ожидается %v для варианта %s", model.Name, point, chargeField.AllowedValues, expectedChargeModes, model.ProfileVariant)
		}
		if chargeField.RawMin == nil || chargeField.RawMax == nil || int(*chargeField.RawMin) != expectedChargeModes[0] || int(*chargeField.RawMax) != expectedChargeModes[len(expectedChargeModes)-1] {
			return fmt.Errorf("профиль %s: charge_mode_point_%d raw bounds не соответствуют allowed_values", model.Name, point)
		}
		voltageField := byCode[fmt.Sprintf("prog%d_voltage", point)]
		if strings.Contains(strings.ToLower(model.Protocol), "high-voltage") || strings.Contains(strings.ToLower(model.Protocol), "hp3") || strings.Contains(model.Key, "_v1054") || strings.Contains(model.Key, "_v104") {
			if voltageField.ModbusAddress != uint16(159+point) || deviceParameterReadScale(voltageField) != 0.1 || voltageField.RawMax == nil || *voltageField.RawMax != 6300 || voltageField.Max == nil || *voltageField.Max != 630 {
				return fmt.Errorf("профиль %s: prog%d_voltage должен использовать address=%d, 0.1 V/raw и protocol max raw=6300 (630 V)", model.Name, point, 159+point)
			}
		}
	}
	gridExport := byCode["grid_export_limit"]
	if gridExport.Max == nil || int(*gridExport.Max) != model.GridExportMaxW || deviceParameterScheduleInputScale(gridExport) != 10 || deviceParameterWriteScale(gridExport) != 10 {
		return fmt.Errorf("профиль %s: grid_export_limit должен иметь max=%d W и scale=10", model.Name, model.GridExportMaxW)
	}
	priority := byCode["priority_load"]
	if priority.WriteMode != "mapped_masked_bits" || priority.WriteBitmask == nil || *priority.WriteBitmask != 3 || priority.WriteValues["0"] != 3 || priority.WriteValues["1"] != 2 {
		return fmt.Errorf("профиль %s: priority_load должен безопасно кодировать Load First=3/Battery First=2 в bits0-1", model.Name)
	}
	useTimer := byCode["use_timer"]
	if useTimer.WriteMode != "masked_bits" || useTimer.WriteBitmask == nil || *useTimer.WriteBitmask != 255 || useTimer.Max == nil || *useTimer.Max != 255 {
		return fmt.Errorf("профиль %s: use_timer должен менять только bits0-7 регистра 146", model.Name)
	}
	if len(useTimer.AllowedValues) > 0 && !allowedValuesSupersededByBitLayout(useTimer) {
		return fmt.Errorf("профиль %s: use_timer/register 146 — битовая маска (bit0 TOU, bits1-7 дни недели); allowed_values=%v без описания битов недопустим", model.Name, useTimer.AllowedValues)
	}
	batteryWakeUp := byCode["battery_wake_up"]
	if batteryWakeUp.WriteMode != "masked_bits" || batteryWakeUp.WriteBitmask == nil || *batteryWakeUp.WriteBitmask != 1 {
		return fmt.Errorf("профиль %s: battery_wake_up/register 112 должен менять только bit0", model.Name)
	}
	offGridMode := byCode["off_grid_mode"]
	if offGridMode.WriteMode != "mapped_masked_bits" || offGridMode.WriteBitmask == nil || *offGridMode.WriteBitmask != 12 ||
		offGridMode.WriteValues["0"] != 8 || offGridMode.WriteValues["1"] != 12 {
		return fmt.Errorf("профиль %s: off_grid_mode/register 179 должен безопасно кодировать disable=0b10/enable=0b11 в bits2-3", model.Name)
	}
	gridPeakEnabled := byCode["grid_peak_shaving_enabled"]
	if gridPeakEnabled.ModbusAddress != 178 || gridPeakEnabled.WriteMode != "mapped_masked_bits" ||
		gridPeakEnabled.WriteBitmask == nil || *gridPeakEnabled.WriteBitmask != 0x30 ||
		gridPeakEnabled.WriteValues["0"] != 0x10 || gridPeakEnabled.WriteValues["1"] != 0x30 {
		return fmt.Errorf("профиль %s: grid_peak_shaving_enabled/register 178 должен безопасно кодировать disable=0b01/enable=0b11 в bits4-5", model.Name)
	}
	workMode := byCode["inverter_work_mode"]
	if !equalIntSlices(workMode.AllowedValues, []int{0, 1, 2}) {
		return fmt.Errorf("профиль %s: inverter_work_mode allowed_values должен быть [0 1 2]", model.Name)
	}
	// Safety-critical non-TOU fields are checked too, so an apparently valid
	// schedule profile cannot expose unsafe manual writes with a wrong scale.
	for _, code := range []string{"battery_equalization_voltage", "battery_absorption_voltage", "battery_float_voltage"} {
		field := byCode[code]
		if deviceParameterReadScale(field) != 0.01 || deviceParameterWriteScale(field) != 0.01 ||
			field.RawMin == nil || *field.RawMin != 3800 || field.RawMax == nil || *field.RawMax != 6100 || field.IsWritable {
			return fmt.Errorf("профиль %s: %s должен быть read-only, 0.01 V/raw, raw 3800..6100", model.Name, code)
		}
	}
	emptyVoltage := byCode["battery_low_voltage"]
	if deviceParameterReadScale(emptyVoltage) != 0.01 || emptyVoltage.IsWritable {
		return fmt.Errorf("профиль %s: battery_low_voltage/register 103 должен быть read-only и 0.01 V/raw", model.Name)
	}
	zeroExport := byCode["zero_export_power"]
	if deviceParameterReadScale(zeroExport) != 10 || zeroExport.IsWritable {
		return fmt.Errorf("профиль %s: zero_export_power/register 104 должен быть read-only и HV 10 W/raw", model.Name)
	}
	equalizationDays := byCode["battery_equalization_days"]
	if equalizationDays.Min == nil || *equalizationDays.Min != 0 || equalizationDays.Max == nil || *equalizationDays.Max != 90 ||
		equalizationDays.RawMin == nil || *equalizationDays.RawMin != 0 || equalizationDays.RawMax == nil || *equalizationDays.RawMax != 90 || !equalizationDays.EnforceBounds {
		return fmt.Errorf("профиль %s: battery_equalization_days/register 105 должен быть 0..90 дней", model.Name)
	}
	equalizationHours := byCode["battery_equalization_hours"]
	if deviceParameterReadScale(equalizationHours) != 0.5 || deviceParameterWriteScale(equalizationHours) != 0.5 ||
		equalizationHours.Min == nil || *equalizationHours.Min != 0 || equalizationHours.Max == nil || *equalizationHours.Max != 10 ||
		equalizationHours.RawMin == nil || *equalizationHours.RawMin != 0 || equalizationHours.RawMax == nil || *equalizationHours.RawMax != 20 || !equalizationHours.EnforceBounds {
		return fmt.Errorf("профиль %s: battery_equalization_hours/register 106 должен быть raw 0..20, 0.5 h/raw", model.Name)
	}
	for _, code := range []string{"generator_max_operating_time", "generator_cooling_time"} {
		field := byCode[code]
		if deviceParameterReadScale(field) != 0.1 || field.IsWritable {
			return fmt.Errorf("профиль %s: %s должен быть read-only и 0.1 h/raw, пока не подтверждён диапазон записи", model.Name, code)
		}
	}
	acCoupleFrequency := byCode["generator_ac_couple_frz_high"]
	if deviceParameterReadScale(acCoupleFrequency) != 0.01 || deviceParameterWriteScale(acCoupleFrequency) != 0.01 ||
		acCoupleFrequency.Min == nil || *acCoupleFrequency.Min != 50 || acCoupleFrequency.Max == nil || *acCoupleFrequency.Max != 65 ||
		acCoupleFrequency.RawMin == nil || *acCoupleFrequency.RawMin != 5000 || acCoupleFrequency.RawMax == nil || *acCoupleFrequency.RawMax != 6500 || !acCoupleFrequency.EnforceBounds {
		return fmt.Errorf("профиль %s: generator_ac_couple_frz_high/register 131 должен быть raw 5000..6500, 0.01 Hz/raw", model.Name)
	}
	upsDelay := byCode["ups_delay_time"]
	if !equalIntSlices(upsDelay.AllowedValues, []int{0, 1}) || upsDelay.RawMin == nil || *upsDelay.RawMin != 0 || upsDelay.RawMax == nil || *upsDelay.RawMax != 1 {
		return fmt.Errorf("профиль %s: ups_delay_time/register 209 должен разрешать только 0 или 1", model.Name)
	}
	for _, code := range []string{"battery_max_charge_current", "battery_max_discharge_current", "generator_charge_battery_current", "grid_charge_battery_current"} {
		field := byCode[code]
		if field.Min == nil || *field.Min != 0 || field.Max == nil || int(*field.Max) != model.BatteryCurrentMaxA ||
			field.RawMin == nil || *field.RawMin != 0 || field.RawMax == nil || int(*field.RawMax) != model.BatteryCurrentMaxA || !field.EnforceBounds {
			return fmt.Errorf("профиль %s: %s должен быть ограничен 0..%d A", model.Name, code, model.BatteryCurrentMaxA)
		}
	}
	for _, code := range []string{"battery_shutdown_voltage", "battery_restart_voltage", "battery_low_voltage_cap"} {
		field := byCode[code]
		if deviceParameterReadScale(field) != 0.1 || field.RawMin == nil || *field.RawMin != 3800 ||
			field.RawMax == nil || *field.RawMax != 6100 || field.IsWritable {
			return fmt.Errorf("профиль %s: %s должен быть read-only, HV 0.1 V/raw, raw 3800..6100", model.Name, code)
		}
	}
	for _, code := range []string{"generator_charge_start_voltage", "grid_charge_start_voltage"} {
		field := byCode[code]
		if deviceParameterReadScale(field) != 0.1 || deviceParameterWriteScale(field) != 0.1 ||
			field.RawMin == nil || *field.RawMin != 1600 || field.RawMax == nil || *field.RawMax != 6300 ||
			field.Min == nil || *field.Min != 160 || field.Max == nil || *field.Max != 630 || !field.EnforceBounds {
			return fmt.Errorf("профиль %s: %s должен использовать безопасный диапазон 160..630 V, 0.1 V/raw", model.Name, code)
		}
		if model.RatedPowerW == 60000 && field.IsWritable {
			return fmt.Errorf("профиль %s: %s должен быть read-only без точного model code 60 kW", model.Name, code)
		}
	}
	minPV := byCode["min_pv_power_for_gen_start"]
	if deviceParameterReadScale(minPV) != 1 || minPV.Min == nil || *minPV.Min != 0 || minPV.Max == nil || *minPV.Max != 8000 ||
		minPV.RawMin == nil || *minPV.RawMin != 0 || minPV.RawMax == nil || *minPV.RawMax != 8000 || !minPV.EnforceBounds {
		return fmt.Errorf("профиль %s: min_pv_power_for_gen_start должен быть 0..8000 W, 1 W/raw", model.Name)
	}
	for _, code := range []string{"grid_peak_shaving_power", "max_solar_power"} {
		field := byCode[code]
		if deviceParameterReadScale(field) != 10 || deviceParameterWriteScale(field) != 10 ||
			field.Max == nil || int(*field.Max) != model.RatedPowerW || field.RawMax == nil || int(*field.RawMax) != model.RatedPowerW/10 || !field.EnforceBounds {
			return fmt.Errorf("профиль %s: %s должен быть HV 10 W/raw и ограничен rated_power_w=%d", model.Name, code, model.RatedPowerW)
		}
	}
	// Grid-code, nominal frequency and phase topology are regulatory / installation
	// settings. Their numeric values vary by market and firmware, so they are
	// intentionally exposed only for reading in every bundled profile.
	for _, code := range []string{"grid_standard", "configured_grid_frequency", "configured_grid_phases"} {
		field, ok := byCode[code]
		if !ok || field.IsWritable {
			return fmt.Errorf("профиль %s: %s/register 182–184 должен оставаться read-only", model.Name, code)
		}
	}

	if strings.HasSuffix(model.Key, "_v104") {
		reg144, ok := byCode["external_ct_clamp_phase_v104"]
		if !ok || reg144.ModbusAddress != 144 || reg144.IsWritable {
			return fmt.Errorf("профиль %s: V104 должен описывать register 144 как read-only external CT clamp phase", model.Name)
		}
	} else if strings.Contains(model.Key, "v1054") {
		reg144, ok := byCode["default_max_sell_to_grid_power_v105"]
		if !ok || reg144.ModbusAddress != 144 || reg144.IsWritable || deviceParameterReadScale(reg144) != 10 {
			return fmt.Errorf("профиль %s: V105.4 должен описывать register 144 как read-only default max sell-to-grid power, 10 W/raw", model.Name)
		}
	}
	return nil
}

func deviceParametersByCode(params []DeviceParameterFixture) map[string]DeviceParameterFields {
	result := make(map[string]DeviceParameterFields, len(params))
	for _, fixture := range params {
		code := strings.ToLower(strings.TrimSpace(fixture.Fields.Code))
		if code != "" {
			result[code] = fixture.Fields
		}
	}
	return result
}

func findDeviceParameterByCodeForModel(modelKey, code string) (InverterModelDefinition, DeviceParameterFields, error) {
	model, params, err := loadDeviceParametersForModel(modelKey)
	if err != nil {
		return model, DeviceParameterFields{}, err
	}
	field, ok := deviceParametersByCode(params)[strings.ToLower(strings.TrimSpace(code))]
	if !ok {
		return model, DeviceParameterFields{}, fmt.Errorf("code=%s не найден в профиле %s", code, model.ParametersFile)
	}
	return model, field, nil
}
