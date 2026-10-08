package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const defaultScheduleDays = 31

const defaultUseTimerMask = 0

func sellTimeForHour(hour int) int {
	if hour < 0 {
		hour = 0
	}
	if hour > 23 {
		hour = hour % 24
	}
	return hour * 100
}

func sellTimeForSlot(hour, minute int) int {
	if hour < 0 {
		hour = 0
	}
	if hour > 23 {
		hour = hour % 24
	}
	if minute < 0 {
		minute = 0
	}
	if minute > 59 {
		minute = 59
	}
	return hour*100 + minute
}

func minuteSlotLabel(hour, minute int) string {
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func slotIndex(hour, minute int) int {
	if minute%5 != 0 {
		minute = (minute / 5) * 5
	}
	return hour*12 + minute/5
}

func defaultMinuteSlot(hour, minute int) MinuteSlot {
	point := pointForHour(hour)
	return MinuteSlot{
		Hour:                 hour,
		Minute:               minute,
		Label:                minuteSlotLabel(hour, minute),
		Point:                point,
		SellTime:             sellTimeForSlot(hour, minute),
		Enabled:              false,
		SellModeKW:           0,
		SellModeBattCapacity: 100,
		ChargeMode:           0,
		GridExportLimit:      0,
		GridChargeEnabled:    false,
		SolarExport:          false,
		LoadLimitMode:        0,
		UseTimer:             false,
		PriorityLoad:         0,
	}
}

func pointForHour(hour int) int {
	switch {
	case hour >= 0 && hour < 5:
		return 1
	case hour >= 5 && hour < 9:
		return 2
	case hour >= 9 && hour < 13:
		return 3
	case hour >= 13 && hour < 17:
		return 4
	case hour >= 17 && hour < 21:
		return 5
	default:
		return 6
	}
}

func hourLabel(hour int) string {
	return fmt.Sprintf("%02d:00", hour)
}

func defaultHourConfig(hour int) HourConfig {
	point := pointForHour(hour)
	return HourConfig{
		Hour:                 hour,
		Label:                hourLabel(hour),
		Point:                point,
		SellTime:             sellTimeForHour(hour),
		Enabled:              false,
		SellModeKW:           0,
		SellModeBattCapacity: 100,
		ChargeMode:           0,
		GridExportLimit:      0,
		GridChargeEnabled:    false,
		SolarExport:          false,
		LoadLimitMode:        0,
		UseTimer:             false,
		PriorityLoad:         0,
	}
}

func defaultDayItem(day int) DayItem {
	hours := make([]HourConfig, 0, 24)
	for h := 0; h < 24; h++ {
		hours = append(hours, defaultHourConfig(h))
	}
	slots := make([]MinuteSlot, 0, 288)
	for h := 0; h < 24; h++ {
		for m := 0; m < 60; m += 5 {
			slots = append(slots, defaultMinuteSlot(h, m))
		}
	}
	return DayItem{Day: day, Enabled: false, Hours: hours, Slots: slots}
}

func defaultSchedulePayload() SchedulePayload {
	payload := SchedulePayload{UseTimerMask: defaultUseTimerMask, Days: make([]DayItem, 0, defaultScheduleDays)}
	for i := 1; i <= defaultScheduleDays; i++ {
		payload.Days = append(payload.Days, defaultDayItem(i))
	}
	return payload
}

func defaultScheduleJSON() string {
	payload := defaultSchedulePayload()
	b, _ := json.Marshal(payload)
	return string(b)
}

func defaultScheduleJSONForEditor() string {
	return `{"use_timer_mask":0,"days":[]}`
}

func minuteSlotFromHourConfig(h HourConfig, minute int) MinuteSlot {
	return MinuteSlot{Hour: h.Hour, Minute: minute, Label: minuteSlotLabel(h.Hour, minute), Point: pointForHour(h.Hour), SellTime: sellTimeForSlot(h.Hour, minute), Enabled: h.Enabled, SellModeKW: h.SellModeKW, SellModeBattCapacity: h.SellModeBattCapacity, ChargeMode: h.ChargeMode, GridExportLimit: h.GridExportLimit, GridChargeEnabled: h.GridChargeEnabled, SolarExport: h.SolarExport, LoadLimitMode: h.LoadLimitMode, UseTimer: h.UseTimer, UseTimerMask: h.UseTimerMask, PriorityLoad: h.PriorityLoad}
}

func normalizeMinuteSlotFields(s *MinuteSlot, fallbackIndex int, mask int) {
	if s.Hour < 0 || s.Hour > 23 {
		s.Hour = fallbackIndex / 12
	}
	if s.Minute < 0 || s.Minute > 59 {
		s.Minute = (fallbackIndex % 12) * 5
	}
	s.Minute = (s.Minute / 5) * 5
	s.Label = minuteSlotLabel(s.Hour, s.Minute)
	s.Point = pointForHour(s.Hour)
	s.SellTime = sellTimeForSlot(s.Hour, s.Minute)
	if s.SellModeBattCapacity < 0 {
		s.SellModeBattCapacity = 0
	}
	if s.SellModeBattCapacity > 100 {
		s.SellModeBattCapacity = 100
	}
	if !isAllowedChargeMode(s.ChargeMode) {
		s.ChargeMode = 0
	}
	if s.LoadLimitMode < 0 || s.LoadLimitMode > 2 {
		s.LoadLimitMode = 0
	}
	s.PriorityLoad = normalizePriorityLoadValue(s.PriorityLoad)
	if s.GridExportLimit < 0 {
		s.GridExportLimit = 0
	}
	if s.SellModeKW < 0 {
		s.SellModeKW = 0
	}
	s.UseTimerMask = mask
	s.UseTimer = mask&1 != 0
}

func minuteSlotDiffersFromHour(s MinuteSlot, h HourConfig) bool {
	if s.DefaultCustomSlot {
		return false
	}
	return s.SellModeKW != h.SellModeKW || s.SellModeBattCapacity != h.SellModeBattCapacity || s.ChargeMode != h.ChargeMode || s.GridExportLimit != h.GridExportLimit || s.GridChargeEnabled != h.GridChargeEnabled || s.SolarExport != h.SolarExport || s.LoadLimitMode != h.LoadLimitMode || s.PriorityLoad != h.PriorityLoad || s.Enabled != h.Enabled
}

func normalizeSchedulePayloadSellTimes(payload *SchedulePayload) {
	if payload == nil {
		return
	}
	payload.UseTimerMask = normalizeUseTimerMask(payload.UseTimerMask)
	if len(payload.Days) != defaultScheduleDays {
		oldDays := payload.Days
		payload.Days = make([]DayItem, defaultScheduleDays)
		for day := 1; day <= defaultScheduleDays; day++ {
			payload.Days[day-1] = defaultDayItem(day)
			for _, existing := range oldDays {
				if existing.Day == day {
					payload.Days[day-1] = existing
					break
				}
			}
		}
	}
	for di := range payload.Days {
		if payload.Days[di].Day < 1 || payload.Days[di].Day > 31 {
			payload.Days[di].Day = di + 1
		}
		if len(payload.Days[di].Hours) != 24 {
			old := payload.Days[di].Hours
			payload.Days[di].Hours = make([]HourConfig, 24)
			for h := 0; h < 24; h++ {
				payload.Days[di].Hours[h] = defaultHourConfig(h)
				for _, existing := range old {
					if existing.Hour == h {
						payload.Days[di].Hours[h] = existing
						break
					}
				}
			}
		}
		slotsWereExpanded := len(payload.Days[di].Slots) != 288
		if slotsWereExpanded {
			old := payload.Days[di].Slots
			slotsMap := map[int]*MinuteSlot{}
			for i := range old {
				slotsMap[slotIndex(old[i].Hour, old[i].Minute)] = &old[i]
			}
			slots := make([]MinuteSlot, 288)
			for h := 0; h < 24; h++ {
				for m := 0; m < 60; m += 5 {
					idx := slotIndex(h, m)
					if s, ok := slotsMap[idx]; ok {
						slots[idx] = *s
					} else {
						slots[idx] = defaultMinuteSlot(h, m)
					}
				}
			}
			payload.Days[di].Slots = slots
		}
		for hi := range payload.Days[di].Hours {
			h := &payload.Days[di].Hours[hi]
			if h.Hour < 0 || h.Hour > 23 {
				h.Hour = hi
			}
			h.Label = hourLabel(h.Hour)
			h.Point = pointForHour(h.Hour)
			h.SellTime = sellTimeForHour(h.Hour)
			if h.SellModeBattCapacity < 0 {
				h.SellModeBattCapacity = 0
			}
			if h.SellModeBattCapacity > 100 {
				h.SellModeBattCapacity = 100
			}
			if !isAllowedChargeMode(h.ChargeMode) {
				h.ChargeMode = 0
			}
			if h.LoadLimitMode < 0 || h.LoadLimitMode > 2 {
				h.LoadLimitMode = 0
			}
			h.PriorityLoad = normalizePriorityLoadValue(h.PriorityLoad)
			if h.GridExportLimit < 0 {
				h.GridExportLimit = 0
			}
			if h.SellModeKW < 0 {
				h.SellModeKW = 0
			}
			h.UseTimerMask = payload.UseTimerMask
			h.UseTimer = payload.UseTimerMask&1 != 0
		}
		if slotsWereExpanded {
			for si := range payload.Days[di].Slots {
				hour := si / 12
				minute := (si % 12) * 5
				payload.Days[di].Slots[si] = minuteSlotFromHourConfig(payload.Days[di].Hours[hour], minute)
			}
		}
		for si := range payload.Days[di].Slots {
			normalizeMinuteSlotFields(&payload.Days[di].Slots[si], si, payload.UseTimerMask)
		}
		if len(payload.Days[di].CustomSlots) > 0 {
			normalizedCustom := make([]MinuteSlot, 0, len(payload.Days[di].CustomSlots))
			seen := map[int]bool{}
			for _, cs := range payload.Days[di].CustomSlots {
				if cs.Hour < 0 || cs.Hour > 23 || cs.Minute < 0 || cs.Minute > 59 {
					continue
				}
				idx := slotIndex(cs.Hour, cs.Minute)
				if idx < 0 || idx >= len(payload.Days[di].Slots) || seen[idx] {
					continue
				}
				normalizeMinuteSlotFields(&cs, idx, payload.UseTimerMask)
				payload.Days[di].Slots[idx] = cs
				normalizedCustom = append(normalizedCustom, cs)
				seen[idx] = true
			}
			payload.Days[di].CustomSlots = normalizedCustom
		}
		dayEnabled := false
		for _, h := range payload.Days[di].Hours {
			if h.Enabled {
				dayEnabled = true
				break
			}
		}
		if !dayEnabled {
			for _, s := range payload.Days[di].Slots {
				if s.Enabled {
					dayEnabled = true
					break
				}
			}
		}
		payload.Days[di].Enabled = dayEnabled
	}
}

func compactSchedulePayloadForEditor(payload SchedulePayload) SchedulePayload {
	normalizeSchedulePayloadSellTimes(&payload)
	compact := SchedulePayload{UseTimerMask: payload.UseTimerMask, Days: make([]DayItem, 0, len(payload.Days))}
	for _, day := range payload.Days {
		outDay := DayItem{Day: day.Day, Enabled: day.Enabled, Hours: day.Hours}
		customSlots := make([]MinuteSlot, 0)

		// В компактном формате CustomSlots является явным списком пользовательских
		// 5-минутных переопределений. Если он присутствует, не выводим остальные
		// 288 слотов как отличающиеся только потому, что они ещё содержат значения
		// старого полного формата и не были повторно унаследованы от изменённого часа.
		slotsToCompact := day.Slots
		if len(day.CustomSlots) > 0 {
			slotsToCompact = day.CustomSlots
		}
		seen := map[int]bool{}
		for _, slot := range slotsToCompact {
			if slot.Minute == 0 || slot.Hour < 0 || slot.Hour >= len(day.Hours) || slot.DefaultCustomSlot {
				continue
			}
			idx := slotIndex(slot.Hour, slot.Minute)
			if seen[idx] {
				continue
			}
			seen[idx] = true
			if minuteSlotDiffersFromHour(slot, day.Hours[slot.Hour]) {
				customSlots = append(customSlots, slot)
			}
		}
		if len(customSlots) > 0 {
			outDay.CustomSlots = customSlots
		}
		compact.Days = append(compact.Days, outDay)
	}
	return compact
}

func compactScheduleJSONForEditor(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return defaultScheduleJSONForEditor()
	}
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		return raw
	}
	b, err := json.Marshal(compactSchedulePayloadForEditor(payload))
	if err != nil {
		return raw
	}
	return string(b)
}

