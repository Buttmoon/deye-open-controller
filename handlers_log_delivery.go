package main

import (
	"net/http"
	"strconv"
	"strings"
)

func parseBoundedPositiveInt(raw string, fallback, min, max int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v < min || v > max {
		return fallback
	}
	return v
}

func (a *App) logDeliveryPageHandler(w http.ResponseWriter, r *http.Request) {
	s, err := a.getLogDeliverySettings()
	if err != nil {
		http.Error(w, "Ошибка загрузки настроек рассылки", http.StatusInternalServerError)
		return
	}
	runs, _ := a.getLogDeliveryRuns(50)
	appSettings, settingsErr := a.getSettings()
	if settingsErr != nil {
		http.Error(w, "Ошибка загрузки имени приложения", http.StatusInternalServerError)
		return
	}
	reportLocationName, reportLocationSource := a.resolveApplicationDisplayNameWithSource(appSettings)
	data := LogDeliveryPageData{
		Title: "Рассылка XLSX логов", Settings: s, Runs: runs,
		ReportLocationName: reportLocationName, ReportLocationSource: reportLocationSource,
		SMTPPasswordStored: s.SMTPPassword != "", TelegramTokenStored: s.TelegramBotToken != "",
		StoragePasswordStored: s.StoragePassword != "", GoogleSecretStored: s.GoogleClientSecret != "",
		GoogleRefreshTokenStored: s.GoogleRefreshToken != "",
	}
	// Секреты никогда не возвращаются в HTML.
	data.Settings.SMTPPassword = ""
	data.Settings.TelegramBotToken = ""
	data.Settings.StoragePassword = ""
	data.Settings.GoogleClientSecret = ""
	data.Settings.GoogleRefreshToken = ""
	if err := a.tmplLogDelivery.Execute(w, data); err != nil {
		http.Error(w, "Ошибка рендеринга", http.StatusInternalServerError)
	}
}

func (a *App) saveLogDeliverySettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка формы"})
		return
	}
	old, err := a.getLogDeliverySettings()
	if err != nil {
		writeJSON(w, 500, jsonResponse{OK: false, Message: "Ошибка загрузки настроек"})
		return
	}
	s := old
	s.Enabled = r.FormValue("enabled") == "1"
	s.IntervalHours = parseBoundedPositiveInt(r.FormValue("interval_hours"), old.IntervalHours, 1, 8760)
	s.LookbackPeriod = strings.TrimSpace(r.FormValue("lookback_period"))
	rawCustomDays := strings.TrimSpace(r.FormValue("lookback_custom_days"))
	if s.LookbackPeriod == logPeriodCustomDays {
		customDays, parseErr := strconv.Atoi(rawCustomDays)
		if parseErr != nil || customDays < 2 || customDays > 365 {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Для периода «Несколько дней» укажите от 2 до 365 дней"})
			return
		}
		s.LookbackCustomDays = customDays
	} else if rawCustomDays != "" {
		s.LookbackCustomDays = parseBoundedPositiveInt(rawCustomDays, old.LookbackCustomDays, 2, 365)
	}
	if s.LookbackPeriod == "" {
		// Backward compatibility for clients of the previous API.
		s.LookbackHours = parseBoundedPositiveInt(r.FormValue("lookback_hours"), old.LookbackHours, 1, 8760)
	}
	normalizeLogDeliveryPeriod(&s)
	s.SMTPEnabled = r.FormValue("smtp_enabled") == "1"
	s.SMTPHost = strings.TrimSpace(r.FormValue("smtp_host"))
	if s.SMTPHost != "" {
		if hostErr := validateSMTPHost(s.SMTPHost); hostErr != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: hostErr.Error()})
			return
		}
	}
	s.SMTPPort = parseBoundedPositiveInt(r.FormValue("smtp_port"), old.SMTPPort, 1, 65535)
	s.SMTPTLSMode = strings.TrimSpace(r.FormValue("smtp_tls_mode"))
	if s.SMTPTLSMode != "starttls" && s.SMTPTLSMode != "implicit_tls" && s.SMTPTLSMode != "none" {
		s.SMTPTLSMode = "starttls"
	}
	s.SMTPUsername = strings.TrimSpace(r.FormValue("smtp_username"))
	if v := r.FormValue("smtp_password"); v != "" {
		s.SMTPPassword = v
	}
	s.SMTPFrom = strings.TrimSpace(r.FormValue("smtp_from"))
	s.SMTPRecipients = strings.TrimSpace(r.FormValue("smtp_recipients"))
	s.TelegramEnabled = r.FormValue("telegram_enabled") == "1"
	if v := r.FormValue("telegram_bot_token"); v != "" {
		s.TelegramBotToken = v
	}
	s.TelegramChatIDs = strings.TrimSpace(r.FormValue("telegram_chat_ids"))
	s.TelegramCaption = strings.TrimSpace(r.FormValue("telegram_caption"))
	s.StorageEnabled = r.FormValue("storage_enabled") == "1"
	s.StorageProvider = strings.TrimSpace(r.FormValue("storage_provider"))
	s.StorageURL = strings.TrimSpace(r.FormValue("storage_url"))
	s.StorageUsername = strings.TrimSpace(r.FormValue("storage_username"))
	if strings.EqualFold(s.StorageProvider, "yandex_disk") {
		if v := r.FormValue("yandex_oauth_token"); v != "" {
			s.StoragePassword = v
		}
		s.StorageURL = ""
		s.StorageUsername = ""
	} else if v := r.FormValue("storage_password"); v != "" {
		s.StoragePassword = v
	}
	s.StorageRemotePath = strings.TrimSpace(r.FormValue("storage_remote_path"))
	s.GoogleClientID = strings.TrimSpace(r.FormValue("google_client_id"))
	if v := r.FormValue("google_client_secret"); v != "" {
		s.GoogleClientSecret = v
	}
	if v := r.FormValue("google_refresh_token"); v != "" {
		s.GoogleRefreshToken = v
	}
	s.GoogleFolderID = strings.TrimSpace(r.FormValue("google_folder_id"))
	if s.Enabled || s.SMTPEnabled || s.TelegramEnabled || s.StorageEnabled {
		if err := validateLogDeliverySettings(s); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Настройки не прошли проверку: " + err.Error()})
			return
		}
	}
	if err := a.saveLogDeliverySettings(s); err != nil {
		writeJSON(w, 500, jsonResponse{OK: false, Message: "Ошибка сохранения: " + err.Error()})
		return
	}
	writeJSON(w, 200, jsonResponse{OK: true, Message: "Настройки рассылки сохранены"})
}

