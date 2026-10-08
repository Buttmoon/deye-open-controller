package main

import (
	"log"
	"math"
	"net/http"
	"strings"

	"inverter-schedule/internal/models"
)

func (a *App) resolveApplicationDisplayNameWithSource(settings models.Settings) (string, string) {
	if name := strings.TrimSpace(settings.ApplicationName); name != "" {
		return selectApplicationDisplayName(name, "")
	}

	var inverterName string
	if err := a.db.QueryRow(`
		SELECT COALESCE(NULLIF(TRIM(name), ''), 'Инвертор ' || id)
		FROM inverters
		ORDER BY id ASC
		LIMIT 1`).Scan(&inverterName); err == nil {
		return selectApplicationDisplayName("", inverterName)
	}

	return selectApplicationDisplayName("", "")
}

func (a *App) resolveApplicationDisplayName(settings models.Settings) string {
	name, _ := a.resolveApplicationDisplayNameWithSource(settings)
	return name
}

func (a *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	search := strings.TrimSpace(r.URL.Query().Get("search"))
	page := parsePositiveInt(r.URL.Query().Get("page"), 1)
	perPage := 10

	inverters, totalCount, err := a.getPaginatedInvertersNoCheck(search, page, perPage)
	if err != nil {
		http.Error(w, "Ошибка получения инверторов", http.StatusInternalServerError)
		log.Println("getPaginatedInverters error:", err)
		return
	}

	settings, err := a.getSettings()
	if err != nil {
		http.Error(w, "Ошибка получения настроек", http.StatusInternalServerError)
		log.Println("getSettings error:", err)
		return
	}

	totalPages := int(math.Ceil(float64(totalCount) / float64(perPage)))
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	stats, err := a.getSettingsStats(settings)
	if err != nil {
		http.Error(w, "Ошибка получения статистики", http.StatusInternalServerError)
		log.Println("getSettingsStats error:", err)
		return
	}

	applicationDisplayName := a.resolveApplicationDisplayName(settings)
	modelsCatalog, err := availableInverterModels()
	if err != nil {
		http.Error(w, "Ошибка чтения каталога моделей: "+err.Error(), http.StatusInternalServerError)
		return
	}

	data := MainPageData{
		Title:                  "Главная",
		ApplicationDisplayName: applicationDisplayName,
		Inverters:              inverters,
		InverterModels:         modelsCatalog,
		Search:                 search,
		Settings:               settings,
		TimezoneOptions:        getTimezoneOptions(),
		SettingsStats:          stats,
		TimeOverview:           a.buildMainTimeOverview(settings),
		Dashboard:              a.buildDashboardData(),
		ActiveCodes:            activeInverterLogExportColumns(),
		Page:                   page,
		PerPage:                perPage,
		TotalPages:             totalPages,
		HasPrev:                page > 1,
		HasNext:                page < totalPages,
		PrevPage:               page - 1,
		NextPage:               page + 1,
		GridPeakEnabled:        a.gridPeakFeatureEnabled(),
	}

	if err := a.tmplMain.Execute(w, data); err != nil {
		log.Println("template execute error:", err)
	}
}