func normalizeScheduleJSONCompact(raw string) (string, error) {
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(compactSchedulePayloadForEditor(payload))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func normalizeScheduleJSONSellTimes(raw string) (string, error) {
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		return "", err
	}
	normalizeSchedulePayloadSellTimes(&payload)
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func isValidTOUHHMM(value int) bool {
	if value < 0 || value > 2355 {
		return false
	}
	hour := value / 100
	minute := value % 100
	return hour >= 0 && hour <= 23 && minute >= 0 && minute <= 55 && minute%5 == 0
}

func isValidTOUChargeMode(value int) bool {
	switch value {
	case 0, 1, 2, 3, 32, 33, 34, 35:
		return true
	default:
		return false
	}
}

func allowedTOUChargeModesForModels(models []InverterModelDefinition) map[int]bool {
	allowed := make(map[int]bool)
	for _, option := range chargeModeOptionsForModels(models) {
		allowed[option.Value] = true
	}
	return allowed
}

func isAllowedTOUChargeMode(value int, allowed map[int]bool) bool {
	if len(allowed) == 0 {
		return isValidTOUChargeMode(value)
	}
	return allowed[value]
}

func allowedTOUChargeModeValues(allowed map[int]bool) []int {
	values := make([]int, 0, len(allowed))
	for _, option := range defaultChargeModeOptions() {
		if allowed[option.Value] {
			values = append(values, option.Value)
		}
	}
	return values
}

func validateScheduleConfigValues(label string, sellTime, sellModeKW, soc, chargeMode, gridExport, loadMode, priority int, schedulePowerMaxW, gridExportMaxW int, allowedChargeModes map[int]bool) error {
	if !isValidTOUHHMM(sellTime) {
		return fmt.Errorf("%s: некорректное время TOU=%d; нужен HHMM 00:00–23:55 с шагом 5 минут", label, sellTime)
	}
	if sellModeKW < 0 || sellModeKW*10 > schedulePowerMaxW {
		return fmt.Errorf("%s: мощность батареи %d W выходит за предел 0–%d W", label, sellModeKW*10, schedulePowerMaxW)
	}
	if gridExport < 0 || gridExport*10 > gridExportMaxW {
		return fmt.Errorf("%s: лимит экспорта %d W выходит за предел 0–%d W", label, gridExport*10, gridExportMaxW)
	}
	if soc < 0 || soc > 100 {
		return fmt.Errorf("%s: SOC должен быть 0–100%%", label)
	}
	if !isAllowedTOUChargeMode(chargeMode, allowedChargeModes) {
		return fmt.Errorf("%s: charge_mode=%d недопустим для выбранного профиля; разрешены %v", label, chargeMode, allowedTOUChargeModeValues(allowedChargeModes))
	}
	if loadMode < 0 || loadMode > 2 {
		return fmt.Errorf("%s: load_limit_mode должен быть 0–2", label)
	}
	if priority < 0 || priority > 1 {
		return fmt.Errorf("%s: priority_load должен быть 0 или 1", label)
	}
	return nil
}

func validateExplicitScheduleFields(raw string, schedulePowerMaxW, gridExportMaxW int, allowedChargeModes map[int]bool) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var root any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return err
	}
	normalizeKey := func(key string) string {
		key = strings.ToLower(strings.TrimSpace(key))
		key = strings.ReplaceAll(key, "_", "")
		key = strings.ReplaceAll(key, "-", "")
		return key
	}
	var walk func(value any, path string) error
	walk = func(value any, path string) error {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				childPath := key
				if path != "" {
					childPath = path + "." + key
				}
				normalized := normalizeKey(key)
				var validate func(int) error
				switch normalized {
				case "selltime":
					validate = func(value int) error {
						if !isValidTOUHHMM(value) {
							return fmt.Errorf("ожидается реальное время HHMM 00:00–23:55 с шагом 5 минут")
						}
						return nil
					}
				case "sellmodekw":
					validate = func(value int) error {
						if value < 0 || value*10 > schedulePowerMaxW {
							return fmt.Errorf("мощность %d W выходит за предел 0–%d W", value*10, schedulePowerMaxW)
						}
						return nil
					}
				case "gridexportlimit":
					validate = func(value int) error {
						if value < 0 || value*10 > gridExportMaxW {
							return fmt.Errorf("лимит экспорта %d W выходит за предел 0–%d W", value*10, gridExportMaxW)
						}
						return nil
					}
				case "sellmodebattcapacity":
					validate = func(value int) error {
						if value < 0 || value > 100 {
							return fmt.Errorf("SOC должен быть 0–100%%")
						}
						return nil
					}
				case "chargemode":
					validate = func(value int) error {
						if !isAllowedTOUChargeMode(value, allowedChargeModes) {
							return fmt.Errorf("для выбранного профиля разрешены значения %v", allowedTOUChargeModeValues(allowedChargeModes))
						}
						return nil
					}
				case "loadlimitmode":
					validate = func(value int) error {
						if value < 0 || value > 2 {
							return fmt.Errorf("разрешены значения 0–2")
						}
						return nil
					}
				case "priorityload":
					validate = func(value int) error {
						if value < 0 || value > 1 {
							return fmt.Errorf("разрешены значения 0 или 1")
						}
						return nil
					}
				case "usetimermask":
					validate = func(value int) error {
						if value < 0 || value > 255 {
							return fmt.Errorf("разрешены значения 0–255; bit8 на устройстве сохраняется")
						}
						return nil
					}
				}
				if validate != nil {
					numeric, ok := intFromAny(child)
					if !ok {
						return fmt.Errorf("%s должен быть целым числом", childPath)
					}
					if err := validate(numeric); err != nil {
						return fmt.Errorf("%s=%d недопустим: %w", childPath, numeric, err)
					}
				}
				if err := walk(child, childPath); err != nil {
					return err
				}
			}
		case []any:
			for index, child := range typed {
				childPath := fmt.Sprintf("%s[%d]", path, index)
				if err := walk(child, childPath); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(root, "")
}

