package main

import (
	"database/sql"

	"inverter-schedule/internal/models"
)

func (a *App) ensureScheduleExists(inverterID int64) error {
	defaultJSON := defaultScheduleJSONForEditor()
	_, err := a.db.Exec(`
		INSERT INTO schedules (inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id)
		VALUES (?, 'Расписание', 0, 'list', ?, NULL)
		ON CONFLICT(inverter_id) DO NOTHING
	`, inverterID, defaultJSON)
	return err
}

func (a *App) toggleScheduleByInverterID(inverterID int64, enabled bool) error {
	var raw string
	err := a.db.QueryRow(`SELECT schedule_json FROM schedules WHERE inverter_id = ?`, inverterID).Scan(&raw)
	if err == sql.ErrNoRows {
		if err := a.ensureScheduleExists(inverterID); err != nil {
			return err
		}
		raw = defaultScheduleJSONForEditor()
	} else if err != nil {
		return err
	}
	updated := setScheduleUseTimerEnabled(raw, enabled)
	if compact, err := normalizeScheduleJSONCompact(updated); err == nil {
		updated = compact
	}
	value := 0
	if enabled {
		value = 1
	}
	_, err = a.db.Exec(`UPDATE schedules SET is_enabled = ?, schedule_json = ?, updated_at = CURRENT_TIMESTAMP WHERE inverter_id = ?`, value, updated, inverterID)
	return err
}

func (a *App) saveScheduleForInverter(inverterID int64, name, viewMode, scheduleJSON string) error {
	return a.saveSchedulesForInverters([]int64{inverterID}, 0, name, viewMode, scheduleJSON)
}

func (a *App) saveScheduleForInverterWithTemplate(inverterID, templateID int64, name, viewMode, scheduleJSON string) error {
	return a.saveSchedulesForInverters([]int64{inverterID}, templateID, name, viewMode, scheduleJSON)
}

func (a *App) saveSchedulesForInverters(inverterIDs []int64, templateID int64, name, viewMode, scheduleJSON string) error {
	if len(inverterIDs) == 0 {
		return nil
	}
	if normalized, err := normalizeScheduleJSONCompact(scheduleJSON); err == nil {
		scheduleJSON = normalized
	}
	enabled := 0
	if scheduleUseTimerEnabled(scheduleJSON) {
		enabled = 1
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stmt *sql.Stmt
	if templateID > 0 {
		stmt, err = tx.Prepare(`
			INSERT INTO schedules (inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(inverter_id) DO UPDATE SET
				name = excluded.name,
				is_enabled = excluded.is_enabled,
				view_mode = excluded.view_mode,
				schedule_json = excluded.schedule_json,
				applied_template_id = excluded.applied_template_id,
				updated_at = CURRENT_TIMESTAMP
		`)
	} else {
		stmt, err = tx.Prepare(`
			INSERT INTO schedules (inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id, updated_at)
			VALUES (?, ?, ?, ?, ?, NULL, CURRENT_TIMESTAMP)
			ON CONFLICT(inverter_id) DO UPDATE SET
				name = excluded.name,
				is_enabled = excluded.is_enabled,
				view_mode = excluded.view_mode,
				schedule_json = excluded.schedule_json,
				updated_at = CURRENT_TIMESTAMP
		`)
	}
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, inverterID := range inverterIDs {
		if templateID > 0 {
			if _, err := stmt.Exec(inverterID, name, enabled, viewMode, scheduleJSON, templateID); err != nil {
				return err
			}
		} else {
			if _, err := stmt.Exec(inverterID, name, enabled, viewMode, scheduleJSON); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (a *App) getScheduleByInverterID(inverterID int64) (models.Schedule, error) {
	var s models.Schedule
	var isEnabled int
	var appliedTemplateID sql.NullInt64
	err := a.db.QueryRow(`
		SELECT id, inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id, updated_at
		FROM schedules
		WHERE inverter_id = ?
	`, inverterID).Scan(&s.ID, &s.InverterID, &s.Name, &isEnabled, &s.ViewMode, &s.ScheduleJSON, &appliedTemplateID, &s.UpdatedAt)
	s.IsEnabled = isEnabled == 1
	if err == nil {
		s.IsEnabled = scheduleUseTimerEnabled(s.ScheduleJSON)
	}
	return s, err
}