func (a *App) runLogDeliveryNowHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	filename, err := a.runLogDelivery("manual")
	if err != nil {
		writeJSON(w, 500, jsonResponse{OK: false, Message: "Ошибка доставки: " + err.Error()})
		return
	}
	writeJSON(w, 200, jsonResponse{OK: true, Message: "XLSX сформирован и доставлен: " + filename})
}

func (a *App) testSMTPDeliveryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	filename, err := a.runLogDeliveryChannel("test_smtp", "smtp")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Тестовая отправка SMTP не выполнена: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Тестовый XLSX отправлен по email: " + filename})
}

func (a *App) testTelegramAvailabilityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	s, err := a.getLogDeliverySettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка загрузки настроек Telegram"})
		return
	}
	results, err := testTelegramAvailability(s)
	message := strings.Join(results, "; ")
	if err != nil {
		if message == "" {
			message = "Telegram недоступен: " + err.Error()
		} else {
			message += "; итог: " + err.Error()
		}
		a.recordLogDeliveryHistory("error", "проверка Telegram: "+message, "", 0, false)
		writeJSON(w, http.StatusBadGateway, jsonResponse{OK: false, Message: message})
		return
	}
	a.recordLogDeliveryHistory("ok", "проверка Telegram: "+message, "", 0, false)
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: message})
}

func (a *App) testTelegramDeliveryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	filename, err := a.runLogDeliveryChannel("test_telegram", "telegram")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Тестовая отправка Telegram не выполнена: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Тестовый XLSX отправлен в Telegram: " + filename})
}

func (a *App) testStorageAvailabilityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	s, err := a.getLogDeliverySettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка загрузки настроек хранилища"})
		return
	}
	message, err := testStorageAvailability(s)
	if err != nil {
		a.recordLogDeliveryHistory("error", "проверка хранилища: "+err.Error(), "", 0, false)
		writeJSON(w, http.StatusBadGateway, jsonResponse{OK: false, Message: "Хранилище недоступно: " + err.Error()})
		return
	}
	a.recordLogDeliveryHistory("ok", "проверка хранилища: "+message, "", 0, false)
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: message})
}

func (a *App) testStorageDeliveryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	filename, err := a.runLogDeliveryChannel("test_storage", "storage")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Тестовая загрузка в хранилище не выполнена: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Тестовый XLSX загружен в хранилище: " + filename})
}