func validateScheduleJSONForModels(raw string, models []InverterModelDefinition) error {
	if len(models) == 0 {
		model, err := defaultInverterModel()
		if err != nil {
			return err
		}
		models = []InverterModelDefinition{model}
	}
	powerMax := models[0].SchedulePowerMaxW
	exportMax := models[0].GridExportMaxW
	for _, model := range models[1:] {
		if model.SchedulePowerMaxW < powerMax {
			powerMax = model.SchedulePowerMaxW
		}
		if model.GridExportMaxW < exportMax {
			exportMax = model.GridExportMaxW
		}
	}
	allowedChargeModes := allowedTOUChargeModesForModels(models)
	if err := validateExplicitScheduleFields(raw, powerMax, exportMax, allowedChargeModes); err != nil {
		return err
	}
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		return err
	}
	if payload.UseTimerMask < 0 || payload.UseTimerMask > 255 {
		return fmt.Errorf("use_timer_mask должен быть 0–255; bit8 режима Spain сохраняется на устройстве")
	}
	for _, day := range payload.Days {
		for i, cfg := range day.Hours {
			if !cfg.Enabled {
				continue
			}
			if err := validateScheduleConfigValues(fmt.Sprintf("день %d, часовая точка %d", day.Day, i), cfg.SellTime, cfg.SellModeKW, cfg.SellModeBattCapacity, cfg.ChargeMode, cfg.GridExportLimit, cfg.LoadLimitMode, cfg.PriorityLoad, powerMax, exportMax, allowedChargeModes); err != nil {
				return err
			}
		}
		for i, slot := range day.Slots {
			if !slot.Enabled {
				continue
			}
			if err := validateScheduleConfigValues(fmt.Sprintf("день %d, слот %d", day.Day, i), slot.SellTime, slot.SellModeKW, slot.SellModeBattCapacity, slot.ChargeMode, slot.GridExportLimit, slot.LoadLimitMode, slot.PriorityLoad, powerMax, exportMax, allowedChargeModes); err != nil {
				return err
			}
		}
		for i, slot := range day.CustomSlots {
			if !slot.Enabled {
				continue
			}
			if err := validateScheduleConfigValues(fmt.Sprintf("день %d, пользовательская точка %d", day.Day, i), slot.SellTime, slot.SellModeKW, slot.SellModeBattCapacity, slot.ChargeMode, slot.GridExportLimit, slot.LoadLimitMode, slot.PriorityLoad, powerMax, exportMax, allowedChargeModes); err != nil {
				return err
			}
		}
	}
	return nil
}

