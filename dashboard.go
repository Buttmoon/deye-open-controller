package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"inverter-schedule/internal/models"
)

var setpointCodes = []string{
	"Enabled", "SellModeKW", "SellModeBattCapacity", "ChargeMode", "GridExportLimit", "GridChargeEnabled", "SolarExport", "LoadLimitMode", "UseTimer", "PriorityLoad",
}

var dashboardCompareCodes = []DashboardMetricValue{
	{Code: "Enabled", Label: "Enabled"},
	{Code: "SellModeKW", Label: "SellModeKW"},
	{Code: "SellModeBattCapacity", Label: "Batt %"},
	{Code: "LoadLimitMode", Label: "Mode"},
	{Code: "battery1_bms_soc", Label: "SOC"},
	{Code: "battery1_bms_voltage", Label: "Batt V"},
	{Code: "battery1_bms_current", Label: "Batt A"},
	{Code: "battery_power_3p", Label: "Batt W"},
	{Code: "zero_export_power", Label: "Zero export"},
}

func (a *App) buildMainTimeOverview(settings models.Settings) TimeOverviewData {
	now := time.Now()
	loc := time.UTC
	if settings.Timezone != "" {
		if loaded, err := time.LoadLocation(settings.Timezone); err == nil {
			loc = loaded
		}
	}

	records := latestNInverterLogRecords(1)
	invDate := "—"
	invClock := "—"
	invTime := "—"
	invStatus := "нет данных в логах"
	lastData := "—"
	lastAgo := "—"
	if len(records) > 0 {
		rec := records[0].Record
		if v := stringFromRecord(rec, "inverter_timestamp"); v != "" {
			invTime = v
			invDate, invClock = splitDateTimeForDisplay(v)
		}
		if v := stringFromRecord(rec, "inverter_time_status"); v != "" {
			invStatus = v
		}
		if !records[0].Time.IsZero() {
			lastData = records[0].Time.Format("2006-01-02 15:04:05")
			lastAgo = humanSince(records[0].Time)
		} else if v := stringFromRecord(rec, "timestamp"); v != "" {
			lastData = v
		}
	}

	return TimeOverviewData{
		SystemDate:          now.Format("2006-01-02"),
		SystemClock:         now.Format("15:04:05"),
		SystemTimezoneDate:  now.In(loc).Format("2006-01-02"),
		SystemTimezoneClock: now.In(loc).Format("15:04:05"),
		SystemTimezoneLabel: loc.String(),
		InverterDate:        invDate,
		InverterClock:       invClock,
		InverterTime:        invTime,
		InverterTimeStatus:  invStatus,
		LastDataReceived:    lastData,
		LastDataAgo:         lastAgo,
	}
}

