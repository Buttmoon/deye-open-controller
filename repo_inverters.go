package main

import (
	"fmt"
	"strings"

	"inverter-schedule/internal/models"
)

func (a *App) createInverter(name, ip string, port int, modelKey string, profileWriteConfirmed bool) (int64, error) {
	modelKey = normalizeInverterModelKey(modelKey)
	model, err := findInverterModel(modelKey)
	if err != nil {
		return 0, err
	}
	confirmed := profileWriteConfirmed || !model.WriteRequiresConfirm
	result, err := a.db.Exec(`INSERT INTO inverters (name, ip, port, model_key, profile_write_confirmed) VALUES (?, ?, ?, ?, ?)`, name, ip, port, modelKey, boolToInt(confirmed))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (a *App) updateInverterName(id int64, name string) error {
	_, err := a.db.Exec(`UPDATE inverters SET name = ? WHERE id = ?`, name, id)
	return err
}

func (a *App) updateInverterSettings(id int64, name, modelKey string, profileWriteConfirmed bool) error {
	modelKey = normalizeInverterModelKey(modelKey)
	model, err := findInverterModel(modelKey)
	if err != nil {
		return err
	}
	confirmed := profileWriteConfirmed || !model.WriteRequiresConfirm
	_, err = a.db.Exec(`UPDATE inverters SET name = ?, model_key = ?, profile_write_confirmed = ? WHERE id = ?`, strings.TrimSpace(name), modelKey, boolToInt(confirmed), id)
	return err
}

func (a *App) deleteInverterByID(id int64) error {
	_, err := a.db.Exec(`DELETE FROM inverters WHERE id = ?`, id)
	return err
}

func applyInverterModelMetadata(inv *models.Inverter) {
	inv.ModelKey = normalizeInverterModelKey(inv.ModelKey)
	model, err := findInverterModel(inv.ModelKey)
	if err != nil {
		inv.ModelName = inv.ModelKey
		inv.ValidationStatus = "profile_error"
		return
	}
	inv.ModelName = model.Name
	inv.ModelCode = model.ModelCode
	inv.ParametersFile = model.ParametersFile
	inv.ValidationStatus = model.ValidationStatus
	inv.WriteRequiresConfirm = model.WriteRequiresConfirm
}

func normalizeInverterName(inv *models.Inverter) {
	if strings.TrimSpace(inv.Name) == "" {
		inv.Name = fmt.Sprintf("Инвертор %d", inv.ID)
	}
}

func (a *App) scanInverterRows(rows interface{ Scan(dest ...any) error }) (models.Inverter, error) {
	var inv models.Inverter
	var hasSchedule, scheduleEnabled, hasTemplate, profileWriteConfirmed int
	if err := rows.Scan(&inv.ID, &inv.Name, &inv.IP, &inv.Port, &inv.ModelKey, &profileWriteConfirmed, &hasSchedule, &scheduleEnabled, &hasTemplate, &inv.AppliedTemplateID, &inv.AppliedTemplate); err != nil {
		return inv, err
	}
	normalizeInverterName(&inv)
	applyInverterModelMetadata(&inv)
	inv.ProfileWriteConfirmed = profileWriteConfirmed == 1
	inv.HasSchedule = hasSchedule == 1
	inv.ScheduleEnabled = scheduleEnabled == 1
	inv.HasTemplate = hasTemplate == 1
	return inv, nil
}

func (a *App) getPaginatedInverters(search string, page, perPage int) ([]models.Inverter, int, error) {
	return a.getPaginatedInvertersNoCheck(search, page, perPage)
}

func (a *App) getPaginatedInvertersNoCheck(search string, page, perPage int) ([]models.Inverter, int, error) {
	searchLike := "%"
	if search != "" {
		searchLike = "%" + search + "%"
	}
	var totalCount int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM inverters WHERE name LIKE ? OR ip LIKE ?`, searchLike, searchLike).Scan(&totalCount); err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * perPage
	if offset < 0 {
		offset = 0
	}
	rows, err := a.db.Query(`
		SELECT i.id, i.name, i.ip, i.port, i.model_key,
		COALESCE(i.profile_write_confirmed, 0),
		CASE WHEN s.id IS NOT NULL THEN 1 ELSE 0 END,
		COALESCE(s.is_enabled, 0),
		CASE WHEN s.applied_template_id IS NOT NULL THEN 1 ELSE 0 END,
		COALESCE(s.applied_template_id, 0),
		COALESCE(t.name, '')
		FROM inverters i
		LEFT JOIN schedules s ON s.inverter_id = i.id
		LEFT JOIN schedule_templates t ON t.id = s.applied_template_id
		WHERE i.name LIKE ? OR i.ip LIKE ?
		ORDER BY i.id DESC LIMIT ? OFFSET ?`, searchLike, searchLike, perPage, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []models.Inverter
	for rows.Next() {
		inv, err := a.scanInverterRows(rows)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, inv)
	}
	return result, totalCount, rows.Err()
}

func (a *App) getInvertersByIDs(ids []int64) ([]models.Inverter, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := fmt.Sprintf(`
		SELECT i.id, i.name, i.ip, i.port, i.model_key,
		COALESCE(i.profile_write_confirmed, 0),
		CASE WHEN s.id IS NOT NULL THEN 1 ELSE 0 END,
		COALESCE(s.is_enabled, 0),
		CASE WHEN s.applied_template_id IS NOT NULL THEN 1 ELSE 0 END,
		COALESCE(s.applied_template_id, 0),
		COALESCE(t.name, '')
		FROM inverters i
		LEFT JOIN schedules s ON s.inverter_id = i.id
		LEFT JOIN schedule_templates t ON t.id = s.applied_template_id
		WHERE i.id IN (%s)
		ORDER BY i.id ASC`, placeholders(len(ids)))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.Inverter
	for rows.Next() {
		inv, err := a.scanInverterRows(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, inv)
	}
	return result, rows.Err()
}

func (a *App) getBasicInvertersByIDs(ids []int64) ([]models.Inverter, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := fmt.Sprintf(`SELECT id, name, ip, port, model_key, COALESCE(profile_write_confirmed, 0) FROM inverters WHERE id IN (%s) ORDER BY id ASC`, placeholders(len(ids)))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.Inverter
	for rows.Next() {
		var inv models.Inverter
		var profileWriteConfirmed int
		if err := rows.Scan(&inv.ID, &inv.Name, &inv.IP, &inv.Port, &inv.ModelKey, &profileWriteConfirmed); err != nil {
			return nil, err
		}
		normalizeInverterName(&inv)
		applyInverterModelMetadata(&inv)
		inv.ProfileWriteConfirmed = profileWriteConfirmed == 1
		result = append(result, inv)
	}
	return result, rows.Err()
}

func (a *App) getAllInvertersNoCheck() ([]models.Inverter, error) {
	rows, err := a.db.Query(`
		SELECT i.id, i.name, i.ip, i.port, i.model_key,
		COALESCE(i.profile_write_confirmed, 0),
		CASE WHEN s.id IS NOT NULL THEN 1 ELSE 0 END,
		COALESCE(s.is_enabled, 0),
		CASE WHEN s.applied_template_id IS NOT NULL THEN 1 ELSE 0 END,
		COALESCE(s.applied_template_id, 0),
		COALESCE(t.name, '')
		FROM inverters i
		LEFT JOIN schedules s ON s.inverter_id = i.id
		LEFT JOIN schedule_templates t ON t.id = s.applied_template_id
		ORDER BY i.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.Inverter
	for rows.Next() {
		inv, err := a.scanInverterRows(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, inv)
	}
	return result, rows.Err()
}