func isValidScheduleJSON(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var payload SchedulePayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false
	}
	if payload.UseTimerMask < 0 || payload.UseTimerMask > 255 {
		return false
	}
	if len(payload.Days) == 0 || len(payload.Days) > 31 {
		return false
	}
	for _, day := range payload.Days {
		if day.Day < 1 || day.Day > 31 {
			return false
		}
	}
	return true
}

func parseSchedulePayloadLoose(raw string) (SchedulePayload, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultSchedulePayload(), nil
	}
	var strict SchedulePayload
	if err := json.Unmarshal([]byte(raw), &strict); err == nil && len(strict.Days) > 0 {
		if !strings.Contains(raw, "\"use_timer_mask\"") && strict.UseTimerMask == 0 && payloadHasEnabledHours(strict) {
			strict.UseTimerMask = 255
		}
		normalizeSchedulePayloadSellTimes(&strict)
		return strict, nil
	}

	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return SchedulePayload{}, err
	}
	payload := defaultSchedulePayload()
	maskSet := false
	if v, ok := intFromAny(root["use_timer_mask"]); ok {
		payload.UseTimerMask = normalizeUseTimerMask(v)
		maskSet = true
	} else if v, ok := intFromAny(root["UseTimerMask"]); ok {
		payload.UseTimerMask = normalizeUseTimerMask(v)
		maskSet = true
	} else if boolFromAny(root["use_timer"]) || boolFromAny(root["is_active"]) || boolFromAny(root["enabled"]) {
		payload.UseTimerMask = 1
		maskSet = true
	}

	rawDays, _ := root["days"].([]any)
	for dayIndex, rawDayAny := range rawDays {
		if dayIndex >= defaultScheduleDays {
			break
		}
		rawDay, _ := rawDayAny.(map[string]any)
		dayNumber := dayIndex + 1
		if v, ok := intFromAny(rawDay["day"]); ok && v >= 1 && v <= defaultScheduleDays {
			dayNumber = v
		}
		day := &payload.Days[dayNumber-1]
		if _, exists := rawDay["enabled"]; exists {
			day.Enabled = boolFromAny(rawDay["enabled"])
		}
		rawHours, _ := rawDay["hours"].([]any)
		for hourIndex, rawHourAny := range rawHours {
			if hourIndex >= 24 {
				break
			}
			rawHour, _ := rawHourAny.(map[string]any)
			hourNumber := hourIndex
			if v, ok := intFromAny(rawHour["hour"]); ok && v >= 0 && v <= 23 {
				hourNumber = v
			}
			h := &day.Hours[hourNumber]
			h.Hour = hourNumber
			if _, exists := rawHour["enabled"]; exists {
				h.Enabled = boolFromAny(rawHour["enabled"])
			} else if day.Enabled {
				h.Enabled = true
			}
			if v, ok := intFromAny(rawHour["sell_mode_kw"]); ok {
				h.SellModeKW = v
			}
			if v, ok := intFromAny(rawHour["sell_mode_batt_capacity"]); ok {
				h.SellModeBattCapacity = v
			}
			if v, ok := chargeModeFromAny(rawHour["charge_mode"]); ok {
				h.ChargeMode = v
			}
			if v, ok := intFromAny(rawHour["grid_export_limit"]); ok {
				h.GridExportLimit = v
			}
			if _, exists := rawHour["grid_charge_enabled"]; exists {
				h.GridChargeEnabled = boolFromAny(rawHour["grid_charge_enabled"])
			}
			if _, exists := rawHour["solar_export"]; exists {
				h.SolarExport = boolFromAny(rawHour["solar_export"])
			}
			if v, ok := loadLimitModeFromAny(rawHour["load_limit_mode"]); ok {
				h.LoadLimitMode = v
			}
			if _, exists := rawHour["priority_load"]; exists {
				h.PriorityLoad = priorityLoadValueFromAny(rawHour["priority_load"])
			}
			if v, ok := intFromAny(rawHour["use_timer_mask"]); ok && !maskSet {
				payload.UseTimerMask = normalizeUseTimerMask(v)
				maskSet = true
			}
			if boolFromAny(rawHour["use_timer"]) && !maskSet {
				payload.UseTimerMask = 1
				maskSet = true
			}
		}
		rawSlots, _ := rawDay["slots"].([]any)
		for si, rawSlotAny := range rawSlots {
			if si >= 288 {
				break
			}
			rawSlot, _ := rawSlotAny.(map[string]any)
			hourNumber := si / 12
			minuteNumber := (si % 12) * 5
			if v, ok := intFromAny(rawSlot["hour"]); ok && v >= 0 && v <= 23 {
				hourNumber = v
			}
			if v, ok := intFromAny(rawSlot["minute"]); ok && v >= 0 && v <= 55 {
				minuteNumber = (v / 5) * 5
			}
			slotIdx := slotIndex(hourNumber, minuteNumber)
			if slotIdx < 0 || slotIdx >= 288 {
				continue
			}
			sl := &day.Slots[slotIdx]
			sl.Hour = hourNumber
			sl.Minute = minuteNumber
			if _, exists := rawSlot["enabled"]; exists {
				sl.Enabled = boolFromAny(rawSlot["enabled"])
			} else if day.Enabled {
				sl.Enabled = true
			}
			if v, ok := intFromAny(rawSlot["sell_mode_kw"]); ok {
				sl.SellModeKW = v
			}
			if v, ok := intFromAny(rawSlot["sell_mode_batt_capacity"]); ok {
				sl.SellModeBattCapacity = v
			}
			if v, ok := chargeModeFromAny(rawSlot["charge_mode"]); ok {
				sl.ChargeMode = v
			}
			if v, ok := intFromAny(rawSlot["grid_export_limit"]); ok {
				sl.GridExportLimit = v
			}
			if _, exists := rawSlot["grid_charge_enabled"]; exists {
				sl.GridChargeEnabled = boolFromAny(rawSlot["grid_charge_enabled"])
			}
			if _, exists := rawSlot["solar_export"]; exists {
				sl.SolarExport = boolFromAny(rawSlot["solar_export"])
			}
			if v, ok := loadLimitModeFromAny(rawSlot["load_limit_mode"]); ok {
				sl.LoadLimitMode = v
			}
			if _, exists := rawSlot["priority_load"]; exists {
				sl.PriorityLoad = priorityLoadValueFromAny(rawSlot["priority_load"])
			}
			if _, exists := rawSlot["default_custom_slot"]; exists {
				sl.DefaultCustomSlot = boolFromAny(rawSlot["default_custom_slot"])
			} else if _, exists := rawSlot["is_default_custom_slot"]; exists {
				sl.DefaultCustomSlot = boolFromAny(rawSlot["is_default_custom_slot"])
			}
			if v, ok := intFromAny(rawSlot["use_timer_mask"]); ok && !maskSet {
				payload.UseTimerMask = normalizeUseTimerMask(v)
				maskSet = true
			}
			if boolFromAny(rawSlot["use_timer"]) && !maskSet {
				payload.UseTimerMask = 1
				maskSet = true
			}
		}
		rawCustomSlots, _ := rawDay["customSlots"].([]any)
		if len(rawCustomSlots) == 0 {
			rawCustomSlots, _ = rawDay["custom_slots"].([]any)
		}
		seenCustomSlots := map[int]bool{}
		for _, rawSlotAny := range rawCustomSlots {
			rawSlot, _ := rawSlotAny.(map[string]any)
			hourNumber, okHour := intFromAny(rawSlot["hour"])
			minuteNumber, okMinute := intFromAny(rawSlot["minute"])
			if !okHour || hourNumber < 0 || hourNumber > 23 || !okMinute || minuteNumber < 0 || minuteNumber > 55 {
				continue
			}
			minuteNumber = (minuteNumber / 5) * 5
			slotIdx := slotIndex(hourNumber, minuteNumber)
			if slotIdx < 0 || slotIdx >= 288 || seenCustomSlots[slotIdx] {
				continue
			}
			base := minuteSlotFromHourConfig(day.Hours[hourNumber], minuteNumber)
			sl := &base
			if _, exists := rawSlot["enabled"]; exists {
				sl.Enabled = boolFromAny(rawSlot["enabled"])
			} else if day.Enabled {
				sl.Enabled = true
			}
			if v, ok := intFromAny(rawSlot["sell_mode_kw"]); ok {
				sl.SellModeKW = v
			}
			if v, ok := intFromAny(rawSlot["sell_mode_batt_capacity"]); ok {
				sl.SellModeBattCapacity = v
			}
			if v, ok := chargeModeFromAny(rawSlot["charge_mode"]); ok {
				sl.ChargeMode = v
			}
			if v, ok := intFromAny(rawSlot["grid_export_limit"]); ok {
				sl.GridExportLimit = v
			}
			if _, exists := rawSlot["grid_charge_enabled"]; exists {
				sl.GridChargeEnabled = boolFromAny(rawSlot["grid_charge_enabled"])
			}
			if _, exists := rawSlot["solar_export"]; exists {
				sl.SolarExport = boolFromAny(rawSlot["solar_export"])
			}
			if v, ok := loadLimitModeFromAny(rawSlot["load_limit_mode"]); ok {
				sl.LoadLimitMode = v
			}
			if _, exists := rawSlot["priority_load"]; exists {
				sl.PriorityLoad = priorityLoadValueFromAny(rawSlot["priority_load"])
			}
			if _, exists := rawSlot["default_custom_slot"]; exists {
				sl.DefaultCustomSlot = boolFromAny(rawSlot["default_custom_slot"])
			} else if _, exists := rawSlot["is_default_custom_slot"]; exists {
				sl.DefaultCustomSlot = boolFromAny(rawSlot["is_default_custom_slot"])
			}
			if v, ok := intFromAny(rawSlot["use_timer_mask"]); ok && !maskSet {
				payload.UseTimerMask = normalizeUseTimerMask(v)
				maskSet = true
			}
			if boolFromAny(rawSlot["use_timer"]) && !maskSet {
				payload.UseTimerMask = 1
				maskSet = true
			}
			day.Slots[slotIdx] = *sl
			day.CustomSlots = append(day.CustomSlots, *sl)
			seenCustomSlots[slotIdx] = true
		}
	}
	if !maskSet && payloadHasEnabledHours(payload) {
		payload.UseTimerMask = 255
	}
	normalizeSchedulePayloadSellTimes(&payload)
	return payload, nil
}