func (a *App) buildDashboardData() InverterDashboardData {
	records := latestNInverterLogRecords(8)
	data := InverterDashboardData{CompareHeaders: dashboardCompareCodes}
	if name, schedule, mask := a.activeScheduleSummary(); name != "" || schedule != "" {
		data.ActiveTemplateName = name
		data.ActiveScheduleName = schedule
		data.ActiveUseTimerMask = mask
	}
	if len(records) == 0 {
		return data
	}
	rec := records[0].Record
	data.LatestLogFile = records[0].FileName
	data.LatestTimestamp = stringFromRecord(rec, "timestamp")
	data.LatestInverterTimestamp = stringFromRecord(rec, "inverter_timestamp")
	data.InverterModelKey = stringFromRecord(rec, "inverter_model_key")
	data.InverterModelName = stringFromRecord(rec, "inverter_model_name")
	data.ParametersFile = stringFromRecord(rec, "parameters_file")
	if data.InverterModelKey == "" {
		data.InverterModelKey = defaultInverterModelKey
	}

	paramInfo := map[string]DeviceParameterFields{}
	if model, params, err := loadDeviceParametersForModel(data.InverterModelKey); err == nil {
		if data.InverterModelName == "" {
			data.InverterModelName = model.Name
		}
		if data.ParametersFile == "" {
			data.ParametersFile = model.ParametersFile
		}
		for _, p := range params {
			code := strings.TrimSpace(p.Fields.Code)
			if code != "" {
				paramInfo[code] = p.Fields
			}
		}
	}
	setpointDescriptions := map[string]string{
		"Enabled":              "Активность часа расписания",
		"SellModeKW":           "Мощность продажи/отдачи для текущего point",
		"SellModeBattCapacity": "Ограничение SOC батареи для текущего point",
		"ChargeMode":           "Режим заряда для текущего point",
		"GridExportLimit":      "Глобальный лимит отдачи в сеть",
		"GridChargeEnabled":    "Разрешение заряда от сети",
		"SolarExport":          "Разрешение продажи/экспорта солнечной энергии",
		"LoadLimitMode":        "Режим ограничения нагрузки: Selling first / Zero export load / Zero export CT",
		"UseTimer":             "Bitmask Use Timer: bit0 Вкл, bit1..bit7 дни недели; адрес определяется профилем модели",
		"PriorityLoad":         "Приоритет нагрузки",
	}
	for _, code := range setpointCodes {
		data.Setpoints = append(data.Setpoints, DashboardRegisterData{Code: code, Name: code, Description: setpointDescriptions[code], Value: fmt.Sprint(formatLogCell(rec[code])), Group: "setpoint"})
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		if isMetaLogField(k) || containsString(setpointCodes, k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	limit := 28
	for i, code := range keys {
		if i >= limit {
			break
		}
		info := paramInfo[code]
		suffix := ""
		if info.Suffix != nil {
			suffix = *info.Suffix
		}
		name := info.Name
		if name == "" {
			name = code
		}
		data.Actuals = append(data.Actuals, DashboardRegisterData{Code: code, Name: name, Description: info.Description, Value: fmt.Sprint(formatLogCell(rec[code])), Suffix: suffix, Group: "actual"})
	}
	for _, line := range records {
		row := DashboardLogRowData{Timestamp: stringFromRecord(line.Record, "timestamp"), InverterTimestamp: stringFromRecord(line.Record, "inverter_timestamp")}
		if row.Timestamp == "" && !line.Time.IsZero() {
			row.Timestamp = line.Time.Format("2006-01-02 15:04:05")
		}
		for _, h := range dashboardCompareCodes {
			row.Values = append(row.Values, DashboardMetricValue{Code: h.Code, Label: h.Label, Value: fmt.Sprint(formatLogCell(line.Record[h.Code]))})
		}
		data.RecentRows = append(data.RecentRows, row)
	}

	for _, code := range []string{"inverter_time_year_month", "inverter_time_day_hour", "inverter_time_minute_second"} {
		if info, ok := paramInfo[code]; ok {
			data.RegisterDescriptions = append(data.RegisterDescriptions, DashboardRegisterData{
				Code: fmt.Sprintf("%s / %v", code, deviceParameterAddresses(info)), Name: info.Name, Description: info.Description, Group: "time",
			})
		}
	}
	data.RegisterDescriptions = append(data.RegisterDescriptions, data.Setpoints...)
	data.RegisterDescriptions = append(data.RegisterDescriptions, data.Actuals...)
	return data
}

func (a *App) activeScheduleSummary() (templateName string, scheduleName string, mask int) {
	var tpl sql.NullString
	var sched sql.NullString
	var raw string
	err := a.db.QueryRow(`
		SELECT COALESCE(t.name,''), COALESCE(s.name,''), COALESCE(s.schedule_json,'')
		FROM schedules s
		LEFT JOIN schedule_templates t ON t.id = s.applied_template_id
		ORDER BY s.updated_at DESC, s.id DESC
		LIMIT 1
	`).Scan(&tpl, &sched, &raw)
	if err != nil {
		return "", "", 0
	}
	mask = scheduleUseTimerMask(raw)
	return tpl.String, sched.String, mask
}

type dashboardRawLogLine struct {
	Record   map[string]any
	Raw      string
	Time     time.Time
	FileName string
}

func latestNInverterLogRecords(limit int) []dashboardRawLogLine {
	if limit <= 0 {
		limit = 1
	}
	files, err := listInverterLogFiles()
	if err != nil || len(files) == 0 {
		return nil
	}
	result := make([]dashboardRawLogLine, 0, limit)
	tailLimit := limit*10 + 50
	if tailLimit < 100 {
		tailLimit = 100
	}
	for _, f := range files {
		path := filepath.Join(inverterLogDir, f.Name)
		lines, err := tailFileLines(path, tailLimit)
		if err != nil {
			continue
		}
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			result = append(result, dashboardRawLogLine{Record: rec, Raw: line, Time: parseLogRecordTime(rec), FileName: f.Name})
			if len(result) >= limit {
				return result
			}
		}
	}
	return result
}

func latestInverterLogRecord() (map[string]any, string, bool) {
	records := latestNInverterLogRecords(1)
	if len(records) == 0 {
		return nil, "", false
	}
	return records[0].Record, records[0].FileName, true
}

func stringFromRecord(rec map[string]any, key string) string {
	if rec == nil {
		return ""
	}
	v, ok := rec[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(formatLogCell(v)))
}

func splitDateTimeForDisplay(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "—", "—"
	}
	if t, ok := parseLogTimeFilter(value); ok {
		return t.Format("2006-01-02"), t.Format("15:04:05")
	}
	parts := strings.Fields(value)
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return value, "—"
}

func humanSince(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%d сек назад", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	}
	return fmt.Sprintf("%d дн назад", int(d.Hours()/24))
}

func isMetaLogField(k string) bool {
	switch k {
	case "timestamp", "inverter_timestamp", "inverter_time_status", "inverter_time_register_62", "inverter_time_register_63", "inverter_time_register_64",
		"inverter_id", "inverter_name", "inverter_ip", "inverter_port", "inverter_model_key", "inverter_model_name", "parameters_file":
		return true
	default:
		return false
	}
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
