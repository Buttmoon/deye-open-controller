package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"
)

func (a *App) getScheduledInvertersForExecution() ([]runtimeScheduledItem, error) {
	rows, err := a.db.Query(`
		SELECT
			i.id,
			i.name,
			i.ip,
			i.port,
			i.model_key,
			COALESCE(i.profile_write_confirmed, 0),
			t.id,
			t.name,
			s.name,
			s.schedule_json,
			COALESCE(s.is_enabled, 0)
		FROM inverters i
		JOIN schedules s ON s.inverter_id = i.id
		LEFT JOIN schedule_templates t ON t.id = s.applied_template_id
		ORDER BY i.id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []runtimeScheduledItem{}
	for rows.Next() {
		var item runtimeScheduledItem
		var templateID sql.NullInt64
		var templateName sql.NullString
		var scheduleEnabled int
		var profileWriteConfirmed int
		if err := rows.Scan(
			&item.InverterID,
			&item.InverterName,
			&item.IP,
			&item.Port,
			&item.ModelKey,
			&profileWriteConfirmed,
			&templateID,
			&templateName,
			&item.ScheduleName,
			&item.ScheduleJSON,
			&scheduleEnabled,
		); err != nil {
			return nil, err
		}
		if !scheduleUseTimerEnabled(item.ScheduleJSON) {
			continue
		}
		if scheduleEnabled != 1 {
			// Лечим старые базы: раньше активность могла жить только в JSON Use Timer.
			// Для выполнения расписания источником истины остаётся schedule_json.
			_, _ = a.db.Exec(`UPDATE schedules SET is_enabled = 1 WHERE inverter_id = ?`, item.InverterID)
		}
		if templateID.Valid {
			item.TemplateID = templateID.Int64
		}
		if templateName.Valid {
			item.TemplateName = templateName.String
		}
		item.ProfileWriteConfirmed = profileWriteConfirmed == 1
		item.ModelKey = normalizeInverterModelKey(item.ModelKey)
		if model, modelErr := findInverterModel(item.ModelKey); modelErr == nil {
			item.ModelName = model.Name
			item.ParametersFile = model.ParametersFile
		} else {
			return nil, modelErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) insertTaskRun(inverterID, templateID int64, status, message, payloadJSON, command string) error {
	var inverterValue any
	if inverterID > 0 {
		inverterValue = inverterID
	}
	var templateValue any
	if templateID > 0 {
		templateValue = templateID
	}
	return a.insertTaskRunValues(inverterValue, templateValue, status, message, payloadJSON, command)
}

func (a *App) insertSystemTaskRun(status, message, payloadJSON, command string) error {
	return a.insertTaskRunValues(nil, nil, status, message, payloadJSON, command)
}

func (a *App) insertTaskRunValues(inverterID any, templateID any, status, message, payloadJSON, command string) error {
	if command != "" {
		message = message + " | command: " + command
	}
	_, err := a.db.Exec(
		`INSERT INTO task_runs (inverter_id, template_id, status, message, payload_json) VALUES (?, ?, ?, ?, ?)`,
		inverterID,
		templateID,
		status,
		message,
		payloadJSON,
	)
	if err != nil {
		log.Printf("insert task run error: status=%s message=%q error=%v", status, message, err)
		a.appendAppLog("error", "task run insert failed", map[string]any{
			"component": "task_log",
			"status":    status,
			"message":   message,
			"error":     err.Error(),
		})
	}
	return err
}

func (a *App) getTaskRunLogs(limit int, timezone string) ([]TaskRunLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := a.db.Query(`
		SELECT tr.id, tr.executed_at_utc, COALESCE(i.name,''), COALESCE(i.ip,''), COALESCE(i.port,0), COALESCE(t.name,''), tr.status, tr.message, tr.payload_json
		FROM task_runs tr
		LEFT JOIN inverters i ON i.id = tr.inverter_id
		LEFT JOIN schedule_templates t ON t.id = tr.template_id
		ORDER BY tr.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	logs := []TaskRunLog{}
	loc, _ := time.LoadLocation(timezone)
	if loc == nil {
		loc = time.UTC
	}
	for rows.Next() {
		var l TaskRunLog
		if err := rows.Scan(&l.ID, &l.ExecutedAtUTC, &l.InverterName, &l.InverterIP, &l.InverterPort, &l.TemplateName, &l.Status, &l.Message, &l.PayloadJSON); err != nil {
			return nil, err
		}
		if parsed, err := time.Parse("2006-01-02 15:04:05", l.ExecutedAtUTC); err == nil {
			l.ExecutedAtLocal = parsed.In(loc).Format("2006-01-02 15:04:05 MST")
		}
		if l.PayloadJSON != "" {
			var payload map[string]any
			if json.Unmarshal([]byte(l.PayloadJSON), &payload) == nil {
				if inv, ok := payload["inverter"].(map[string]any); ok {
					if l.InverterName == "" {
						if v, ok := inv["name"].(string); ok {
							l.InverterName = v
						}
					}
					if l.InverterIP == "" {
						if v, ok := inv["ip"].(string); ok {
							l.InverterIP = v
						}
					}
					if l.InverterPort == 0 {
						if v, ok := inv["port"].(float64); ok {
							l.InverterPort = int(v)
						}
					}
				}
				if l.TemplateName == "" {
					if t, ok := payload["template"].(map[string]any); ok {
						if v, ok := t["name"].(string); ok {
							l.TemplateName = v
						}
					}
				}
			}
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// getTaskRunLogsPage returns one page of task runs without payload bodies; payloads are fetched separately.
func (a *App) getTaskRunLogsPage(page, perPage int, query, timezone string) ([]TaskRunLog, int, error) {
	if perPage <= 0 || perPage > 500 {
		perPage = 50
	}
	if page < 1 {
		page = 1
	}
	const fromClause = `
		FROM task_runs tr
		LEFT JOIN inverters i ON i.id = tr.inverter_id
		LEFT JOIN schedule_templates t ON t.id = tr.template_id`
	const nameExpr = `COALESCE(NULLIF(i.name,''), CASE WHEN json_valid(tr.payload_json) THEN json_extract(tr.payload_json,'$.inverter.name') END, '')`
	const ipExpr = `COALESCE(NULLIF(i.ip,''), CASE WHEN json_valid(tr.payload_json) THEN json_extract(tr.payload_json,'$.inverter.ip') END, '')`
	const portExpr = `COALESCE(NULLIF(i.port,0), CASE WHEN json_valid(tr.payload_json) THEN CAST(json_extract(tr.payload_json,'$.inverter.port') AS INTEGER) END, 0)`
	const templateExpr = `COALESCE(NULLIF(t.name,''), CASE WHEN json_valid(tr.payload_json) THEN json_extract(tr.payload_json,'$.template.name') END, '')`
	where := ""
	args := []any{}
	if q := strings.ToLower(strings.TrimSpace(query)); q != "" {
		where = ` WHERE instr(lower(` + nameExpr + ` || ' ' || ` + ipExpr + ` || ':' || ` + portExpr + ` || ' ' || ` + templateExpr + ` || ' ' || tr.status || ' ' || tr.message), ?) > 0`
		args = append(args, q)
	}
	var total int
	if err := a.db.QueryRow(`SELECT COUNT(*)`+fromClause+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := a.db.Query(`SELECT tr.id, tr.executed_at_utc, `+nameExpr+`, `+ipExpr+`, `+portExpr+`, `+templateExpr+`, tr.status, tr.message, length(tr.payload_json)`+
		fromClause+where+` ORDER BY tr.id DESC LIMIT ? OFFSET ?`, append(args, perPage, (page-1)*perPage)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	loc, _ := time.LoadLocation(timezone)
	if loc == nil {
		loc = time.UTC
	}
	logs := []TaskRunLog{}
	for rows.Next() {
		var l TaskRunLog
		var payloadLen sql.NullInt64
		if err := rows.Scan(&l.ID, &l.ExecutedAtUTC, &l.InverterName, &l.InverterIP, &l.InverterPort, &l.TemplateName, &l.Status, &l.Message, &payloadLen); err != nil {
			return nil, 0, err
		}
		l.PayloadBytes = int(payloadLen.Int64)
		if parsed, err := time.Parse("2006-01-02 15:04:05", l.ExecutedAtUTC); err == nil {
			l.ExecutedAtLocal = parsed.In(loc).Format("2006-01-02 15:04:05 MST")
		}
		logs = append(logs, l)
	}
	return logs, total, rows.Err()
}

func (a *App) getTaskRunPayload(id int64) (string, error) {
	var payload string
	err := a.db.QueryRow(`SELECT payload_json FROM task_runs WHERE id = ?`, id).Scan(&payload)
	return payload, err
}

func (a *App) clearTaskRunLogs() error { _, err := a.db.Exec(`DELETE FROM task_runs`); return err }

func (a *App) trimTaskRunLogs(maxRows int) error {
	if maxRows <= 0 {
		return nil
	}
	_, err := a.db.Exec(`DELETE FROM task_runs WHERE id NOT IN (SELECT id FROM task_runs ORDER BY id DESC LIMIT ?)`, maxRows)
	return err
}