func payloadHasEnabledHours(payload SchedulePayload) bool {
	for _, day := range payload.Days {
		if day.Enabled {
			return true
		}
		for _, hour := range day.Hours {
			if hour.Enabled {
				return true
			}
		}
		for _, slot := range day.Slots {
			if slot.Enabled {
				return true
			}
		}
	}
	return false
}

func intFromAny(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case float32:
		return int(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
		if f, err := strconv.ParseFloat(t.String(), 64); err == nil {
			return int(f), true
		}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		if strings.Contains(s, ":") {
			s = strings.SplitN(s, ":", 2)[0]
		}
		if n, err := strconv.Atoi(s); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

func boolFromAny(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		s = strings.ReplaceAll(s, "_", " ")
		s = strings.Join(strings.Fields(s), " ")
		switch s {
		case "1", "true", "yes", "y", "on", "вкл", "да", "истина", "enabled", "enable":
			return true
		}
	}
	return false
}

func normalizePriorityLoadValue(v int) int {
	if v != 0 {
		return 1
	}
	return 0
}

func priorityLoadValueFromAny(v any) int {
	if n, ok := intFromAny(v); ok {
		return normalizePriorityLoadValue(n)
	}
	s := normalizeModeString(v)
	switch s {
	case "battery first", "batteryfirst", "battery", "акб first", "battery priority", "1", "true", "yes", "y", "on", "вкл", "да", "истина", "enabled", "enable":
		return 1
	case "load first", "loadfirst", "load", "load priority", "0", "false", "no", "n", "off", "выкл", "нет", "ложь", "disabled", "disable":
		return 0
	}
	if boolFromAny(v) {
		return 1
	}
	return 0
}

func chargeModeFromAny(v any) (int, bool) {
	if n, ok := intFromAny(v); ok {
		return normalizeChargeModeValue(n), true
	}
	s := normalizeModeString(v)
	if value, ok := chargeModeValueFromLabel(fmt.Sprint(v)); ok {
		return value, true
	}
	switch s {
	case "no grid or gen or sell", "no grid or gen", "no grid", "none", "нет", "0":
		return 0, true
	case "allow grid", "grid", "1":
		return 1, true
	case "allow gen", "gen", "2":
		return 2, true
	case "allow gen and grid", "allow grid and gen", "allow grid & gen", "allow gen & grid", "gen and grid", "grid and gen", "grid & gen", "3":
		return 3, true
	case "sell", "32":
		return 32, true
	case "sell and grid", "sell & grid", "sell grid", "33":
		return 33, true
	case "sell and gen", "sell & gen", "sell gen", "34":
		return 34, true
	case "sell and grid and gen", "sell & grid & gen", "sell and gen and grid", "sell & gen & grid", "sell grid gen", "35":
		return 35, true
	}
	return 0, false
}

func isAllowedChargeMode(v int) bool {
	return isConfiguredChargeModeValue(v)
}

func normalizeChargeModeValue(v int) int {
	if isAllowedChargeMode(v) {
		return v
	}
	return 0
}

func loadLimitModeFromAny(v any) (int, bool) {
	if n, ok := intFromAny(v); ok {
		if n < 0 {
			return 0, true
		}
		if n > 2 {
			return 2, true
		}
		return n, true
	}
	s := normalizeModeString(v)
	switch s {
	case "selling first", "sellingfirst", "allow export", "allowexport", "0":
		return 0, true
	case "zero export load", "zeroexportload", "essentials", "1":
		return 1, true
	case "zero export ct", "zeroexportct", "zero export", "zeroexport", "2":
		return 2, true
	}
	return 0, false
}

func normalizeModeString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	return strings.Join(strings.Fields(s), " ")
}

