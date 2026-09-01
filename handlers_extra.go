package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func (a *App) templateImportPageHandler(w http.ResponseWriter, r *http.Request) {
	data := TemplateImportPageData{
		Title:        "Импорт шаблонов из XLSX",
		ExampleSheet: templateImportExampleText(),
	}
	if err := a.tmplTemplateImport.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы импорта", http.StatusInternalServerError)
		log.Println("tmplTemplateImport.Execute error:", err)
	}
}

func (a *App) templateImportExampleXLSXHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	data, err := buildSchedulesXLSX([]scheduleExportSheet{{Name: "Example", JSON: defaultScheduleJSON()}})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка генерации XLSX"})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="template_import_example.xlsx"`)
	_, _ = w.Write(data)
}

func (a *App) templateImportUploadHandler(w http.ResponseWriter, r *http.Request) {
	wantsJSON := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") || strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	file, header, err := r.FormFile("xlsx_file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не выбран XLSX файл"})
		return
	}
	defer file.Close()

	templates, err := ParseTemplatesFromXLSX(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка разбора XLSX: " + err.Error()})
		return
	}
	if len(templates) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "В XLSX не найдено шаблонов"})
		return
	}

	results := make([]ImportedTemplateResult, 0, len(templates))
	stamp := time.Now().Format("2006-01-02 15-04")
	for i, tpl := range templates {
		name := fmt.Sprintf("%s | %s | %d", stamp, tpl.Name, i+1)
		description := fmt.Sprintf("Импортировано из %s, лист: %s", header.Filename, tpl.Name)
		if err := a.createTemplate(name, description, "grid", tpl.ScheduleJSON); err != nil {
			results = append(results, ImportedTemplateResult{Name: name, Description: description, Created: false, Error: err.Error()})
			continue
		}
		results = append(results, ImportedTemplateResult{Name: name, Description: description, Created: true})
	}

	if wantsJSON {
		created := 0
		for _, result := range results {
			if result.Created {
				created++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": fmt.Sprintf("Загружено шаблонов: %d", created), "imported": results})
		return
	}

	data := TemplateImportPageData{
		Title:        "Импорт шаблонов из XLSX",
		Imported:     results,
		ExampleSheet: templateImportExampleText(),
	}
	if err := a.tmplTemplateImport.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы импорта", http.StatusInternalServerError)
		log.Println("tmplTemplateImport.Execute error:", err)
	}
}

func templateImportExampleText() string {
	return "Скачай пример XLSX ниже. Каждый лист = отдельный шаблон. Колонки: Day | Hour | SellModeKW | SellModeBattCapacity | ChargeMode | GridExportLimit | GridChargeEnabled | LoadLimitMode | UseTimerMask | UseTimerEnabled | UseTimerMonday..UseTimerSunday | PriorityLoad. Для ChargeMode используются текущие label из реестра профилей инверторов: " + strings.Join(chargeModeValidationLabels(), ", ") + "."
}

func (a *App) taskLogsPageHandler(w http.ResponseWriter, r *http.Request) {
	settings, _ := a.getSettings()
	logs, err := a.getTaskRunLogs(200, settings.Timezone)
	if err != nil {
		http.Error(w, "Ошибка загрузки журнала задач", http.StatusInternalServerError)
		log.Println("getTaskRunLogs error:", err)
		return
	}
	data := TaskLogPageData{Title: "Журнал выполнения задач", Logs: logs}
	if err := a.tmplTaskLogs.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга журнала задач", http.StatusInternalServerError)
		log.Println("tmplTaskLogs.Execute error:", err)
	}
}

func parseBoolCell(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "1" || s == "1.0" || s == "true" || s == "yes" || s == "да" || s == "on"
}

func (a *App) appLogsPageHandler(w http.ResponseWriter, r *http.Request) {
	lines, err := a.readAppLogs(500)
	if err != nil {
		http.Error(w, "Ошибка загрузки app логов", http.StatusInternalServerError)
		log.Println("readAppLogs error:", err)
		return
	}
	data := AppLogsPageData{Title: "Мониторинг логов", Lines: lines}
	if err := a.tmplAppLogs.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы логов", http.StatusInternalServerError)
		log.Println("tmplAppLogs.Execute error:", err)
	}
}

func (a *App) apiLogsHandler(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 200)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch kind {
	case "", "app":
		lines, err := a.readAppLogs(limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения app логов"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "kind": "app", "lines": lines})
	case "task":
		settings, _ := a.getSettings()
		logs, err := a.getTaskRunLogs(limit, settings.Timezone)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения task логов"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "kind": "task", "logs": logs})
	default:
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Неизвестный kind"})
	}
}

func (a *App) rawAppLogsHandler(w http.ResponseWriter, r *http.Request) {
	lines, err := a.readAppLogs(1000)
	if err != nil {
		http.Error(w, "Ошибка чтения логов", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strings.Join(lines, "\n")))
}
