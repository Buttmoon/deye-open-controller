package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) integrationPageHandler(w http.ResponseWriter, r *http.Request) {
	settings, err := a.getSettings()
	if err != nil {
		http.Error(w, "Ошибка получения настроек", http.StatusInternalServerError)
		return
	}
	syncCfg := a.syncAuthConfig()
	data := IntegrationPageData{
		Title:    "Интеграция",
		Settings: settings,
		SyncAuth: SyncAuthPageData{
			Configured: syncCfg.configured(),
			Scheme:     syncCfg.Scheme,
			Token:      syncCfg.Token,
			Username:   syncCfg.Username,
			Password:   syncCfg.Password,
		},
	}
	if err := a.tmplIntegration.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга страницы интеграции", http.StatusInternalServerError)
		log.Println(err)
	}
}

func (a *App) saveSyncAuthSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	scheme := strings.TrimSpace(r.FormValue("sync_api_auth_scheme"))
	token := r.FormValue("sync_api_auth_token")
	username := r.FormValue("sync_api_auth_username")
	password := r.FormValue("sync_api_auth_password")
	if err := a.saveSyncAuthSettings(scheme, token, username, password); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка сохранения Sync auth: " + err.Error()})
		return
	}
	cfg := a.syncAuthConfig()
	a.recordHistory(HistoryEntry{
		OperationType: opConfigChange, Status: histApplied, Initiator: requestInitiator(r),
		Message: "Обновлены учётные данные Sync API",
		Details: map[string]any{"scheme": cfg.Scheme, "configured": cfg.configured()},
	})
	a.appendAppLog("info", "sync api auth settings updated", map[string]any{
		"component":   "sync_api",
		"scheme":      cfg.Scheme,
		"configured":  cfg.configured(),
		"has_token":   strings.TrimSpace(cfg.Token) != "",
		"has_username": strings.TrimSpace(cfg.Username) != "",
	})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Настройки Sync API сохранены"})
}

func (a *App) saveIntegrationSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения настроек"})
		return
	}
	settings.IntegrationURL = strings.TrimSpace(r.FormValue("integration_url"))
	settings.IntegrationAuthEnabled = strings.TrimSpace(r.FormValue("integration_auth_enabled")) == "1"
	settings.IntegrationAuthScheme = strings.TrimSpace(r.FormValue("integration_auth_scheme"))
	settings.IntegrationAuthToken = strings.TrimSpace(r.FormValue("integration_auth_token"))
	settings.IntegrationAuthUsername = strings.TrimSpace(r.FormValue("integration_auth_username"))
	settings.IntegrationAuthPassword = strings.TrimSpace(r.FormValue("integration_auth_password"))
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("integration_push_interval_seconds"))); err == nil && v > 0 {
		settings.IntegrationPushIntervalSeconds = v
	}
	settings.IntegrationServerName = strings.TrimSpace(r.FormValue("integration_server_name"))
	settings.IntegrationServerIP = strings.TrimSpace(r.FormValue("integration_server_ip"))
	if err := a.updateIntegrationSettings(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения настроек интеграции"})
		return
	}
	a.restartIntegrationPusher("integration settings updated")
	a.appendAppLog("info", "integration settings updated", map[string]any{
		"component":             "integration",
		"url":                   settings.IntegrationURL,
		"auth_enabled":          settings.IntegrationAuthEnabled,
		"auth_scheme":           settings.IntegrationAuthScheme,
		"push_interval_seconds": settings.IntegrationPushIntervalSeconds,
		"server_name":           settings.IntegrationServerName,
		"server_ip":             settings.IntegrationServerIP,
	})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Настройки интеграции сохранены"})
}

func (a *App) testIntegrationPushHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	go a.runIntegrationPushOnce("manual test")
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Тестовая отправка запущена"})
}

func (a *App) previewIntegrationPayloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "message": "Метод не поддерживается"})
		return
	}
	settings, err := a.getSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "message": "Ошибка чтения настроек"})
		return
	}

	latestLog, err := a.getLatestInverterLogJSON()
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "Ошибка чтения лога: " + err.Error()})
		return
	}

	payload := map[string]any{
		"server_name": settings.IntegrationServerName,
		"server_ip":   settings.IntegrationServerIP,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"log":         latestLog,
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "payload": payload})
}