func normalizeUseTimerMask(mask int) int {
	if mask < 0 {
		return 0
	}
	if mask > 255 {
		return 255
	}
	return mask
}

func useTimerMaskFromFlags(enabled bool, weekdays ...bool) int {
	mask := 0
	if enabled {
		mask |= 1 << 0
	}
	for i, on := range weekdays {
		if on && i < 7 {
			mask |= 1 << (i + 1)
		}
	}
	return mask
}

var lastUseTimerMask struct {
	sync.Mutex
	raw  string
	mask int
	ok   bool
}

func scheduleUseTimerMask(raw string) int {
	lastUseTimerMask.Lock()
	if lastUseTimerMask.ok && lastUseTimerMask.raw == raw {
		mask := lastUseTimerMask.mask
		lastUseTimerMask.Unlock()
		return mask
	}
	lastUseTimerMask.Unlock()
	mask := 0
	if payload, err := parseSchedulePayloadLoose(raw); err == nil {
		mask = normalizeUseTimerMask(payload.UseTimerMask)
	}
	lastUseTimerMask.Lock()
	lastUseTimerMask.raw, lastUseTimerMask.mask, lastUseTimerMask.ok = raw, mask, true
	lastUseTimerMask.Unlock()
	return mask
}

func scheduleUseTimerEnabled(raw string) bool {
	return scheduleUseTimerMask(raw)&1 != 0
}

