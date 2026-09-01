package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

type InverterScreenPoint struct {
	Point     int    `json:"point"`
	Grid      bool   `json:"grid"`
	Gen       bool   `json:"gen"`
	Sell      bool   `json:"sell"`
	Mode      uint16 `json:"mode"`
	TimeStart string `json:"time_start"`
	TimeEnd   string `json:"time_end"`
	Power     uint16 `json:"power"`
	Batt      uint16 `json:"batt"`
}

type InverterScreenSettings struct {
	UseTimerRaw       uint16   `json:"use_timer_raw"`
	UseTimerEnabled   bool     `json:"use_timer_enabled"`
	UseTimerDays      []string `json:"use_timer_days"`
	WorkModeRaw       uint16   `json:"work_mode_raw"`
	WorkModeLabel     string   `json:"work_mode_label"`
	GridPeakValue     uint16   `json:"grid_peak_value"`
	GridPeakEnabled   bool     `json:"grid_peak_enabled"`
	GridPeakEnableRaw uint16   `json:"grid_peak_enable_raw"`
	PriorityLoadRaw   uint16   `json:"priority_load_raw"`
	EnergyPattern     string   `json:"energy_pattern"`
}

type InverterScreenResponse struct {
	OK                bool                   `json:"ok"`
	Message           string                 `json:"message"`
	Target            string                 `json:"target"`
	UpdatedAt         string                 `json:"updated_at"`
	Points            []InverterScreenPoint  `json:"points"`
	Settings          InverterScreenSettings `json:"settings"`
	ModelKey          string                 `json:"model_key"`
	ModelName         string                 `json:"model_name"`
	ParametersFile    string                 `json:"parameters_file"`
	RegisterAddresses map[string][]uint16    `json:"register_addresses"`
}

func (a *App) apiInverterScreenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}

	target, source, err := a.defaultModbusTarget()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, InverterScreenResponse{OK: false, Message: "Активный инвертор не найден: " + err.Error()})
		return
	}

	// Для экрана используем тот же стабильный механизм, что и для общего лога инвертора:
	// один полный проход логирования, затем построение таблицы из JSON-записи.
	// Это убирает рассинхрон TCP-Modbus transaction id при серии отдельных коротких чтений.
	model, params, err := loadDeviceParametersForModel(target.ModelKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, InverterScreenResponse{OK: false, Message: "Ошибка профиля инвертора: " + err.Error()})
		return
	}
	addressMap := map[string][]uint16{}
	for _, fixture := range params {
		code := fixture.Fields.Code
		if code != "" {
			addressMap[code] = deviceParameterAddresses(fixture.Fields)
		}
	}

	settings, err := a.getSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, InverterScreenResponse{OK: false, Message: "Ошибка чтения настроек: " + err.Error()})
		return
	}
	a.runInverterLoggingOnce(settings, "screen visualization")

	record, err := a.getLatestInverterLogJSONForInverterID(target.InverterID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, InverterScreenResponse{OK: false, Message: "Не удалось получить данные лога инвертора: " + err.Error()})
		return
	}

	screenSettings := buildInverterScreenSettings(record)

	points := make([]InverterScreenPoint, 0, 6)
	for i := 1; i <= 6; i++ {
		next := i + 1
		if next > 6 {
			next = 1
		}

		mode := uint16FromRecord(record, fmt.Sprintf("charge_mode_point_%d", i))
		points = append(points, InverterScreenPoint{
			Point:     i,
			Grid:      mode&1 == 1,
			Gen:       mode&2 == 2,
			Sell:      mode&32 == 32,
			Mode:      mode,
			TimeStart: formatInverterPointTime(uint16FromRecord(record, fmt.Sprintf("sell_time_point_%d", i))),
			TimeEnd:   formatInverterPointTime(uint16FromRecord(record, fmt.Sprintf("sell_time_point_%d", next))),
			Power:     uint16FromRecord(record, fmt.Sprintf("sell_mode_kw_point_%d", i)),
			Batt:      uint16FromRecord(record, fmt.Sprintf("sell_mode_batt_capacity_%d", i)),
		})
	}

	name := target.InverterName
	if name == "" {
		name = "Инвертор"
	}

	message := "Данные экрана инвертора прочитаны через общий лог"
	if a.inverterLogState.LastStatus == "warn" || a.inverterLogState.LastStatus == "error" {
		message = a.inverterLogState.LastMessage
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(InverterScreenResponse{
		OK:        true,
		Message:   message,
		Target:    fmt.Sprintf("%s (%s:%d, %s, %s)", name, target.IP, target.Port, model.ModelCode, source),
		UpdatedAt: time.Now().Format("02.01.2006 15:04:05"),
		Points:    points,
		Settings:  screenSettings,
		ModelKey:  model.Key, ModelName: model.Name, ParametersFile: model.ParametersFile, RegisterAddresses: addressMap,
	})
}

