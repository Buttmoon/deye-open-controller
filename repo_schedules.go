package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"inverter-schedule/internal/models"
)

func (a *App) ensureScheduleExists(inverterID int64) error {
	defaultJSON := defaultScheduleJSONForEditor()
	_, err := a.db.Exec(`
		INSERT INTO schedules (inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id, revision)
		VALUES (?, 'Расписание', 0, 'list', ?, NULL, 0)
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
	_, err = a.db.Exec(`
		UPDATE schedules
		SET is_enabled = ?, schedule_json = ?, revision = revision + 1, updated_at = CURRENT_TIMESTAMP
		WHERE inverter_id = ?`, value, updated, inverterID)
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
			INSERT INTO schedules (inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id, revision, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, CURRENT_TIMESTAMP)
			ON CONFLICT(inverter_id) DO UPDATE SET
				name = excluded.name,
				is_enabled = excluded.is_enabled,
				view_mode = excluded.view_mode,
				schedule_json = excluded.schedule_json,
				applied_template_id = excluded.applied_template_id,
				revision = schedules.revision + 1,
				updated_at = CURRENT_TIMESTAMP
		`)
	} else {
		stmt, err = tx.Prepare(`
			INSERT INTO schedules (inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id, revision, updated_at)
			VALUES (?, ?, ?, ?, ?, NULL, 1, CURRENT_TIMESTAMP)
			ON CONFLICT(inverter_id) DO UPDATE SET
				name = excluded.name,
				is_enabled = excluded.is_enabled,
				view_mode = excluded.view_mode,
				schedule_json = excluded.schedule_json,
				revision = schedules.revision + 1,
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
		SELECT id, inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id,
		       COALESCE(revision, 0), updated_at, client_updated_at
		FROM schedules
		WHERE inverter_id = ?
	`, inverterID).Scan(&s.ID, &s.InverterID, &s.Name, &isEnabled, &s.ViewMode, &s.ScheduleJSON, &appliedTemplateID, &s.Revision, &s.UpdatedAt, &s.ClientUpdatedAt)
	s.IsEnabled = isEnabled == 1
	s.AppliedTemplateID = appliedTemplateID
	if err == nil {
		s.IsEnabled = scheduleUseTimerEnabled(s.ScheduleJSON)
	}
	return s, err
}

type syncScheduleUpsertInput struct {
	InverterID      int64
	Name            string
	ViewMode        string
	IsEnabled       bool
	ScheduleJSON    string
	IfMatch         int64
	IfMatchPresent  bool
	BodyRevision    int64
	ClientUpdatedAt string
}

type syncScheduleUpsertResult struct {
	Schedule models.Schedule
	Created  bool
}

type syncCASError struct {
	Code    string
	Message string
	Status  int
}

func (e *syncCASError) Error() string { return e.Message }

