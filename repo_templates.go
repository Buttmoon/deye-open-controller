package main

import (
	"database/sql"
	"fmt"
	"strings"

	"inverter-schedule/internal/models"
)

func (a *App) getTemplates() ([]models.ScheduleTemplate, error) {
	rows, err := a.db.Query(`SELECT id, name, description, view_mode, schedule_json, created_at FROM schedule_templates ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.ScheduleTemplate
	for rows.Next() {
		var t models.ScheduleTemplate
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.ViewMode, &t.ScheduleJSON, &t.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (a *App) getTemplateOptions() ([]models.ScheduleTemplate, error) {
	rows, err := a.db.Query(`SELECT id, name FROM schedule_templates ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.ScheduleTemplate
	for rows.Next() {
		var t models.ScheduleTemplate
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (a *App) createTemplate(name, description, viewMode, scheduleJSON string) error {
	if normalized, err := normalizeScheduleJSONCompact(scheduleJSON); err == nil {
		scheduleJSON = normalized
	}
	_, err := a.db.Exec(`INSERT INTO schedule_templates (name, description, view_mode, schedule_json) VALUES (?, ?, ?, ?)`, name, description, viewMode, scheduleJSON)
	return err
}

func (a *App) updateTemplate(id int64, name, description, viewMode, scheduleJSON string) error {
	if normalized, err := normalizeScheduleJSONCompact(scheduleJSON); err == nil {
		scheduleJSON = normalized
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE schedule_templates SET name = ?, description = ?, view_mode = ?, schedule_json = ? WHERE id = ?`, name, description, viewMode, scheduleJSON, id); err != nil {
		return err
	}

	enabled := 0
	if scheduleUseTimerEnabled(scheduleJSON) {
		enabled = 1
	}

	if _, err := tx.Exec(`
		UPDATE schedules
		SET name = ?, is_enabled = ?, view_mode = ?, schedule_json = ?, updated_at = CURRENT_TIMESTAMP
		WHERE applied_template_id = ?
	`, name, enabled, viewMode, scheduleJSON, id); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) deleteTemplate(id int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		UPDATE schedules
		SET applied_template_id = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE applied_template_id = ?
	`, id); err != nil {
		return err
	}

	res, err := tx.Exec(`DELETE FROM schedule_templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}

	return tx.Commit()
}

func (a *App) getTemplateByID(id int64) (models.ScheduleTemplate, error) {
	var t models.ScheduleTemplate
	err := a.db.QueryRow(`SELECT id, name, description, view_mode, schedule_json, created_at FROM schedule_templates WHERE id = ?`, id).Scan(&t.ID, &t.Name, &t.Description, &t.ViewMode, &t.ScheduleJSON, &t.CreatedAt)
	return t, err
}

func (a *App) getPaginatedTemplateList(search string, page, perPage int) ([]models.ScheduleTemplateListItem, int, error) {
	searchLike := "%"
	if search != "" {
		searchLike = "%" + search + "%"
	}
	var totalCount int
	if err := a.db.QueryRow(`
		SELECT COUNT(DISTINCT t.id)
		FROM schedule_templates t
		LEFT JOIN schedules s ON s.applied_template_id = t.id
		LEFT JOIN inverters i ON i.id = s.inverter_id
		WHERE t.name LIKE ? OR i.name LIKE ? OR i.ip LIKE ?
	`, searchLike, searchLike, searchLike).Scan(&totalCount); err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * perPage
	if offset < 0 {
		offset = 0
	}
	rows, err := a.db.Query(`
		SELECT t.id, t.name, t.description, t.view_mode, t.created_at, i.name, i.ip, i.port
		FROM schedule_templates t
		LEFT JOIN schedules s ON s.applied_template_id = t.id
		LEFT JOIN inverters i ON i.id = s.inverter_id
		WHERE t.id IN (
			SELECT DISTINCT t2.id
			FROM schedule_templates t2
			LEFT JOIN schedules s2 ON s2.applied_template_id = t2.id
			LEFT JOIN inverters i2 ON i2.id = s2.inverter_id
			WHERE t2.name LIKE ? OR i2.name LIKE ? OR i2.ip LIKE ?
			ORDER BY t2.id DESC
			LIMIT ? OFFSET ?
		)
		ORDER BY t.id DESC, i.name ASC
	`, searchLike, searchLike, searchLike, perPage, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	templateMap := make(map[int64]*models.ScheduleTemplateListItem)
	order := make([]int64, 0)
	for rows.Next() {
		var id int64
		var name, description, viewMode, createdAt string
		var invName, invIP sql.NullString
		var invPort sql.NullInt64
		if err := rows.Scan(&id, &name, &description, &viewMode, &createdAt, &invName, &invIP, &invPort); err != nil {
			return nil, 0, err
		}
		item, exists := templateMap[id]
		if !exists {
			item = &models.ScheduleTemplateListItem{ID: id, Name: name, Description: description, ViewMode: viewMode, CreatedAt: createdAt, UsedInverters: []string{}}
			templateMap[id] = item
			order = append(order, id)
		}
		if invName.Valid || invIP.Valid {
			label := strings.TrimSpace(invName.String)
			if label == "" {
				label = "Инвертор"
			}
			ip := strings.TrimSpace(invIP.String)
			port := 0
			if invPort.Valid {
				port = int(invPort.Int64)
			}
			item.UsedInverters = append(item.UsedInverters, fmt.Sprintf("%s | %s | %d", label, ip, port))
			item.InverterCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	result := make([]models.ScheduleTemplateListItem, 0, len(order))
	for _, id := range order {
		result = append(result, *templateMap[id])
	}
	return result, totalCount, nil
}
