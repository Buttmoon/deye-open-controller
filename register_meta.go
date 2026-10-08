package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
)

// RegisterBitDef documents one bit (or a group of adjacent bits) of a register.
type RegisterBitDef struct {
	Bit         int               `json:"bit"`
	Width       int               `json:"width,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
	Writable    bool              `json:"writable"`
	DerivedFrom string            `json:"derived_from,omitempty"`
}

func (b RegisterBitDef) width() int {
	if b.Width < 1 {
		return 1
	}
	return b.Width
}

func (b RegisterBitDef) mask() uint16 {
	return uint16(((1 << b.width()) - 1) << b.Bit)
}

const (
	kindBoolean = "boolean"
	kindUint    = "uint"
	kindInt     = "int"
	kindEnum    = "enum"
	kindBitmask = "bitmask"
	kindScaled  = "scaled"
	kindMulti   = "multi"
	kindTime    = "time"
)

var registerKindLabels = map[string]string{
	kindBoolean: "Логический (0/1)",
	kindUint:    "Целое без знака",
	kindInt:     "Целое со знаком",
	kindEnum:    "Перечисление",
	kindBitmask: "Битовая маска",
	kindScaled:  "Масштабированное число",
	kindMulti:   "Многорегистровое значение",
	kindTime:    "Время HHMM",
}

func maskBitCount(f DeviceParameterFields) int {
	if f.WriteBitmask != nil {
		return bits.OnesCount16(*f.WriteBitmask)
	}
	if f.Bitmask != nil {
		return bits.OnesCount16(uint16(*f.Bitmask))
	}
	return 0
}

// inferRegisterValueKind classifies a field. An explicit value_kind wins; the
// inference never classifies a multi-bit masked field as boolean, even when the
// profile lists only 0 and 1 as allowed values.
func inferRegisterValueKind(f DeviceParameterFields) string {
	if k := strings.ToLower(strings.TrimSpace(f.ValueKind)); k != "" {
		return k
	}
	if strings.EqualFold(f.Type, "time") {
		return kindTime
	}
	if f.RegisterCount > 1 || len(f.ModbusAddresses) > 1 {
		return kindMulti
	}
	if len(f.Bits) > 0 || (strings.EqualFold(f.WriteMode, "masked_bits") && maskBitCount(f) > 1) {
		return kindBitmask
	}
	if strings.EqualFold(f.WriteMode, "mapped_masked_bits") || len(f.WriteValues) > 0 {
		return kindEnum
	}
	if f.Bitmask != nil && bits.OnesCount16(uint16(*f.Bitmask)) == 1 {
		return kindBoolean
	}
	if len(f.AllowedValues) > 0 {
		if len(f.AllowedValues) == 2 && f.AllowedValues[0] == 0 && f.AllowedValues[1] == 1 {
			return kindBoolean
		}
		return kindEnum
	}
	if s := deviceParameterReadScale(f); s != 1 {
		return kindScaled
	}
	if f.Signed || strings.EqualFold(f.DataType, "int16") || strings.EqualFold(f.DataType, "int32") {
		return kindInt
	}
	return kindUint
}

// allowedValuesSupersededByBitLayout reports whether allowed_values must be
// ignored for a field because the profile documents it as a multi-bit mask.
// Validation then uses the write mask and raw bounds instead.
func allowedValuesSupersededByBitLayout(f DeviceParameterFields) bool {
	if !strings.EqualFold(strings.TrimSpace(f.WriteMode), "masked_bits") || maskBitCount(f) < 2 {
		return false
	}
	return len(f.Bits) > 0 || strings.EqualFold(f.ValueKind, kindBitmask)
}

// enrichProfileBitLayouts derives bit definitions for masked multi-bit fields
// from sibling single-bit fields on the same address (e.g. prog_monday_enabled
// with bitmask=2 on register 146). Only information already present in the
// profile is used.
func enrichProfileBitLayouts(params []DeviceParameterFixture) {
	byAddress := map[uint16][]int{}
	for i, p := range params {
		byAddress[p.Fields.ModbusAddress] = append(byAddress[p.Fields.ModbusAddress], i)
	}
	for i := range params {
		f := &params[i].Fields
		if len(f.Bits) > 0 || !strings.EqualFold(f.WriteMode, "masked_bits") || maskBitCount(*f) < 2 {
			continue
		}
		derived := []RegisterBitDef{}
		for _, j := range byAddress[f.ModbusAddress] {
			if j == i {
				continue
			}
			s := params[j].Fields
			if s.Bitmask == nil || bits.OnesCount16(uint16(*s.Bitmask)) != 1 {
				continue
			}
			bit := bits.TrailingZeros16(uint16(*s.Bitmask))
			derived = append(derived, RegisterBitDef{
				Bit: bit, Width: 1, Name: s.Name, Description: s.Description,
				Values: map[string]string{"0": "0", "1": "1"}, Writable: f.WriteBitmask != nil && *f.WriteBitmask&uint16(*s.Bitmask) != 0,
				DerivedFrom: s.Code,
			})
		}
		if len(derived) == 0 {
			continue
		}
		sort.Slice(derived, func(a, b int) bool { return derived[a].Bit < derived[b].Bit })
		f.Bits = derived
	}
}

type ProfileIssue struct {
	Severity string `json:"severity"` // error | warning | info
	Code     string `json:"code,omitempty"`
	Address  int    `json:"address,omitempty"`
	Message  string `json:"message"`
}

// validateProfileStructure performs schema-level checks that every profile must
// pass to be installed. It is deliberately independent from the stricter,
// model-specific safety checks in validateDeviceParameterProfile, which still
// gate every write.
func validateProfileStructure(params []DeviceParameterFixture) []ProfileIssue {
	issues := []ProfileIssue{}
	add := func(sev, code string, addr uint16, format string, args ...any) {
		issues = append(issues, ProfileIssue{Severity: sev, Code: code, Address: int(addr), Message: fmt.Sprintf(format, args...)})
	}
	if len(params) == 0 {
		add("error", "", 0, "профиль пуст")
		return issues
	}
	seen := map[string]bool{}
	for _, p := range params {
		f := p.Fields
		code := strings.ToLower(strings.TrimSpace(f.Code))
		if code == "" {
			add("error", "", f.ModbusAddress, "параметр без code (адрес %d)", f.ModbusAddress)
			continue
		}
		if seen[code] {
			add("error", code, f.ModbusAddress, "повторяющийся code=%s", code)
		}
		seen[code] = true
		if f.RegisterCount <= 0 {
			add("error", code, f.ModbusAddress, "register_count должен быть больше 0")
		}
		if f.RegisterCount > 4 {
			add("error", code, f.ModbusAddress, "register_count=%d слишком велик (максимум 4)", f.RegisterCount)
		}
		if _, ok := getRegisterType(f.RegisterType); !ok {
			add("error", code, f.ModbusAddress, "неподдерживаемый register_type=%q", f.RegisterType)
		}
		if int(f.ModbusAddress)+f.RegisterCount-1 > 65535 {
			add("error", code, f.ModbusAddress, "диапазон адресов выходит за 65535")
		}
		if dt := strings.ToLower(strings.TrimSpace(f.DataType)); dt != "" && dt != "uint16" && dt != "int16" && dt != "uint32" && dt != "int32" {
			add("error", code, f.ModbusAddress, "неподдерживаемый data_type=%q", f.DataType)
		}
		if f.WriteScaleFactor != nil && *f.WriteScaleFactor == 0 {
			add("error", code, f.ModbusAddress, "write_scale_factor не может быть 0")
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			add("error", code, f.ModbusAddress, "min (%v) больше max (%v)", *f.Min, *f.Max)
		}
		if f.RawMin != nil && f.RawMax != nil && *f.RawMin > *f.RawMax {
			add("error", code, f.ModbusAddress, "raw_min (%v) больше raw_max (%v)", *f.RawMin, *f.RawMax)
		}
		if f.IsWritable && !strings.EqualFold(f.RegisterType, "holding") {
			add("error", code, f.ModbusAddress, "записывать можно только holding-регистры")
		}
		wm := strings.ToLower(strings.TrimSpace(f.WriteMode))
		if wm != "" && wm != "masked_bits" && wm != "mapped_masked_bits" {
			add("error", code, f.ModbusAddress, "неизвестный write_mode=%q", f.WriteMode)
		}
		if (wm == "masked_bits" || wm == "mapped_masked_bits") && (f.WriteBitmask == nil || *f.WriteBitmask == 0) {
			add("error", code, f.ModbusAddress, "write_mode=%s требует ненулевой write_bitmask", wm)
		}
		if wm == "mapped_masked_bits" && f.WriteBitmask != nil {
			for k, v := range f.WriteValues {
				if v&^*f.WriteBitmask != 0 {
					add("error", code, f.ModbusAddress, "write_values[%s]=0x%04X выходит за write_bitmask 0x%04X", k, v, *f.WriteBitmask)
				}
			}
		}
		for _, b := range f.Bits {
			if b.Bit < 0 || b.Bit+b.width() > 16*max(1, f.RegisterCount) {
				add("error", code, f.ModbusAddress, "бит %d (ширина %d) вне регистра", b.Bit, b.width())
			}
		}
		if wm == "masked_bits" && maskBitCount(f) > 1 && isBooleanOnlyAllowed(f.AllowedValues) {
			add("warning", code, f.ModbusAddress, "allowed_values=%v для многобитовой маски 0x%04X: ограничение игнорируется, проверяются маска и raw-диапазон", f.AllowedValues, *f.WriteBitmask)
		}
		if f.IsWritable && f.IsActive && (!f.EnforceBounds || f.RawMin == nil || f.RawMax == nil) {
			add("warning", code, f.ModbusAddress, "для записываемого параметра не заданы enforce_bounds/raw_min/raw_max — запись будет заблокирована проверкой безопасности")
		}
		if strings.TrimSpace(f.Confidence) == "" {
			add("info", code, f.ModbusAddress, "не указан уровень достоверности (confidence)")
		}
	}
	return issues
}

func isBooleanOnlyAllowed(values []int) bool {
	if len(values) == 0 || len(values) > 2 {
		return false
	}
	for _, v := range values {
		if v != 0 && v != 1 {
			return false
		}
	}
	return true
}

func profileIssuesHaveErrors(issues []ProfileIssue) bool {
	for _, i := range issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}

// RegisterDescription is everything the UI shows about one register.
type RegisterDescription struct {
	Code               string           `json:"code"`
	Name               string           `json:"name"`
	Description        string           `json:"description"`
	Address            int              `json:"address"`
	Addresses          []uint16         `json:"addresses"`
	RegisterType       string           `json:"register_type"`
	DataType           string           `json:"data_type"`
	Width              int              `json:"width"`
	Signed             bool             `json:"signed"`
	Writable           bool             `json:"writable"`
	Active             bool             `json:"active"`
	Kind               string           `json:"kind"`
	KindLabel          string           `json:"kind_label"`
	RawMin             *float64         `json:"raw_min,omitempty"`
	RawMax             *float64         `json:"raw_max,omitempty"`
	Min                *float64         `json:"min,omitempty"`
	Max                *float64         `json:"max,omitempty"`
	Step               *float64         `json:"step,omitempty"`
	ReadScale          float64          `json:"read_scale"`
	WriteScale         float64          `json:"write_scale"`
	ScheduleScale      float64          `json:"schedule_scale"`
	Offset             float64          `json:"offset"`
	Unit               string           `json:"unit"`
	AllowedValues      []int            `json:"allowed_values,omitempty"`
	AllowedIgnored     bool             `json:"allowed_values_ignored"`
	EnumLabels         map[string]string `json:"enum_labels,omitempty"`
	Bits               []RegisterBitDef `json:"bits,omitempty"`
	BitmaskRead        *int             `json:"bitmask_read,omitempty"`
	WriteMode          string           `json:"write_mode,omitempty"`
	WriteBitmask       *uint16          `json:"write_bitmask,omitempty"`
	WriteValues        map[string]uint16 `json:"write_values,omitempty"`
	Source             string           `json:"source"`
	Confidence         string           `json:"confidence"`
	ConfidenceLevel    string           `json:"confidence_level"`
	ProtocolVersion    string           `json:"protocol_version"`
	Notes              string           `json:"notes"`
	ModelKey           string           `json:"model_key"`
	ModelName          string           `json:"model_name"`
	CompatibleModels   []string         `json:"compatible_models"`
	SharedAddressCodes []string         `json:"shared_address_codes,omitempty"`
	Incomplete         []string         `json:"incomplete,omitempty"`
	Warnings           []string         `json:"warnings,omitempty"`
}

// confidenceLevel maps free-text confidence strings to a three-step badge.
func confidenceLevel(confidence string) string {
	c := strings.ToLower(confidence)
	switch {
	case c == "":
		return "unknown"
	case strings.Contains(c, "physically_verified") || strings.Contains(c, "device_verified") || strings.Contains(c, "confirmed"):
		return "verified"
	case strings.Contains(c, "requires") || strings.Contains(c, "unverified") || strings.Contains(c, "not_model_verified") || strings.Contains(c, "experimental"):
		return "unverified"
	case strings.Contains(c, "manufacturer"):
		return "documented"
	}
	return "unverified"
}

func enumLabelsFromField(f DeviceParameterFields) map[string]string {
	if len(f.EnumValues) == 0 || string(f.EnumValues) == "null" {
		return nil
	}
	var list []struct {
		Value any    `json:"value"`
		Label string `json:"label"`
	}
	if json.Unmarshal(f.EnumValues, &list) == nil && len(list) > 0 {
		out := map[string]string{}
		for _, e := range list {
			out[fmt.Sprint(e.Value)] = e.Label
		}
		return out
	}
	var m map[string]string
	if json.Unmarshal(f.EnumValues, &m) == nil && len(m) > 0 {
		return m
	}
	return nil
}

func describeRegisterField(model InverterModelDefinition, params []DeviceParameterFixture, f DeviceParameterFields) RegisterDescription {
	unit := ""
	if f.Suffix != nil {
		unit = *f.Suffix
	}
	dataType := strings.ToLower(strings.TrimSpace(f.DataType))
	if dataType == "" {
		switch {
		case f.RegisterCount >= 2:
			dataType = "uint32"
		case f.Signed:
			dataType = "int16"
		default:
			dataType = "uint16"
		}
	}
	offset := 0.0
	if f.Offset != nil {
		offset = *f.Offset
	}
	kind := inferRegisterValueKind(f)
	d := RegisterDescription{
		Code: f.Code, Name: f.Name, Description: f.Description, Address: int(f.ModbusAddress), Addresses: deviceParameterAddresses(f),
		RegisterType: strings.ToLower(f.RegisterType), DataType: dataType, Width: max(1, f.RegisterCount),
		Signed: f.Signed || strings.HasPrefix(dataType, "int"), Writable: f.IsWritable, Active: f.IsActive,
		Kind: kind, KindLabel: registerKindLabels[kind],
		RawMin: f.RawMin, RawMax: f.RawMax, Min: f.Min, Max: f.Max, Step: f.Step,
		ReadScale: deviceParameterReadScale(f), WriteScale: deviceParameterWriteScale(f), ScheduleScale: deviceParameterScheduleInputScale(f),
		Offset: offset, Unit: unit, AllowedValues: f.AllowedValues, AllowedIgnored: allowedValuesSupersededByBitLayout(f),
		EnumLabels: enumLabelsFromField(f), Bits: f.Bits, BitmaskRead: f.Bitmask, WriteMode: f.WriteMode, WriteBitmask: f.WriteBitmask,
		WriteValues: f.WriteValues, Source: f.Source, Confidence: f.Confidence, ConfidenceLevel: confidenceLevel(f.Confidence),
		ProtocolVersion: f.ProtocolVersionOrVariant(), Notes: f.Notes, ModelKey: model.Key, ModelName: model.Name,
	}
	if d.KindLabel == "" {
		d.KindLabel = kind
	}
	for _, p := range params {
		if p.Fields.ModbusAddress == f.ModbusAddress && !strings.EqualFold(p.Fields.Code, f.Code) {
			d.SharedAddressCodes = append(d.SharedAddressCodes, p.Fields.Code)
		}
	}
	if models, err := availableInverterModels(); err == nil {
		for _, m := range models {
			_, mp, err := loadDeviceParametersForModelStructural(m.Key)
			if err != nil {
				continue
			}
			for _, p := range mp {
				if strings.EqualFold(p.Fields.Code, f.Code) && p.Fields.ModbusAddress == f.ModbusAddress {
					d.CompatibleModels = append(d.CompatibleModels, m.Name)
					break
				}
			}
		}
	}
	if f.Description == "" {
		d.Incomplete = append(d.Incomplete, "нет описания")
	}
	if f.Suffix == nil && kind != kindBoolean && kind != kindEnum && kind != kindBitmask {
		d.Incomplete = append(d.Incomplete, "не указана единица измерения")
	}
	if f.RawMin == nil || f.RawMax == nil {
		d.Incomplete = append(d.Incomplete, "не задан raw-диапазон")
	}
	if f.Min == nil || f.Max == nil {
		d.Incomplete = append(d.Incomplete, "не задан диапазон инженерных значений")
	}
	if kind == kindBitmask && len(f.Bits) == 0 {
		d.Incomplete = append(d.Incomplete, "раскладка битов не описана")
	}
	if kind == kindEnum && len(d.EnumLabels) == 0 && len(f.WriteValues) == 0 {
		d.Incomplete = append(d.Incomplete, "нет расшифровки значений перечисления")
	}
	if strings.TrimSpace(f.Source) == "" {
		d.Incomplete = append(d.Incomplete, "не указан источник")
	}
	if d.AllowedIgnored {
		mask := 0
		if f.WriteBitmask != nil {
			mask = int(*f.WriteBitmask)
		} else if f.Bitmask != nil {
			mask = *f.Bitmask
		}
		d.Warnings = append(d.Warnings, fmt.Sprintf("allowed_values=%v противоречит битовой раскладке и не применяется; проверяются маска 0x%04X и raw-диапазон", f.AllowedValues, mask))
	}
	if kind == kindBitmask && len(f.Bits) > 0 && f.Bits[0].DerivedFrom != "" {
		d.Warnings = append(d.Warnings, "раскладка битов получена из соседних полей профиля ("+f.Bits[0].DerivedFrom+" и др.)")
	}
	if d.ConfidenceLevel != "verified" {
		d.Warnings = append(d.Warnings, "описание регистра не подтверждено физической проверкой на этом инверторе")
	}
	return d
}

func (f DeviceParameterFields) ProtocolVersionOrVariant() string {
	parts := []string{}
	if v := strings.TrimSpace(f.ProtocolVersion); v != "" {
		parts = append(parts, v)
	}
	if v := strings.TrimSpace(f.ProfileVariant); v != "" {
		parts = append(parts, v)
	}
	return strings.Join(parts, " · ")
}

// DecodedRegisterValue is the interpretation of a raw register value.
type DecodedRegisterValue struct {
	Raw        []uint16          `json:"raw"`
	Hex        []string          `json:"hex"`
	Binary     []string          `json:"binary"`
	Signed     []int16           `json:"signed"`
	Decoded    *float64          `json:"decoded,omitempty"`
	DecodedStr string            `json:"decoded_text"`
	Unit       string            `json:"unit,omitempty"`
	Bits       []DecodedBit      `json:"bits,omitempty"`
	Violations []string          `json:"violations,omitempty"`
	EnumLabel  string            `json:"enum_label,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
}