// upsertScheduleWithRevisionCAS performs create/update on a single reserved
// SQLite connection with BEGIN IMMEDIATE. It never nests BEGIN inside sql.Tx
// and never splits the CAS across pooled connections.
func (a *App) upsertScheduleWithRevisionCAS(ctx context.Context, in syncScheduleUpsertInput) (syncScheduleUpsertResult, error) {
	var out syncScheduleUpsertResult
	if !in.IfMatchPresent {
		return out, &syncCASError{Code: "precondition_required", Message: "If-Match header is required", Status: 428}
	}
	if in.IfMatch < 0 || in.BodyRevision < 0 || in.BodyRevision > 2147483647 {
		return out, &syncCASError{Code: "invalid_revision", Message: "revision out of range", Status: 400}
	}

	conn, err := a.db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()

	if err := beginImmediate(ctx, conn); err != nil {
		if isSQLiteBusyOrLocked(err) {
			return out, &syncCASError{Code: "revision_conflict", Message: "database busy during CAS", Status: 409}
		}
		return out, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var currentRevision sql.NullInt64
	var existingID sql.NullInt64
	qErr := conn.QueryRowContext(ctx, `SELECT id, revision FROM schedules WHERE inverter_id = ?`, in.InverterID).Scan(&existingID, &currentRevision)
	exists := qErr == nil
	if qErr != nil && qErr != sql.ErrNoRows {
		return out, qErr
	}

	serverNow := time.Now().UTC().Format(time.RFC3339)
	enabled := 0
	if in.IsEnabled {
		enabled = 1
	}
	var clientUpdated any
	if strings.TrimSpace(in.ClientUpdatedAt) != "" {
		clientUpdated = strings.TrimSpace(in.ClientUpdatedAt)
	}

	if !exists {
		if in.IfMatch != 0 {
			return out, &syncCASError{Code: "revision_conflict", Message: "first create requires If-Match: 0", Status: 409}
		}
		if in.BodyRevision != 1 {
			return out, &syncCASError{Code: "revision_conflict", Message: "first create requires body revision: 1", Status: 409}
		}
		res, execErr := conn.ExecContext(ctx, `
			INSERT INTO schedules (inverter_id, name, is_enabled, view_mode, schedule_json, applied_template_id, revision, updated_at, client_updated_at)
			VALUES (?, ?, ?, ?, ?, NULL, 1, ?, ?)`,
			in.InverterID, in.Name, enabled, in.ViewMode, in.ScheduleJSON, serverNow, clientUpdated)
		if execErr != nil {
			if isSQLiteConstraintOrBusy(execErr) {
				return out, &syncCASError{Code: "revision_conflict", Message: "concurrent create conflict", Status: 409}
			}
			return out, execErr
		}
		id, _ := res.LastInsertId()
		out.Created = true
		out.Schedule = models.Schedule{
			ID: id, InverterID: in.InverterID, Name: in.Name, IsEnabled: in.IsEnabled,
			ViewMode: in.ViewMode, ScheduleJSON: in.ScheduleJSON, Revision: 1, UpdatedAt: serverNow,
		}
		if strings.TrimSpace(in.ClientUpdatedAt) != "" {
			out.Schedule.ClientUpdatedAt = sql.NullString{String: strings.TrimSpace(in.ClientUpdatedAt), Valid: true}
		}
	} else {
		cur := currentRevision.Int64
		if in.IfMatch != cur {
			return out, &syncCASError{Code: "revision_conflict", Message: fmt.Sprintf("If-Match %d does not match current revision %d", in.IfMatch, cur), Status: 409}
		}
		if in.BodyRevision != cur+1 {
			return out, &syncCASError{Code: "revision_conflict", Message: "body revision must equal If-Match + 1", Status: 409}
		}
		res, execErr := conn.ExecContext(ctx, `
			UPDATE schedules
			SET name = ?, is_enabled = ?, view_mode = ?, schedule_json = ?, revision = ?, updated_at = ?, client_updated_at = ?
			WHERE inverter_id = ? AND revision = ?`,
			in.Name, enabled, in.ViewMode, in.ScheduleJSON, in.BodyRevision, serverNow, clientUpdated, in.InverterID, cur)
		if execErr != nil {
			return out, execErr
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return out, &syncCASError{Code: "revision_conflict", Message: "concurrent update conflict", Status: 409}
		}
		out.Schedule = models.Schedule{
			ID: existingID.Int64, InverterID: in.InverterID, Name: in.Name, IsEnabled: in.IsEnabled,
			ViewMode: in.ViewMode, ScheduleJSON: in.ScheduleJSON, Revision: in.BodyRevision, UpdatedAt: serverNow,
		}
		if strings.TrimSpace(in.ClientUpdatedAt) != "" {
			out.Schedule.ClientUpdatedAt = sql.NullString{String: strings.TrimSpace(in.ClientUpdatedAt), Valid: true}
		}
	}

	// Pause auto-renew; keep rules_json.
	if _, err := conn.ExecContext(ctx, `UPDATE schedule_recurrences SET auto_renew = 0, updated_at = ? WHERE inverter_id = ?`, serverNow, in.InverterID); err != nil {
		return out, err
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return out, err
	}
	committed = true
	return out, nil
}

func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		_, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
		if err == nil {
			return nil
		}
		last = err
		if !isSQLiteBusyOrLocked(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(15*(attempt+1)) * time.Millisecond):
		}
	}
	return last
}

func isSQLiteBusyOrLocked(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "busy") || strings.Contains(msg, "locked")
}

func isSQLiteConstraintOrBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") ||
		strings.Contains(msg, "constraint") ||
		isSQLiteBusyOrLocked(err)
}