func getSlotConfig(payload *SchedulePayload, day int, hour, minute int) (MinuteSlot, bool) {
	if payload == nil {
		return MinuteSlot{}, false
	}
	for i := range payload.Days {
		if payload.Days[i].Day != day {
			continue
		}
		if !payload.Days[i].Enabled {
			return MinuteSlot{}, false
		}
		if hour < 0 || hour >= len(payload.Days[i].Hours) {
			return MinuteSlot{}, false
		}

		hourCfg := payload.Days[i].Hours[hour]
		if !hourCfg.Enabled {
			return MinuteSlot{}, false
		}
		idx := slotIndex(hour, minute)
		if idx >= 0 && idx < len(payload.Days[i].Slots) {
			slot := payload.Days[i].Slots[idx]
			// 5-минутный слот отправляем только если он явно заполнен, активен и отличается
			// от часовой настройки. Пустой/унаследованный слот заменяем часовой точкой HH:00.
			if minute != 0 && slot.Enabled && minuteSlotDiffersFromHour(slot, hourCfg) {
				return slot, true
			}
		}
		return minuteSlotFromHourConfig(hourCfg, 0), true
	}
	return MinuteSlot{}, false
}

func getHourConfig(payload *SchedulePayload, day int, hour int) (HourConfig, bool) {
	if payload == nil {
		return HourConfig{}, false
	}
	for i := range payload.Days {
		if payload.Days[i].Day == day {
			hours := payload.Days[i].Hours
			if hour >= 0 && hour < len(hours) {
				return hours[hour], true
			}
			return HourConfig{}, false
		}
	}
	return HourConfig{}, false
}

