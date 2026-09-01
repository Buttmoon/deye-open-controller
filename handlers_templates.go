package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) validateTemplateScheduleForSave(templateID int64, scheduleJSON string) error {
	models, err := availableInverterModels()
	if err != nil {
		return fmt.Errorf("ошибка загрузки каталога моделей: %w", err)
	}
	validationModel := InverterModelDefinition{Name: "универсальный шаблон"}
	for _, model := range models {
		if model.SchedulePowerMaxW > validationModel.SchedulePowerMaxW {
			validationModel.SchedulePowerMaxW = model.SchedulePowerMaxW
		}
		if model.GridExportMaxW > validationModel.GridExportMaxW {
			validationModel.GridExportMaxW = model.GridExportMaxW
		}
	}
	if err := validateScheduleJSONForModels(scheduleJSON, []InverterModelDefinition{validationModel}); err != nil {
		return fmt.Errorf("шаблон не прошёл проверку: %w", err)
	}
	if templateID <= 0 {
		return nil
	}
	rows, err := a.db.Query(`SELECT i.model_key FROM schedules s JOIN inverters i ON i.id=s.inverter_id WHERE s.applied_template_id=?`, templateID)
	if err != nil {
		return fmt.Errorf("не удалось проверить привязанные инверторы: %w", err)
	}
	defer rows.Close()
	boundModels := []InverterModelDefinition{}
	for rows.Next() {
		var modelKey string
		if err := rows.Scan(&modelKey); err != nil {
			return err
		}
		model, err := findInverterModel(modelKey)
		if err != nil {
			return err
		}
		boundModels = append(boundModels, model)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(boundModels) > 0 {
		if err := validateScheduleJSONForModels(scheduleJSON, boundModels); err != nil {
			return fmt.Errorf("изменение несовместимо с инверторами, где шаблон уже применён: %w", err)
		}
	}
	return nil
}

func templateEditorPowerLimits() (int, int) {
	models, err := availableInverterModels()
	if err != nil {
		return 60000, 60000
	}
	powerMax, exportMax := 0, 0
	for _, model := range models {
		if model.SchedulePowerMaxW > powerMax {
			powerMax = model.SchedulePowerMaxW
		}
		if model.GridExportMaxW > exportMax {
			exportMax = model.GridExportMaxW
		}
	}
	return powerMax, exportMax
}

func (a *App) createTemplateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	viewMode := strings.TrimSpace(r.FormValue("view_mode"))
	scheduleJSON := strings.TrimSpace(r.FormValue("schedule_json"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Имя шаблона обязательно"})
		return
	}
	if viewMode != "grid" {
		viewMode = "list"
	}
	if scheduleJSON == "" {
		scheduleJSON = defaultScheduleJSON()
	}
	if !isValidScheduleJSON(scheduleJSON) {
		if normalized, err := normalizeScheduleJSONSellTimes(scheduleJSON); err == nil {
			scheduleJSON = normalized
		}
		if !isValidScheduleJSON(scheduleJSON) {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON шаблона"})
			return
		}
	}
	if err := a.validateTemplateScheduleForSave(0, scheduleJSON); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if err := a.createTemplate(name, description, viewMode, scheduleJSON); err != nil {
		msg := "Ошибка сохранения шаблона"
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
			msg = "Шаблон с таким именем уже существует"
		}
		log.Println("createTemplate error:", err)
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: msg})
		return
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Шаблон сохранён"})
}

func (a *App) deleteTemplateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный ID шаблона"})
		return
	}
	if err := a.deleteTemplate(id); err != nil {
		log.Println("deleteTemplate error:", err)
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Шаблон не найден"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка удаления шаблона"})
		return
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Шаблон удалён", Redirect: "/templates/list"})
}

func (a *App) applyTemplateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Неверный шаблон"})
		return
	}
	tpl, err := a.getTemplateByID(id)
	if err != nil {
		log.Println("getTemplateByID error:", err)
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка загрузки шаблона"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(templateApplyResponse{OK: true, Message: "Шаблон загружен", ID: tpl.ID, Name: tpl.Name, ViewMode: tpl.ViewMode, ScheduleJSON: compactScheduleJSONForEditor(tpl.ScheduleJSON)})
}

func (a *App) templatesListHandler(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	perPage := 10
	templates, totalCount, err := a.getPaginatedTemplateList(search, page, perPage)
	if err != nil {
		http.Error(w, "Ошибка загрузки шаблонов", http.StatusInternalServerError)
		log.Println("getPaginatedTemplateList error:", err)
		return
	}
	totalPages := int(math.Ceil(float64(totalCount) / float64(perPage)))
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	data := TemplatesListPageData{Title: "Шаблоны расписаний", Templates: templates, Search: search, Page: page, PerPage: perPage, TotalPages: totalPages, HasPrev: page > 1, HasNext: page < totalPages, PrevPage: page - 1, NextPage: page + 1}
	if err := a.tmplTemplatesList.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга списка шаблонов", http.StatusInternalServerError)
		log.Println("tmplTemplatesList.Execute error:", err)
	}
}

