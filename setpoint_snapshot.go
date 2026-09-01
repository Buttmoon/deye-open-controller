package main

import (
	"database/sql"
	"time"
)

func (a *App) currentSetpointSnapshotForInverter(inverterID int64, settingsTimezone string, now time.Time) map[string]any {
	out := map[string]any{
		"Enabled":              false,
		"SellModeKW":           0,
		"SellModeBattCapacity": 0,
		"ChargeMode":           0,
		"GridExportLimit":      0,
		"GridChargeEnabled":    false,
		"SolarExport":          false,
		"LoadLimitMode":        0,
		"UseTimer":             0,
		"PriorityLoad":         0,
	}
	var raw string
	err := a.db.QueryRow(`SELECT schedule_json FROM schedules WHERE inverter_id = ?`, inverterID).Scan(&raw)
	if err != nil {
		if err != sql.ErrNoRows {
			a.appendAppLog("warn", "setpoint snapshot schedule read failed", map[string]any{"component": "inverter_logger", "inverter_id": inverterID, "error": err.Error()})
		}
		return out
	}
	loc := time.UTC
	if settingsTimezone != "" {
		if loaded, err := time.LoadLocation(settingsTimezone); err == nil {
			loc = loaded
		}
	}
	localNow := now.In(loc)
	payload, err := parseSchedulePayloadLoose(raw)
	if err != nil {
		return out
	}
	normalizeSchedulePayloadSellTimes(&payload)
	day := localNow.Day()
	hour := localNow.Hour()
	minute := localNow.Minute() / 5 * 5
	for _, d := range payload.Days {
		if d.Day != day {
			continue
		}
		idx := slotIndex(hour, minute)
		if idx < 0 || idx >= len(d.Slots) {
			break
		}
		sl := d.Slots[idx]
		out["Enabled"] = scheduleUseTimerEnabled(raw) && sl.Enabled
		out["SellModeKW"] = sl.SellModeKW
		out["SellModeBattCapacity"] = sl.SellModeBattCapacity
		out["ChargeMode"] = sl.ChargeMode
		out["GridExportLimit"] = sl.GridExportLimit
		out["GridChargeEnabled"] = sl.GridChargeEnabled
		out["SolarExport"] = sl.SolarExport
		out["LoadLimitMode"] = sl.LoadLimitMode
		out["UseTimer"] = normalizeUseTimerMask(payload.UseTimerMask)
		out["PriorityLoad"] = sl.PriorityLoad
		out["ScheduleHour"] = sl.Label
		out["SchedulePoint"] = sl.Point
		out["ScheduleSellTime"] = sl.SellTime
		return out
	}
	return out
}