func buildInverterScreenSettings(record map[string]any) InverterScreenSettings {
	useTimer := uint16FromRecord(record, "use_timer")
	days := []struct {
		bit  uint16
		name string
	}{
		{1, "Пн"},
		{2, "Вт"},
		{3, "Ср"},
		{4, "Чт"},
		{5, "Пт"},
		{6, "Сб"},
		{7, "Вс"},
	}
	activeDays := make([]string, 0, 7)
	for _, d := range days {
		if useTimer&(1<<d.bit) != 0 {
			activeDays = append(activeDays, d.name)
		}
	}

	workMode := uint16FromRecord(record, "inverter_work_mode")
	workModeLabel := "Unknown"
	switch workMode {
	case 0:
		workModeLabel = "Selling first"
	case 1:
		workModeLabel = "Zero export load"
	case 2:
		workModeLabel = "Zero export CT"
	}

	gridPeakValue := uint16FromRecord(record, "grid_export_limit")
	if gridPeakValue == 0 {
		gridPeakValue = uint16FromRecord(record, "grid_peak_shaving_power")
	}
	gridPeakEnableRaw := uint16FromRecord(record, "grid_charge_enable")
	priorityLoad := uint16FromRecord(record, "priority_load")
	energyPattern := "Unknown"
	switch priorityLoad & 0x0003 {
	case 2:
		energyPattern = "Battery First"
	case 3:
		energyPattern = "Load First"
	}

	return InverterScreenSettings{
		UseTimerRaw:       useTimer,
		UseTimerEnabled:   useTimer != 0,
		UseTimerDays:      activeDays,
		WorkModeRaw:       workMode,
		WorkModeLabel:     workModeLabel,
		GridPeakValue:     gridPeakValue,
		GridPeakEnabled:   gridPeakEnableRaw != 0,
		GridPeakEnableRaw: gridPeakEnableRaw,
		PriorityLoadRaw:   priorityLoad,
		EnergyPattern:     energyPattern,
	}
}

func uint16FromRecord(record map[string]any, key string) uint16 {
	v, ok := record[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return 0
		}
		if n > math.MaxUint16 {
			return math.MaxUint16
		}
		return uint16(math.Round(n))
	case float32:
		f := float64(n)
		if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return 0
		}
		if f > math.MaxUint16 {
			return math.MaxUint16
		}
		return uint16(math.Round(f))
	case int:
		if n < 0 {
			return 0
		}
		if n > math.MaxUint16 {
			return math.MaxUint16
		}
		return uint16(n)
	case int64:
		if n < 0 {
			return 0
		}
		if n > math.MaxUint16 {
			return math.MaxUint16
		}
		return uint16(n)
	case uint16:
		return n
	case uint:
		if n > math.MaxUint16 {
			return math.MaxUint16
		}
		return uint16(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			if i < 0 {
				return 0
			}
			if i > math.MaxUint16 {
				return math.MaxUint16
			}
			return uint16(i)
		}
		if f, err := n.Float64(); err == nil {
			if f < 0 {
				return 0
			}
			if f > math.MaxUint16 {
				return math.MaxUint16
			}
			return uint16(math.Round(f))
		}
	}
	return 0
}

func formatInverterPointTime(raw uint16) string {
	hour := int(raw) / 100
	minute := int(raw) % 100
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return fmt.Sprintf("%d", raw)
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}
