package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
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
	GridPeakMaxW      int      `json:"grid_peak_max_w"`
	GridPeakEnabled   bool     `json:"grid_peak_enabled"`
	GridPeakEnableRaw uint16   `json:"grid_peak_enable_raw"`
	PriorityLoadRaw   uint16   `json:"priority_load_raw"`
	EnergyPattern     string   `json:"energy_pattern"`
}

type InverterScreenResponse struct {
	OK                bool                   `json:"ok"`
	Message           string                 `json:"message"`
	Target            string                 `json:"target"`
	InverterID        int64                  `json:"inverter_id"`
	DataSource        string                 `json:"data_source"`
	UpdatedAt         string                 `json:"updated_at"`
	Points            []InverterScreenPoint  `json:"points"`
	Settings          InverterScreenSettings `json:"settings"`
	ModelKey          string                 `json:"model_key"`
	ModelName         string                 `json:"model_name"`
	ParametersFile    string                 `json:"parameters_file"`
	RegisterAddresses map[string][]uint16    `json:"register_addresses"`
}

type storedGridPeakState struct {
	PowerW  int
	Enabled bool
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

	// Без явного refresh экран не обращается к Modbus: первое корректное значение
	// берётся из файла, а последующие ручные изменения — из сохранённого состояния.
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

	refreshFromInverter := r.URL.Query().Get("refresh") == "1"
	dataSource := "saved_file"
	message := "Начальное значение ограничения загружено из файла лога"

	var record map[string]any
	var sourceFile string
	if refreshFromInverter {
		settings, settingsErr := a.getSettings()
		if settingsErr != nil {
			writeJSON(w, http.StatusInternalServerError, InverterScreenResponse{OK: false, Message: "Ошибка чтения настроек: " + settingsErr.Error()})
			return
		}
		a.runInverterLoggingOnce(settings, "screen visualization manual refresh")
		latest, latestErr := a.getLatestInverterLogJSONForInverterID(target.InverterID)
		if latestErr == nil && usableGridPeakLogRecord(latest) {
			record = latest
			dataSource = "inverter"
			message = "Значение обновлено прямым чтением инвертора"
		}
	}
	if record == nil {
		record, sourceFile, err = a.findFirstUsableGridPeakLogRecord(target.InverterID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, InverterScreenResponse{OK: false, Message: "Не удалось получить первое сохранённое значение ограничения: " + err.Error()})
			return
		}
		if refreshFromInverter {
			message = "Инвертор не ответил; сохранено значение из файла " + sourceFile
		} else {
			message = "Начальное значение загружено из файла " + sourceFile
		}
	}

	state, seededFromFile, stateErr := a.loadOrSeedStoredGridPeakState(target.InverterID, model.RatedPowerW, record)
	if stateErr != nil {
		writeJSON(w, http.StatusInternalServerError, InverterScreenResponse{OK: false, Message: "Ошибка сохранённого состояния ограничения: " + stateErr.Error()})
		return
	}
	if dataSource == "inverter" {
		power := int(uint16FromRecord(record, "grid_peak_shaving_power"))
		if power >= 0 && power <= model.RatedPowerW {
			state.PowerW = power
		}
		if _, ok := record["grid_peak_shaving_enabled"]; ok {
			state.Enabled = uint16FromRecord(record, "grid_peak_shaving_enabled")&0x20 != 0
		}
		_ = a.saveStoredGridPeakState(target.InverterID, &state.PowerW, &state.Enabled)
	} else if seededFromFile {
		dataSource = "file_initial"
		message = "Начальное значение загружено из файла " + sourceFile
	} else {
		dataSource = "stored"
		if refreshFromInverter {
			message = "Инвертор не ответил; сохранённое значение оставлено без изменений"
		} else {
			message = "Используется сохранённое значение ограничения"
		}
	}
	record["grid_peak_shaving_power"] = state.PowerW
	if state.Enabled {
		record["grid_peak_shaving_enabled"] = uint16(0x30)
	} else {
		record["grid_peak_shaving_enabled"] = uint16(0x10)
	}

	screenSettings := buildInverterScreenSettings(record)
	screenSettings.GridPeakMaxW = model.RatedPowerW

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

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(InverterScreenResponse{
		OK:         true,
		Message:    message,
		Target:     fmt.Sprintf("%s (%s:%d, %s, %s)", name, target.IP, target.Port, model.ModelCode, source),
		InverterID: target.InverterID,
		DataSource: dataSource,
		UpdatedAt:  time.Now().Format("02.01.2006 15:04:05"),
		Points:     points,
		Settings:   screenSettings,
		ModelKey:   model.Key, ModelName: model.Name, ParametersFile: model.ParametersFile, RegisterAddresses: addressMap,
	})
}

func usableGridPeakLogRecord(record map[string]any) bool {
	if record == nil {
		return false
	}
	if _, ok := record["grid_peak_shaving_power"]; !ok {
		return false
	}
	if text, ok := record["logger_error"].(string); ok && text != "" {
		return false
	}
	if status, ok := record["inverter_time_status"].(string); ok {
		switch status {
		case "error", "no_connection", "profile_error":
			return false
		}
	}
	return true
}

type gridPeakRecordCacheEntry struct {
	record    map[string]any
	file      string
	preceding map[string]int64 // newer files (name → size) that had no usable record
	legacy    bool
}

var gridPeakRecordCache struct {
	sync.Mutex
	entries map[int64]gridPeakRecordCacheEntry
}