func (a *App) templateEditPageHandler(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	powerMax, exportMax := templateEditorPowerLimits()
	data := TemplateEditPageData{Title: "Создание шаблона", TemplateID: 0, Name: "", Description: "", ViewMode: "list", ScheduleJSON: defaultScheduleJSONForEditor(), ChargeModeOptionsJSON: chargeModeOptionsJSON(), SchedulePowerMaxW: powerMax, GridExportMaxW: exportMax, IsEdit: false}
	if idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err == nil && id > 0 {
			tpl, err := a.getTemplateByID(id)
			if err == nil {
				data = TemplateEditPageData{Title: "Редактирование шаблона", TemplateID: tpl.ID, Name: tpl.Name, Description: tpl.Description, ViewMode: tpl.ViewMode, ScheduleJSON: compactScheduleJSONForEditor(tpl.ScheduleJSON), ChargeModeOptionsJSON: chargeModeOptionsJSON(), SchedulePowerMaxW: powerMax, GridExportMaxW: exportMax, IsEdit: true}
			}
		}
	}
	if err := a.tmplTemplateEdit.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга шаблона", http.StatusInternalServerError)
		log.Println("tmplTemplateEdit.Execute error:", err)
	}
}

func (a *App) templateSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	idStr := strings.TrimSpace(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	viewMode := strings.TrimSpace(r.FormValue("view_mode"))
	scheduleJSON := strings.TrimSpace(r.FormValue("schedule_json"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Имя шаблона обязательно"})
		return
	}
	if viewMode != "grid" {
		viewMode = "list"
	}
	if scheduleJSON == "" {
		scheduleJSON = defaultScheduleJSON()
	}
	if !isValidScheduleJSON(scheduleJSON) {
		if normalized, err := normalizeScheduleJSONSellTimes(scheduleJSON); err == nil {
			scheduleJSON = normalized
		}
		if !isValidScheduleJSON(scheduleJSON) {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON шаблона"})
			return
		}
	}
	templateID := int64(0)
	if idStr != "" && idStr != "0" {
		parsedID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || parsedID <= 0 {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный ID шаблона"})
			return
		}
		templateID = parsedID
	}
	if err := a.validateTemplateScheduleForSave(templateID, scheduleJSON); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if templateID == 0 {
		if err := a.createTemplate(name, description, viewMode, scheduleJSON); err != nil {
			msg := "Ошибка сохранения шаблона"
			if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
				msg = "Шаблон с таким именем уже существует"
			}
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: msg})
			return
		}
	} else {
		if err := a.updateTemplate(templateID, name, description, viewMode, scheduleJSON); err != nil {
			msg := "Ошибка обновления шаблона"
			if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
				msg = "Шаблон с таким именем уже существует"
			}
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: msg})
			return
		}
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Шаблон сохранён", Redirect: "/templates/list"})
}

func (a *App) templateApplyPageHandler(w http.ResponseWriter, r *http.Request) {
	ids := parseIDsCSV(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	inverters, err := a.getBasicInvertersByIDs(ids)
	if err != nil {
		http.Error(w, "Ошибка загрузки инверторов", http.StatusInternalServerError)
		log.Println("getInvertersByIDs error:", err)
		return
	}
	templates, err := a.getTemplateOptions()
	if err != nil {
		http.Error(w, "Ошибка загрузки шаблонов", http.StatusInternalServerError)
		log.Println("getTemplates error:", err)
		return
	}
	data := TemplateApplyPageData{Title: "Быстрая установка шаблона", Inverters: inverters, Templates: templates, SelectedIDs: joinIDs(ids), SelectedCount: len(ids)}
	if err := a.tmplTemplateApply.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы установки шаблона", http.StatusInternalServerError)
		log.Println("tmplTemplateApply.Execute error:", err)
	}
}

func (a *App) applyTemplateToInvertersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	ids := parseIDsCSV(r.FormValue("ids"))
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не выбраны инверторы"})
		return
	}
	templateID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("template_id")), 10, 64)
	if err != nil || templateID <= 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не выбран шаблон"})
		return
	}
	tpl, err := a.getTemplateByID(templateID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка загрузки шаблона"})
		return
	}
	selectedInverters, err := a.getBasicInvertersByIDs(ids)
	if err != nil || len(selectedInverters) != len(ids) {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не удалось определить модели выбранных инверторов"})
		return
	}
	modelsForValidation := make([]InverterModelDefinition, 0, len(selectedInverters))
	for _, inv := range selectedInverters {
		model, modelErr := findInverterModel(inv.ModelKey)
		if modelErr != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: modelErr.Error()})
			return
		}
		modelsForValidation = append(modelsForValidation, model)
	}
	if err := validateScheduleJSONForModels(tpl.ScheduleJSON, modelsForValidation); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Шаблон несовместим с выбранными инверторами: " + err.Error()})
		return
	}
	if err := a.saveSchedulesForInverters(ids, tpl.ID, tpl.Name, tpl.ViewMode, tpl.ScheduleJSON); err != nil {
		log.Println("saveSchedulesForInverters template apply error:", err)
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка применения шаблона"})
		return
	}
	a.recreateActiveSchedulerTask("template applied to schedules")
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Шаблон успешно применён, задача пересоздана", Redirect: "/"})
}
