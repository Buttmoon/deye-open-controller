package main

import (
	"encoding/json"
	"html/template"
	"sort"
	"strconv"
	"strings"
)

type ChargeModeOption struct {
	Value int    `json:"value"`
	Label string `json:"label"`
}

func chargeModeOptionsFromFields(fields DeviceParameterFields) []ChargeModeOption {
	labels := map[int]string{}
	if len(fields.EnumValues) > 0 && string(fields.EnumValues) != "null" {
		var configured []ChargeModeOption
		if json.Unmarshal(fields.EnumValues, &configured) == nil {
			for _, option := range configured {
				if isCanonicalChargeModeValue(option.Value) && strings.TrimSpace(option.Label) != "" {
					labels[option.Value] = strings.TrimSpace(option.Label)
				}
			}
		}
	}
	allowed := append([]int(nil), fields.AllowedValues...)
	if len(allowed) == 0 {
		for value := range labels {
			allowed = append(allowed, value)
		}
	}
	if len(allowed) == 0 {
		for _, option := range defaultChargeModeOptions() {
			allowed = append(allowed, option.Value)
		}
	}
	canonicalLabels := map[int]string{}
	for _, option := range defaultChargeModeOptions() {
		canonicalLabels[option.Value] = option.Label
	}
	seen := map[int]bool{}
	result := make([]ChargeModeOption, 0, len(allowed))
	for _, value := range allowed {
		if seen[value] || !isCanonicalChargeModeValue(value) {
			continue
		}
		seen[value] = true
		label := labels[value]
		if label == "" {
			label = canonicalLabels[value]
		}
		result = append(result, ChargeModeOption{Value: value, Label: label})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Value < result[j].Value })
	return result
}

func chargeModeOptionsForModels(models []InverterModelDefinition) []ChargeModeOption {
	if len(models) == 0 {
		return defaultChargeModeOptions()
	}
	var intersection map[int]ChargeModeOption
	loadedModels := 0
	for _, model := range models {
		if strings.TrimSpace(model.Key) == "" {
			continue
		}
		_, params, err := loadDeviceParametersForModel(model.Key)
		if err != nil {
			return defaultChargeModeOptions()
		}
		loadedModels++
		field, ok := deviceParametersByCode(params)["charge_mode_point_1"]
		if !ok {
			return defaultChargeModeOptions()
		}
		options := chargeModeOptionsFromFields(field)
		current := make(map[int]ChargeModeOption, len(options))
		for _, option := range options {
			current[option.Value] = option
		}
		if intersection == nil {
			intersection = current
			continue
		}
		for value := range intersection {
			if _, ok := current[value]; !ok {
				delete(intersection, value)
			}
		}
	}
	if loadedModels == 0 {
		return defaultChargeModeOptions()
	}
	if len(intersection) == 0 {
		return []ChargeModeOption{{Value: 0, Label: "No Grid or Gen"}}
	}
	result := make([]ChargeModeOption, 0, len(intersection))
	for _, canonical := range defaultChargeModeOptions() {
		if option, ok := intersection[canonical.Value]; ok {
			if strings.TrimSpace(option.Label) == "" {
				option.Label = canonical.Label
			}
			result = append(result, option)
		}
	}
	return result
}

func chargeModeOptionsFromDeviceParameters() []ChargeModeOption {
	return chargeModeOptionsForModels(nil)
}

func defaultChargeModeOptions() []ChargeModeOption {
	return []ChargeModeOption{
		{Value: 0, Label: "No Grid or Gen or Sell"},
		{Value: 1, Label: "Allow Grid"},
		{Value: 2, Label: "Allow Gen"},
		{Value: 3, Label: "Allow Grid & Gen"},
		{Value: 32, Label: "Sell"},
		{Value: 33, Label: "Sell & Grid"},
		{Value: 34, Label: "Sell & Gen"},
		{Value: 35, Label: "Sell & Grid & Gen"},
	}
}

func isCanonicalChargeModeValue(value int) bool {
	switch value {
	case 0, 1, 2, 3, 32, 33, 34, 35:
		return true
	default:
		return false
	}
}

func chargeModeFromFlags(sell, grid, gen bool) int {
	value := 0
	if sell {
		value += 32
	}
	if grid {
		value += 1
	}
	if gen {
		value += 2
	}
	if isCanonicalChargeModeValue(value) {
		return value
	}
	return 0
}

func chargeModeOptionsJSONForModels(models []InverterModelDefinition) template.JS {
	data, err := json.Marshal(chargeModeOptionsForModels(models))
	if err != nil {
		data, _ = json.Marshal(defaultChargeModeOptions())
	}
	return template.JS(data)
}

func chargeModeOptionsJSON() template.JS {
	return chargeModeOptionsJSONForModels(nil)
}

func chargeModeLabelFromValue(value int) string {
	for _, option := range chargeModeOptionsFromDeviceParameters() {
		if option.Value == value {
			return option.Label
		}
	}
	for _, option := range defaultChargeModeOptions() {
		if option.Value == value {
			return option.Label
		}
	}
	return defaultChargeModeOptions()[0].Label
}

func chargeModeValueFromLabel(label string) (int, bool) {
	normalized := normalizeModeString(label)
	if normalized == "" {
		return 0, false
	}
	for _, option := range chargeModeOptionsFromDeviceParameters() {
		if normalizeModeString(option.Label) == normalized || strings.TrimSpace(label) == strings.TrimSpace(stringFromInt(option.Value)) {
			return option.Value, true
		}
	}
	return 0, false
}

func isConfiguredChargeModeValue(value int) bool {
	// Не привязываем сохранение/отправку к содержимому конкретного профильного JSON:
	// файл может быть старым, а значения Sell/Gen/Grid всё равно должны пройти.
	return isCanonicalChargeModeValue(value)
}

func chargeModeValidationLabels() []string {
	options := chargeModeOptionsFromDeviceParameters()
	labels := make([]string, 0, len(options))
	for _, option := range options {
		labels = append(labels, option.Label)
	}
	return labels
}

func stringFromInt(value int) string {
	return strconv.Itoa(value)
}