// findFirstUsableGridPeakLogRecord scans log files newest first. The result is
// cached and reused while the found file and all newer files are unchanged in
// name and size, which avoids re-reading large log files on every page load.
func (a *App) findFirstUsableGridPeakLogRecord(inverterID int64) (map[string]any, string, error) {
	files, err := listInverterLogFiles()
	if err != nil {
		return nil, "", err
	}
	gridPeakRecordCache.Lock()
	entry, ok := gridPeakRecordCache.entries[inverterID]
	gridPeakRecordCache.Unlock()
	if ok {
		valid := false
		if entry.legacy {
			valid = len(files) == len(entry.preceding)
			for _, f := range files {
				if size, known := entry.preceding[f.Name]; !known || size != f.SizeBytes {
					valid = false
					break
				}
			}
		} else {
			seen := 0
			for _, f := range files {
				if f.Name == entry.file {
					valid = seen == len(entry.preceding)
					break
				}
				if size, known := entry.preceding[f.Name]; !known || size != f.SizeBytes {
					break
				}
				seen++
			}
		}
		if valid {
			return cloneRecord(entry.record), entry.file, nil
		}
	}
	record, file, legacy, err := a.scanFirstUsableGridPeakLogRecord(inverterID, files)
	if err != nil {
		return nil, "", err
	}
	// A legacy hit (record without inverter_id) can be superseded by a new
	// per-inverter record in any file, so it is keyed on every file's size.
	preceding := map[string]int64{}
	for _, f := range files {
		if f.Name == file && !legacy {
			break
		}
		preceding[f.Name] = f.SizeBytes
	}
	gridPeakRecordCache.Lock()
	if gridPeakRecordCache.entries == nil {
		gridPeakRecordCache.entries = map[int64]gridPeakRecordCacheEntry{}
	}
	gridPeakRecordCache.entries[inverterID] = gridPeakRecordCacheEntry{record: cloneRecord(record), file: file, preceding: preceding, legacy: legacy}
	gridPeakRecordCache.Unlock()
	return record, file, nil
}

func cloneRecord(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (a *App) scanFirstUsableGridPeakLogRecord(inverterID int64, files []InverterLogFileInfo) (map[string]any, string, bool, error) {
	// Сначала ищем запись именно этого инвертора, затем поддерживаем старые
	// файлы без inverter_id. В каждом файле берётся первое пригодное значение.
	for _, legacy := range []bool{false, true} {
		for _, file := range files {
			records, readErr := readAllLogRecords(inverterLogDir + "/" + file.Name)
			if readErr != nil {
				continue
			}
			for _, record := range records {
				recordID := logRecordInverterID(record)
				if inverterID > 0 {
					if legacy && recordID != 0 {
						continue
					}
					if !legacy && recordID != inverterID {
						continue
					}
				}
				if usableGridPeakLogRecord(record) {
					return record, file.Name, legacy, nil
				}
			}
		}
	}
	return nil, "", false, fmt.Errorf("в файлах логов нет корректного grid_peak_shaving_power")
}

func (a *App) loadOrSeedStoredGridPeakState(inverterID int64, maxW int, record map[string]any) (storedGridPeakState, bool, error) {
	var storedPower, storedEnabled sql.NullInt64
	if err := a.db.QueryRow(`SELECT grid_peak_shaving_power, grid_peak_shaving_enabled FROM inverters WHERE id = ?`, inverterID).Scan(&storedPower, &storedEnabled); err != nil {
		return storedGridPeakState{}, false, err
	}
	state := storedGridPeakState{}
	if storedPower.Valid {
		state.PowerW = int(storedPower.Int64)
	} else {
		state.PowerW = int(uint16FromRecord(record, "grid_peak_shaving_power"))
		if state.PowerW < 0 || state.PowerW > maxW {
			state.PowerW = 0
		}
	}
	if storedEnabled.Valid {
		state.Enabled = storedEnabled.Int64 != 0
	} else {
		state.Enabled = uint16FromRecord(record, "grid_peak_shaving_enabled")&0x20 != 0
	}
	if !storedPower.Valid || !storedEnabled.Valid {
		if err := a.saveStoredGridPeakState(inverterID, &state.PowerW, &state.Enabled); err != nil {
			return storedGridPeakState{}, false, err
		}
	}
	return state, !storedPower.Valid || !storedEnabled.Valid, nil
}

func (a *App) saveStoredGridPeakState(inverterID int64, powerW *int, enabled *bool) error {
	var powerValue any
	var enabledValue any
	if powerW != nil {
		powerValue = *powerW
	}
	if enabled != nil {
		enabledValue = boolToInt(*enabled)
	}
	_, err := a.db.Exec(`
		UPDATE inverters
		SET grid_peak_shaving_power = COALESCE(?, grid_peak_shaving_power),
		    grid_peak_shaving_enabled = COALESCE(?, grid_peak_shaving_enabled)
		WHERE id = ?`, powerValue, enabledValue, inverterID)
	return err
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

	gridPeakValue := uint16FromRecord(record, "grid_peak_shaving_power")
	gridPeakEnableRaw := uint16FromRecord(record, "grid_peak_shaving_enabled")
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
		// Register 178 stores Grid Peak Shaving in bits 4-5. Bit 4 is the
		// fixed mode marker; bit 5 is the actual enabled flag (0x20).
		GridPeakEnabled:   gridPeakEnableRaw&0x20 != 0,
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