type DecodedBit struct {
	Bit      int    `json:"bit"`
	Width    int    `json:"width"`
	Name     string `json:"name"`
	Value    int    `json:"value"`
	Meaning  string `json:"meaning"`
	Writable bool   `json:"writable"`
}

// rawValueForBounds returns the part of a register word that raw_min/raw_max
// describe. Bits outside a masked_bits write mask belong to other functions of
// the register; mapped bitfields are validated against write_values instead,
// so ok is false for them.
func rawValueForBounds(f DeviceParameterFields, word uint16) (uint16, bool) {
	switch strings.ToLower(strings.TrimSpace(f.WriteMode)) {
	case "mapped_masked_bits":
		return word, false
	case "masked_bits":
		if f.WriteBitmask != nil {
			return word & *f.WriteBitmask, true
		}
	}
	return word, true
}

func formatBinary16(v uint16) string {
	s := fmt.Sprintf("%016b", v)
	return s[0:4] + " " + s[4:8] + " " + s[8:12] + " " + s[12:16]
}

// decodeRegisterValue interprets raw words using a field definition. With a nil
// field only the raw representations are produced.
func decodeRegisterValue(raw []uint16, f *DeviceParameterFields) DecodedRegisterValue {
	out := DecodedRegisterValue{Raw: raw}
	for _, v := range raw {
		out.Hex = append(out.Hex, fmt.Sprintf("0x%04X", v))
		out.Binary = append(out.Binary, formatBinary16(v))
		out.Signed = append(out.Signed, int16(v))
	}
	if f == nil || len(raw) == 0 {
		out.DecodedStr = "описание регистра отсутствует — показаны только raw-значения"
		return out
	}
	if f.Suffix != nil {
		out.Unit = *f.Suffix
	}
	kind := inferRegisterValueKind(*f)
	word := raw[0]
	if f.Bitmask != nil && kind != kindBitmask {
		mask := uint16(*f.Bitmask)
		shift := bits.TrailingZeros16(mask)
		v := float64((word & mask) >> shift)
		out.Decoded = &v
		out.DecodedStr = strconv.FormatFloat(v, 'f', -1, 64)
	} else if value, err := decodeDeviceParameterRegisters(raw, *f); err == nil {
		out.Decoded = &value
		out.DecodedStr = strconv.FormatFloat(value, 'f', -1, 64)
		if out.Unit != "" {
			out.DecodedStr += " " + out.Unit
		}
	} else {
		out.DecodedStr = "ошибка декодирования: " + err.Error()
	}
	if kind == kindTime {
		out.DecodedStr = fmt.Sprintf("%02d:%02d", word/100, word%100)
	}
	labels := enumLabelsFromField(*f)
	if strings.EqualFold(f.WriteMode, "mapped_masked_bits") && f.WriteBitmask != nil {
		masked := word & *f.WriteBitmask
		for logical, mapped := range f.WriteValues {
			if mapped == masked {
				out.EnumLabel = logical
				if l, ok := labels[logical]; ok {
					out.EnumLabel = logical + " — " + l
				}
				out.DecodedStr = out.EnumLabel
			}
		}
		if out.EnumLabel == "" {
			out.Violations = append(out.Violations, fmt.Sprintf("биты 0x%04X = 0x%04X не соответствуют ни одному известному значению write_values", *f.WriteBitmask, masked))
		}
	} else if labels != nil && out.Decoded != nil && kind != kindBitmask {
		if l, ok := labels[strconv.FormatFloat(*out.Decoded, 'f', -1, 64)]; ok {
			out.EnumLabel = l
			out.DecodedStr += " — " + l
		}
	}
	for _, b := range f.Bits {
		v := int((word & b.mask()) >> b.Bit)
		meaning := b.Values[strconv.Itoa(v)]
		if meaning == "" && len(b.Values) > 0 {
			meaning = "значение не описано в профиле"
		}
		out.Bits = append(out.Bits, DecodedBit{Bit: b.Bit, Width: b.width(), Name: b.Name, Value: v, Meaning: meaning, Writable: b.Writable})
	}
	if kind == kindBitmask {
		out.DecodedStr = fmt.Sprintf("маска %s (%d)", formatBinary16(word), word)
	}
	bounded, checkBounds := rawValueForBounds(*f, word)
	if checkBounds && f.RawMin != nil && float64(bounded) < *f.RawMin && !f.Signed {
		out.Violations = append(out.Violations, fmt.Sprintf("raw %d меньше raw_min %v из профиля", bounded, *f.RawMin))
	}
	if checkBounds && f.RawMax != nil && float64(bounded) > *f.RawMax && !f.Signed && f.RegisterCount <= 1 {
		out.Violations = append(out.Violations, fmt.Sprintf("raw %d больше raw_max %v из профиля", bounded, *f.RawMax))
	}
	if len(f.AllowedValues) > 0 && !allowedValuesSupersededByBitLayout(*f) && out.Decoded != nil && kind != kindEnum {
		found := false
		for _, a := range f.AllowedValues {
			if math.Abs(float64(a)-*out.Decoded) < 1e-9 {
				found = true
			}
		}
		if !found {
			out.Violations = append(out.Violations, fmt.Sprintf("значение %s отсутствует в allowed_values=%v", out.DecodedStr, f.AllowedValues))
		}
	}
	return out
}

// loadDeviceParametersForModelStructural loads a profile for read-only use.
// It requires only structural validity, so newly installed or unverified
// profiles can be inspected and read while writes stay blocked by the full
// safety validation in loadDeviceParametersForModel.
func loadDeviceParametersForModelStructural(modelKey string) (InverterModelDefinition, []DeviceParameterFixture, error) {
	model, err := findInverterModel(modelKey)
	if err != nil {
		return InverterModelDefinition{}, nil, err
	}
	params, err := LoadDeviceParameters(model.ParametersFile)
	if err != nil {
		return model, nil, fmt.Errorf("ошибка чтения профиля %s (%s): %w", model.Name, model.ParametersFile, err)
	}
	if issues := validateProfileStructure(params); profileIssuesHaveErrors(issues) {
		for _, i := range issues {
			if i.Severity == "error" {
				return model, nil, fmt.Errorf("профиль %s структурно некорректен: %s", model.Name, i.Message)
			}
		}
	}
	return model, params, nil
}