func minuteSlotToHourConfig(s MinuteSlot) HourConfig {
	return HourConfig{
		Hour:                 s.Hour,
		Label:                s.Label,
		Point:                s.Point,
		SellTime:             s.SellTime,
		Enabled:              s.Enabled,
		SellModeKW:           s.SellModeKW,
		SellModeBattCapacity: s.SellModeBattCapacity,
		ChargeMode:           s.ChargeMode,
		GridExportLimit:      s.GridExportLimit,
		GridChargeEnabled:    s.GridChargeEnabled,
		SolarExport:          s.SolarExport,
		LoadLimitMode:        s.LoadLimitMode,
		UseTimer:             s.UseTimer,
		UseTimerMask:         s.UseTimerMask,
		PriorityLoad:         s.PriorityLoad,
	}
}

func setScheduleUseTimerEnabled(raw string, enabled bool) string {
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil || len(payload.Days) == 0 {
		payload = defaultSchedulePayload()
	}
	mask := normalizeUseTimerMask(payload.UseTimerMask)
	if enabled {
		mask |= 1
	} else {
		mask &^= 1
	}
	payload.UseTimerMask = normalizeUseTimerMask(mask)
	normalizeSchedulePayloadSellTimes(&payload)
	for di := range payload.Days {
		for hi := range payload.Days[di].Hours {
			payload.Days[di].Hours[hi].UseTimer = enabled
			payload.Days[di].Hours[hi].UseTimerMask = payload.UseTimerMask
		}
		for si := range payload.Days[di].Slots {
			payload.Days[di].Slots[si].UseTimer = enabled
			payload.Days[di].Slots[si].UseTimerMask = payload.UseTimerMask
		}
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
