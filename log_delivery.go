package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const xlsxContentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

var (
	telegramAPIBaseURL       = "https://api.telegram.org"
	yandexDiskAPIBaseURL     = "https://cloud-api.yandex.net/v1/disk"
	googleOAuthTokenURL      = "https://oauth2.googleapis.com/token"
	googleDriveAPIBaseURL    = "https://www.googleapis.com/drive/v3"
	googleDriveUploadBaseURL = "https://www.googleapis.com/upload/drive/v3"
)

const (
	logPeriod6Hours     = "6_hours"
	logPeriod12Hours    = "12_hours"
	logPeriodDay        = "day"
	logPeriodCustomDays = "custom_days"
	logPeriodWeek       = "week"
	logPeriodMonth      = "month"
)

type LogDeliverySettings struct {
	LocationName       string
	Enabled            bool
	IntervalHours      int
	LookbackHours      int // legacy/derived value kept for API and database compatibility
	LookbackPeriod     string
	LookbackCustomDays int
	SMTPEnabled        bool
	SMTPHost           string
	SMTPPort           int
	SMTPTLSMode        string
	SMTPUsername       string
	SMTPPassword       string
	SMTPFrom           string
	SMTPRecipients     string
	SMTPSubject        string
	TelegramEnabled    bool
	TelegramBotToken   string
	TelegramChatIDs    string
	TelegramCaption    string
	StorageEnabled     bool
	StorageProvider    string
	StorageURL         string
	StorageUsername    string
	StoragePassword    string
	StorageRemotePath  string
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRefreshToken string
	GoogleFolderID     string
	LastRunUTC         string
	LastStatus         string
	LastMessage        string
}

type LogDeliveryMessageMeta struct {
	LocationName  string
	PeriodLabel   string
	From          time.Time
	To            time.Time
	GeneratedAt   time.Time
	InverterNames []string
}

type LogDeliveryRun struct {
	ID            int64
	ExecutedAtUTC string
	Status        string
	Message       string
	Filename      string
	SizeBytes     int64
}

type LogDeliveryPageData struct {
	Title                    string
	Settings                 LogDeliverySettings
	ReportLocationName       string
	ReportLocationSource     string
	Runs                     []LogDeliveryRun
	SMTPPasswordStored       bool
	TelegramTokenStored      bool
	StoragePasswordStored    bool
	GoogleSecretStored       bool
	GoogleRefreshTokenStored bool
}

type logDeliveryRuntime struct {
	mu      sync.Mutex
	running bool
}

func validateLogDeliverySettings(s LogDeliverySettings) error {
	if s.IntervalHours < 1 || s.IntervalHours > 8760 {
		return fmt.Errorf("interval_hours должен быть 1–8760")
	}
	if err := validateLogDeliveryPeriod(s); err != nil {
		return err
	}
	if !s.SMTPEnabled && !s.TelegramEnabled && !s.StorageEnabled {
		return fmt.Errorf("не включён ни один канал доставки")
	}
	if s.SMTPEnabled {
		if s.SMTPHost == "" || s.SMTPPort < 1 || s.SMTPPort > 65535 || s.SMTPFrom == "" || len(splitAddressList(s.SMTPRecipients)) == 0 {
			return fmt.Errorf("для SMTP заполните host, port, from и recipients")
		}
		if err := validateSMTPHost(s.SMTPHost); err != nil {
			return err
		}
		if s.SMTPTLSMode != "starttls" && s.SMTPTLSMode != "implicit_tls" && s.SMTPTLSMode != "none" {
			return fmt.Errorf("неподдерживаемый SMTP TLS mode=%s", s.SMTPTLSMode)
		}
		if strings.ContainsAny(s.SMTPFrom+s.SMTPSubject, "\r\n") {
			return fmt.Errorf("SMTP headers содержат недопустимый перевод строки")
		}
		if _, err := mail.ParseAddress(s.SMTPFrom); err != nil {
			return fmt.Errorf("некорректный SMTP From: %w", err)
		}
		for _, recipient := range splitAddressList(s.SMTPRecipients) {
			if _, err := mail.ParseAddress(recipient); err != nil {
				return fmt.Errorf("некорректный SMTP recipient %q: %w", recipient, err)
			}
		}
	}
	if s.TelegramEnabled {
		if strings.TrimSpace(s.TelegramBotToken) == "" || len(splitAddressList(s.TelegramChatIDs)) == 0 {
			return fmt.Errorf("для Telegram заполните bot token и chat ID")
		}
	}
	if s.StorageEnabled {
		switch strings.ToLower(strings.TrimSpace(s.StorageProvider)) {
		case "google_drive":
			if s.GoogleClientID == "" || s.GoogleClientSecret == "" || s.GoogleRefreshToken == "" {
				return fmt.Errorf("для Google Drive заполните client ID, client secret и refresh token")
			}
		case "yandex_disk":
			if strings.TrimSpace(s.StoragePassword) == "" {
				return fmt.Errorf("для Yandex Disk заполните OAuth-токен")
			}
		case "webdav", "yandex_webdav", "owncloud", "nextcloud", "":
			parsed, err := url.Parse(strings.TrimSpace(s.StorageURL))
			if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
				return fmt.Errorf("для WebDAV укажите корректный HTTP(S) URL")
			}
		default:
			return fmt.Errorf("неизвестный storage_provider=%s", s.StorageProvider)
		}
	}
	return nil
}

func validateLogDeliveryChannel(s LogDeliverySettings, channel string) error {
	s.Enabled = false
	s.SMTPEnabled = false
	s.TelegramEnabled = false
	s.StorageEnabled = false
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "smtp":
		s.SMTPEnabled = true
	case "telegram":
		s.TelegramEnabled = true
	case "storage":
		s.StorageEnabled = true
	default:
		return fmt.Errorf("неизвестный канал доставки %q", channel)
	}
	return validateLogDeliverySettings(s)
}

func validateLogDeliveryPeriod(s LogDeliverySettings) error {
	switch strings.TrimSpace(s.LookbackPeriod) {
	case logPeriod6Hours, logPeriod12Hours, logPeriodDay, logPeriodWeek, logPeriodMonth:
		return nil
	case logPeriodCustomDays:
		if s.LookbackCustomDays < 2 || s.LookbackCustomDays > 365 {
			return fmt.Errorf("lookback_custom_days должен быть 2–365")
		}
		return nil
	default:
		return fmt.Errorf("неподдерживаемый период данных %q", s.LookbackPeriod)
	}
}

func normalizeLogDeliveryPeriod(s *LogDeliverySettings) {
	if s == nil {
		return
	}
	if s.LookbackCustomDays < 2 || s.LookbackCustomDays > 365 {
		s.LookbackCustomDays = 2
	}
	switch strings.TrimSpace(s.LookbackPeriod) {
	case logPeriod6Hours, logPeriod12Hours, logPeriodDay, logPeriodCustomDays, logPeriodWeek, logPeriodMonth:
	case "":
		switch s.LookbackHours {
		case 6:
			s.LookbackPeriod = logPeriod6Hours
		case 12:
			s.LookbackPeriod = logPeriod12Hours
		case 168:
			s.LookbackPeriod = logPeriodWeek
		case 720:
			s.LookbackPeriod = logPeriodMonth
		default:
			if s.LookbackHours > 24 && s.LookbackHours%24 == 0 {
				days := s.LookbackHours / 24
				if days >= 2 && days <= 365 {
					s.LookbackPeriod = logPeriodCustomDays
					s.LookbackCustomDays = days
					break
				}
			}
			s.LookbackPeriod = logPeriodDay
		}
	default:
		// Keep an unknown explicit value intact so validation rejects it instead
		// of silently falling back to a different period.
	}
	s.LookbackHours = nominalLogDeliveryLookbackHours(*s)
}

func nominalLogDeliveryLookbackHours(s LogDeliverySettings) int {
	switch strings.TrimSpace(s.LookbackPeriod) {
	case logPeriod6Hours:
		return 6
	case logPeriod12Hours:
		return 12
	case logPeriodWeek:
		return 7 * 24
	case logPeriodMonth:
		// Compatibility value only. The real range is a calendar month and is
		// calculated by logDeliveryTimeRange.
		return 30 * 24
	case logPeriodCustomDays:
		days := s.LookbackCustomDays
		if days < 2 || days > 365 {
			days = 2
		}
		return days * 24
	default:
		return 24
	}
}

func subtractCalendarMonthClamped(t time.Time) time.Time {
	year, month, day := t.Date()
	month--
	if month < time.January {
		month = time.December
		year--
	}
	lastDay := time.Date(year, month+1, 0, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

func logDeliveryTimeRange(s LogDeliverySettings, to time.Time) (time.Time, time.Time, error) {
	if err := validateLogDeliveryPeriod(s); err != nil {
		return time.Time{}, time.Time{}, err
	}
	var from time.Time
	switch strings.TrimSpace(s.LookbackPeriod) {
	case logPeriod6Hours:
		from = to.Add(-6 * time.Hour)
	case logPeriod12Hours:
		from = to.Add(-12 * time.Hour)
	case logPeriodDay:
		from = to.Add(-24 * time.Hour)
	case logPeriodCustomDays:
		from = to.AddDate(0, 0, -s.LookbackCustomDays)
	case logPeriodWeek:
		from = to.AddDate(0, 0, -7)
	case logPeriodMonth:
		from = subtractCalendarMonthClamped(to)
	}
	return from, to, nil
}

func logDeliveryPeriodLabel(s LogDeliverySettings) string {
	switch strings.TrimSpace(s.LookbackPeriod) {
	case logPeriod6Hours:
		return "последние 6 часов"
	case logPeriod12Hours:
		return "последние 12 часов"
	case logPeriodDay:
		return "последние 24 часа"
	case logPeriodCustomDays:
		return fmt.Sprintf("последние %d дн.", s.LookbackCustomDays)
	case logPeriodWeek:
		return "последние 7 дней"
	case logPeriodMonth:
		return "последний месяц (календарно)"
	default:
		return "неизвестный период"
	}
}

func (a *App) getLogDeliverySettings() (LogDeliverySettings, error) {
	var s LogDeliverySettings
	var enabled, smtpEnabled, telegramEnabled, storageEnabled int
	err := a.db.QueryRow(`
		SELECT location_name, enabled, interval_hours, lookback_hours, lookback_period, lookback_custom_days,
		smtp_enabled, smtp_host, smtp_port, smtp_tls_mode, smtp_username, smtp_password, smtp_from, smtp_recipients, smtp_subject,
		telegram_enabled, telegram_bot_token, telegram_chat_ids, telegram_caption,
		storage_enabled, storage_provider, storage_url, storage_username, storage_password, storage_remote_path,
		google_client_id, google_client_secret, google_refresh_token, google_folder_id,
		last_run_utc, last_status, last_message
		FROM log_delivery_settings WHERE id=1
	`).Scan(&s.LocationName, &enabled, &s.IntervalHours, &s.LookbackHours, &s.LookbackPeriod, &s.LookbackCustomDays,
		&smtpEnabled, &s.SMTPHost, &s.SMTPPort, &s.SMTPTLSMode, &s.SMTPUsername, &s.SMTPPassword, &s.SMTPFrom, &s.SMTPRecipients, &s.SMTPSubject,
		&telegramEnabled, &s.TelegramBotToken, &s.TelegramChatIDs, &s.TelegramCaption,
		&storageEnabled, &s.StorageProvider, &s.StorageURL, &s.StorageUsername, &s.StoragePassword, &s.StorageRemotePath,
		&s.GoogleClientID, &s.GoogleClientSecret, &s.GoogleRefreshToken, &s.GoogleFolderID,
		&s.LastRunUTC, &s.LastStatus, &s.LastMessage)
	s.Enabled = enabled == 1
	s.SMTPEnabled = smtpEnabled == 1
	s.TelegramEnabled = telegramEnabled == 1
	s.StorageEnabled = storageEnabled == 1
	if s.IntervalHours < 1 {
		s.IntervalHours = 24
	}
	if s.LookbackHours < 1 {
		s.LookbackHours = 24
	}
	normalizeLogDeliveryPeriod(&s)
	if s.SMTPPort <= 0 {
		s.SMTPPort = 587
	}
	return s, err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (a *App) saveLogDeliverySettings(s LogDeliverySettings) error {
	_, err := a.db.Exec(`
		UPDATE log_delivery_settings SET
		location_name=?, enabled=?, interval_hours=?, lookback_hours=?, lookback_period=?, lookback_custom_days=?,
		smtp_enabled=?, smtp_host=?, smtp_port=?, smtp_tls_mode=?, smtp_username=?, smtp_password=?, smtp_from=?, smtp_recipients=?, smtp_subject=?,
		telegram_enabled=?, telegram_bot_token=?, telegram_chat_ids=?, telegram_caption=?,
		storage_enabled=?, storage_provider=?, storage_url=?, storage_username=?, storage_password=?, storage_remote_path=?,
		google_client_id=?, google_client_secret=?, google_refresh_token=?, google_folder_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=1
	`, s.LocationName, boolInt(s.Enabled), s.IntervalHours, nominalLogDeliveryLookbackHours(s), s.LookbackPeriod, s.LookbackCustomDays,
		boolInt(s.SMTPEnabled), s.SMTPHost, s.SMTPPort, s.SMTPTLSMode, s.SMTPUsername, s.SMTPPassword, s.SMTPFrom, s.SMTPRecipients, s.SMTPSubject,
		boolInt(s.TelegramEnabled), s.TelegramBotToken, s.TelegramChatIDs, s.TelegramCaption,
		boolInt(s.StorageEnabled), s.StorageProvider, s.StorageURL, s.StorageUsername, s.StoragePassword, s.StorageRemotePath,
		s.GoogleClientID, s.GoogleClientSecret, s.GoogleRefreshToken, s.GoogleFolderID)
	return err
}

func (a *App) getLogDeliveryRuns(limit int) ([]LogDeliveryRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.db.Query(`SELECT id, executed_at_utc, status, message, filename, size_bytes FROM log_delivery_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogDeliveryRun
	for rows.Next() {
		var r LogDeliveryRun
		if err := rows.Scan(&r.ID, &r.ExecutedAtUTC, &r.Status, &r.Message, &r.Filename, &r.SizeBytes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (a *App) recordLogDeliveryHistory(status, message, filename string, size int64, updateLastRun bool) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = a.db.Exec(`INSERT INTO log_delivery_runs (executed_at_utc,status,message,filename,size_bytes) VALUES (?,?,?,?,?)`, now, status, message, filename, size)
	if updateLastRun {
		_, _ = a.db.Exec(`UPDATE log_delivery_settings SET last_run_utc=?, last_status=?, last_message=? WHERE id=1`, now, status, message)
	}
	a.appendAppLog(map[bool]string{true: "info", false: "error"}[status == "ok"], "xlsx log delivery completed", map[string]any{"component": "log_delivery", "status": status, "message": message, "filename": filename, "size_bytes": size, "updates_schedule_state": updateLastRun})
}

func (a *App) recordLogDeliveryRun(status, message, filename string, size int64) {
	a.recordLogDeliveryHistory(status, message, filename, size, true)
}

func (a *App) startLogDelivery() {
	a.logDeliveryCtlMu.Lock()
	defer a.logDeliveryCtlMu.Unlock()
	if a.logDeliveryStopCh != nil {
		return
	}
	stop := make(chan struct{})
	a.logDeliveryStopCh = stop
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s, err := a.getLogDeliverySettings()
				if err != nil || !s.Enabled {
					continue
				}
				last, _ := time.Parse(time.RFC3339, strings.TrimSpace(s.LastRunUTC))
				if last.IsZero() || time.Since(last) >= time.Duration(s.IntervalHours)*time.Hour {
					go a.runLogDelivery("periodic")
				}
			case <-stop:
				return
			}
		}
	}()
}

func (a *App) stopLogDelivery() {
	a.logDeliveryCtlMu.Lock()
	defer a.logDeliveryCtlMu.Unlock()
	if a.logDeliveryStopCh != nil {
		close(a.logDeliveryStopCh)
		a.logDeliveryStopCh = nil
	}
}

func buildLogDeliverySubject(location string, at time.Time) string {
	location = strings.TrimSpace(location)
	return fmt.Sprintf("Deye Log — %s — %s", location, at.Format("02.01.2006 15:04"))
}

func buildSMTPTextBody(location, periodLabel string, from, to time.Time, inverterNames []string) string {
	location = strings.TrimSpace(location)
	lines := []string{
		"Автоматическая выгрузка логов инверторов Deye.",
		"",
		"Локация: " + location,
		"Период: " + strings.TrimSpace(periodLabel),
		"Диапазон: " + from.Format("02.01.2006 15:04") + " — " + to.Format("02.01.2006 15:04"),
	}
	if len(inverterNames) > 0 {
		lines = append(lines, "Инверторы: "+strings.Join(inverterNames, ", "))
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

func uniqueInverterNames(items []inverterLogLine) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, item := range items {
		name := strings.TrimSpace(fmt.Sprint(item.Record["inverter_name"]))
		if name == "" || name == "<nil>" {
			name = strings.TrimSpace(fmt.Sprint(item.Record["inverter_ip"]))
		}
		if name == "" || name == "<nil>" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func (a *App) logDeliveryTimezone() *time.Location {
	settings, err := a.getSettings()
	if err == nil && strings.TrimSpace(settings.Timezone) != "" {
		if loc, loadErr := time.LoadLocation(settings.Timezone); loadErr == nil {
			return loc
		}
	}
	return time.Local
}

func (a *App) prepareLogDeliveryArtifact(s LogDeliverySettings) ([]byte, string, LogDeliveryMessageMeta, error) {
	loc := a.logDeliveryTimezone()
	to := time.Now().In(loc)
	from, to, err := logDeliveryTimeRange(s, to)
	if err != nil {
		return nil, "", LogDeliveryMessageMeta{}, err
	}
	data, filename, inverterNames, err := buildInverterLogXLSX(from, to)
	if err != nil {
		return nil, "", LogDeliveryMessageMeta{}, err
	}
	appSettings, settingsErr := a.getSettings()
	if settingsErr != nil {
		return nil, "", LogDeliveryMessageMeta{}, fmt.Errorf("не удалось определить локацию отчёта: %w", settingsErr)
	}
	locationName, _ := a.resolveApplicationDisplayNameWithSource(appSettings)
	meta := LogDeliveryMessageMeta{
		LocationName:  locationName,
		PeriodLabel:   logDeliveryPeriodLabel(s),
		From:          from,
		To:            to,
		GeneratedAt:   to,
		InverterNames: inverterNames,
	}
	return data, filename, meta, nil
}

func deliverLogDeliveryChannel(s LogDeliverySettings, channel, filename string, data []byte, meta LogDeliveryMessageMeta) error {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "smtp":
		return sendXLSXSMTP(s, filename, data, meta)
	case "telegram":
		return sendXLSXTelegram(s, filename, data)
	case "storage":
		return uploadXLSXStorage(s, filename, data)
	default:
		return fmt.Errorf("неизвестный канал доставки %q", channel)
	}
}

func (a *App) runLogDeliveryChannel(source, channel string) (string, error) {
	a.logDeliveryRuntime.mu.Lock()
	if a.logDeliveryRuntime.running {
		a.logDeliveryRuntime.mu.Unlock()
		return "", fmt.Errorf("доставка XLSX уже выполняется")
	}
	a.logDeliveryRuntime.running = true
	a.logDeliveryRuntime.mu.Unlock()
	defer func() {
		a.logDeliveryRuntime.mu.Lock()
		a.logDeliveryRuntime.running = false
		a.logDeliveryRuntime.mu.Unlock()
	}()

	s, err := a.getLogDeliverySettings()
	if err != nil {
		return "", err
	}
	if err := validateLogDeliveryChannel(s, channel); err != nil {
		a.recordLogDeliveryHistory("error", fmt.Sprintf("источник=%s; канал=%s; %s", source, channel, err.Error()), "", 0, false)
		return "", err
	}
	data, filename, meta, err := a.prepareLogDeliveryArtifact(s)
	if err != nil {
		a.recordLogDeliveryHistory("error", fmt.Sprintf("источник=%s; канал=%s; %s", source, channel, err.Error()), "", 0, false)
		return "", err
	}
	if err := deliverLogDeliveryChannel(s, channel, filename, data, meta); err != nil {
		message := fmt.Sprintf("источник=%s; канал=%s; ошибка: %s", source, channel, err.Error())
		a.recordLogDeliveryHistory("error", message, filename, int64(len(data)), false)
		return filename, err
	}
	message := fmt.Sprintf("источник=%s; канал=%s; тестовая доставка успешна; период=%s (%s — %s)", source, channel, meta.PeriodLabel, meta.From.Format(time.RFC3339), meta.To.Format(time.RFC3339))
	a.recordLogDeliveryHistory("ok", message, filename, int64(len(data)), false)
	return filename, nil
}

func (a *App) runLogDelivery(source string) (string, error) {
	a.logDeliveryRuntime.mu.Lock()
	if a.logDeliveryRuntime.running {
		a.logDeliveryRuntime.mu.Unlock()
		return "", fmt.Errorf("доставка XLSX уже выполняется")
	}
	a.logDeliveryRuntime.running = true
	a.logDeliveryRuntime.mu.Unlock()
	defer func() {
		a.logDeliveryRuntime.mu.Lock()
		a.logDeliveryRuntime.running = false
		a.logDeliveryRuntime.mu.Unlock()
	}()
	s, err := a.getLogDeliverySettings()
	if err != nil {
		return "", err
	}
	if err := validateLogDeliverySettings(s); err != nil {
		a.recordLogDeliveryRun("error", err.Error(), "", 0)
		return "", err
	}
	data, filename, meta, err := a.prepareLogDeliveryArtifact(s)
	if err != nil {
		a.recordLogDeliveryRun("error", err.Error(), "", 0)
		return "", err
	}
	var results []string
	var failures []string
	if s.SMTPEnabled {
		if err := sendXLSXSMTP(s, filename, data, meta); err != nil {
			failures = append(failures, "SMTP: "+err.Error())
		} else {
			results = append(results, "SMTP")
		}
	}
	if s.TelegramEnabled {
		if err := sendXLSXTelegram(s, filename, data); err != nil {
			failures = append(failures, "Telegram: "+err.Error())
		} else {
			results = append(results, "Telegram")
		}
	}
	if s.StorageEnabled {
		if err := uploadXLSXStorage(s, filename, data); err != nil {
			failures = append(failures, "Хранилище: "+err.Error())
		} else {
			results = append(results, "Хранилище")
		}
	}
	message := fmt.Sprintf("источник=%s; период=%s (%s — %s); успешно: %s", source, meta.PeriodLabel, meta.From.Format(time.RFC3339), meta.To.Format(time.RFC3339), strings.Join(results, ", "))
	status := "ok"
	if len(failures) > 0 {
		status = "error"
		message += "; ошибки: " + strings.Join(failures, "; ")
	}
	a.recordLogDeliveryRun(status, message, filename, int64(len(data)))
	if len(failures) > 0 {
		return filename, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return filename, nil
}

func buildInverterLogXLSX(from, to time.Time) ([]byte, string, []string, error) {
	items, headers, err := collectInverterLogRecords(from, to, nil, "")
	if err != nil {
		return nil, "", nil, err
	}
	if len(items) == 0 {
		return nil, "", nil, fmt.Errorf("за выбранный период записи логов не найдены")
	}
	xlsxHeaders, rows := buildLogExportXLSXRows(items, headers)
	data, err := buildGenericXLSX("InverterLog", xlsxHeaders, rows)
	if err != nil {
		return nil, "", nil, err
	}
	filename := fmt.Sprintf("inverter-log-%s--%s.xlsx", from.Format("20060102-150405"), to.Format("20060102-150405"))
	return data, filename, uniqueInverterNames(items), nil
}

func validateSMTPHost(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("SMTP host не указан")
	}
	if strings.ContainsAny(raw, "\r\n\x00") {
		return fmt.Errorf("SMTP host содержит недопустимые управляющие символы")
	}
	return nil
}

func splitAddressList(raw string) []string {
	raw = strings.NewReplacer(";", ",", "\n", ",", "\r", ",").Replace(raw)
	parts := strings.Split(raw, ",")
	var out []string
	seen := map[string]bool{}
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func sendXLSXSMTP(s LogDeliverySettings, filename string, data []byte, meta LogDeliveryMessageMeta) error {
	recipients := splitAddressList(s.SMTPRecipients)
	if s.SMTPHost == "" || s.SMTPPort <= 0 || s.SMTPFrom == "" || len(recipients) == 0 {
		return fmt.Errorf("не заполнены SMTP host/port/from/recipients")
	}
	if err := validateSMTPHost(s.SMTPHost); err != nil {
		return err
	}
	subject := buildLogDeliverySubject(meta.LocationName, meta.GeneratedAt)
	if strings.ContainsAny(s.SMTPFrom+subject+filename, "\r\n") {
		return fmt.Errorf("SMTP headers содержат недопустимый перевод строки")
	}
	fromAddress, err := mail.ParseAddress(s.SMTPFrom)
	if err != nil {
		return fmt.Errorf("некорректный SMTP From: %w", err)
	}
	parsedRecipients := make([]*mail.Address, 0, len(recipients))
	for _, recipient := range recipients {
		address, parseErr := mail.ParseAddress(recipient)
		if parseErr != nil {
			return fmt.Errorf("некорректный получатель %q: %w", recipient, parseErr)
		}
		parsedRecipients = append(parsedRecipients, address)
	}
	boundary := "inverter-xlsx-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	encodedSubject := mime.QEncoding.Encode("UTF-8", subject)
	toHeaders := make([]string, 0, len(parsedRecipients))
	for _, recipient := range parsedRecipients {
		toHeaders = append(toHeaders, recipient.String())
	}
	var body bytes.Buffer
	fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=%q\r\n\r\n", fromAddress.String(), strings.Join(toHeaders, ", "), encodedSubject, boundary)
	fmt.Fprintf(&body, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, buildSMTPTextBody(meta.LocationName, meta.PeriodLabel, meta.From, meta.To, meta.InverterNames))
	fmt.Fprintf(&body, "--%s\r\nContent-Type: %s; name=%q\r\nContent-Disposition: attachment; filename=%q\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary, xlsxContentType, filename, filename)
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		body.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	body.WriteString(encoded + "\r\n--" + boundary + "--\r\n")

	addr := net.JoinHostPort(s.SMTPHost, strconv.Itoa(s.SMTPPort))
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var client *smtp.Client
	if s.SMTPTLSMode == "implicit_tls" {
		conn, dialErr := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.SMTPHost, MinVersion: tls.VersionTLS12})
		if dialErr != nil {
			return fmt.Errorf("SMTP TLS connection failed: %w", dialErr)
		}
		client, err = smtp.NewClient(conn, s.SMTPHost)
	} else {
		conn, dialErr := dialer.Dial("tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("SMTP connection failed: %w", dialErr)
		}
		client, err = smtp.NewClient(conn, s.SMTPHost)
	}
	if err != nil {
		return fmt.Errorf("SMTP connection failed: %w", err)
	}
	defer client.Close()
	if s.SMTPTLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP-сервер не поддерживает STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("SMTP STARTTLS failed: %w", err)
		}
	}
	if s.SMTPUsername != "" {
		if err := client.Auth(smtp.PlainAuth("", s.SMTPUsername, s.SMTPPassword, s.SMTPHost)); err != nil {
			return fmt.Errorf("SMTP authentication failed: %w", err)
		}
	}
	if err := client.Mail(fromAddress.Address); err != nil {
		return fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}
	for _, recipient := range parsedRecipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return fmt.Errorf("SMTP recipient %s rejected: %w", recipient.Address, err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}
	if _, err = wc.Write(body.Bytes()); err != nil {
		_ = wc.Close()
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err = wc.Close(); err != nil {
		return fmt.Errorf("SMTP message close failed: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP QUIT failed: %w", err)
	}
	return nil
}

func sendXLSXTelegram(s LogDeliverySettings, filename string, data []byte) error {
	chats := splitAddressList(s.TelegramChatIDs)
	if s.TelegramBotToken == "" || len(chats) == 0 {
		return fmt.Errorf("не заполнены bot token/chat_id")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	endpoint := strings.TrimRight(telegramAPIBaseURL, "/") + "/bot" + s.TelegramBotToken + "/sendDocument"
	for _, chatID := range chats {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if err := mw.WriteField("chat_id", chatID); err != nil {
			return err
		}
		if s.TelegramCaption != "" {
			if err := mw.WriteField("caption", s.TelegramCaption); err != nil {
				return err
			}
		}
		part, err := mw.CreateFormFile("document", filename)
		if err != nil {
			return err
		}
		if _, err = part.Write(data); err != nil {
			return err
		}
		if err := mw.Close(); err != nil {
			return err
		}
		req, err := http.NewRequest(http.MethodPost, endpoint, &body)
		if err != nil {
			return fmt.Errorf("не удалось создать Telegram-запрос")
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := client.Do(req)
		if err != nil {
			// Не возвращаем исходную ошибку URL, потому что URL содержит bot token.
			return fmt.Errorf("Telegram request failed for chat %s", chatID)
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("chat %s: Telegram HTTP %d: %s", chatID, resp.StatusCode, strings.TrimSpace(string(respBody)))
		}
	}
	return nil
}

func uploadXLSXStorage(s LogDeliverySettings, filename string, data []byte) error {
	switch strings.ToLower(strings.TrimSpace(s.StorageProvider)) {
	case "google_drive":
		return uploadGoogleDrive(s, filename, data)
	case "yandex_disk":
		return uploadYandexDisk(s, filename, data)
	case "webdav", "yandex_webdav", "owncloud", "nextcloud", "":
		return uploadWebDAV(s, filename, data)
	default:
		return fmt.Errorf("неизвестный storage_provider=%s", s.StorageProvider)
	}
}

func uploadWebDAV(s LogDeliverySettings, filename string, data []byte) error {
	base, err := url.Parse(strings.TrimSpace(s.StorageURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("некорректный WebDAV URL")
	}
	base.Path = path.Join(base.Path, strings.Trim(s.StorageRemotePath, "/"), filename)
	req, err := http.NewRequest(http.MethodPut, base.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", xlsxContentType)
	if s.StorageUsername != "" {
		req.SetBasicAuth(s.StorageUsername, s.StoragePassword)
	} else if s.StoragePassword != "" {
		req.Header.Set("Authorization", "OAuth "+s.StoragePassword)
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WebDAV HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return nil
}

func normalizeYandexDiskPath(remotePath, filename string) string {
	remotePath = strings.TrimSpace(strings.ReplaceAll(remotePath, "\\", "/"))
	if remotePath == "" {
		remotePath = "app:/inverter-logs"
	}
	if !strings.HasPrefix(remotePath, "app:/") && !strings.HasPrefix(remotePath, "disk:/") {
		remotePath = "app:/" + strings.TrimLeft(remotePath, "/")
	}
	return strings.TrimRight(remotePath, "/") + "/" + strings.TrimLeft(filename, "/")
}

func createYandexDiskFolder(client *http.Client, token, folder string) error {
	folder = strings.TrimRight(strings.TrimSpace(folder), "/")
	if folder == "" || folder == "app:" || folder == "disk:" {
		return nil
	}
	parts := strings.SplitN(folder, ":/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("некорректный путь Yandex Disk: %s", folder)
	}
	prefix := parts[0] + ":/"
	current := strings.Trim(parts[1], "/")
	if current == "" {
		return nil
	}
	segments := strings.Split(current, "/")
	built := strings.TrimRight(prefix, "/")
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		built += "/" + segment
		endpoint := strings.TrimRight(yandexDiskAPIBaseURL, "/") + "/resources?" + url.Values{"path": {built}}.Encode()
		req, err := http.NewRequest(http.MethodPut, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "OAuth "+token)
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("Yandex Disk folder request failed: %w", err)
		}
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		// 201 = created, 409 = already exists / parent conflict. Existing folders
		// are harmless; the subsequent upload call remains the final validation.
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			return fmt.Errorf("Yandex Disk folder HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
		}
	}
	return nil
}

func uploadYandexDisk(s LogDeliverySettings, filename string, data []byte) error {
	token := strings.TrimSpace(s.StoragePassword)
	if token == "" {
		return fmt.Errorf("не заполнен OAuth-токен Yandex Disk")
	}
	target := normalizeYandexDiskPath(s.StorageRemotePath, filename)
	folder := target
	if slash := strings.LastIndex(folder, "/"); slash >= 0 {
		folder = folder[:slash]
	}
	client := &http.Client{Timeout: 90 * time.Second}
	if err := createYandexDiskFolder(client, token, folder); err != nil {
		return err
	}
	endpoint := strings.TrimRight(yandexDiskAPIBaseURL, "/") + "/resources/upload?" + url.Values{
		"path":      {target},
		"overwrite": {"true"},
	}.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Yandex Disk upload-link request failed: %w", err)
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Yandex Disk upload-link HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var link struct {
		Href   string `json:"href"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(payload, &link); err != nil || strings.TrimSpace(link.Href) == "" {
		return fmt.Errorf("Yandex Disk не вернул URL загрузки")
	}
	method := strings.ToUpper(strings.TrimSpace(link.Method))
	if method == "" {
		method = http.MethodPut
	}
	uploadReq, err := http.NewRequest(method, link.Href, bytes.NewReader(data))
	if err != nil {
		return err
	}
	uploadReq.Header.Set("Content-Type", xlsxContentType)
	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		return fmt.Errorf("Yandex Disk upload request failed: %w", err)
	}
	defer uploadResp.Body.Close()
	uploadBody, _ := io.ReadAll(io.LimitReader(uploadResp.Body, 1<<20))
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return fmt.Errorf("Yandex Disk upload HTTP %d: %s", uploadResp.StatusCode, strings.TrimSpace(string(uploadBody)))
	}
	return nil
}

func testTelegramAvailability(s LogDeliverySettings) ([]string, error) {
	token := strings.TrimSpace(s.TelegramBotToken)
	chats := splitAddressList(s.TelegramChatIDs)
	if token == "" || len(chats) == 0 {
		return nil, fmt.Errorf("для Telegram заполните bot token и chat ID")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(telegramAPIBaseURL, "/") + "/bot" + token

	var meResp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := telegramGETJSON(client, base+"/getMe", nil, &meResp); err != nil {
		return nil, err
	}
	if !meResp.OK {
		return nil, fmt.Errorf("Telegram getMe: %s", strings.TrimSpace(meResp.Description))
	}
	botLabel := strings.TrimSpace(meResp.Result.Username)
	if botLabel == "" {
		botLabel = strconv.FormatInt(meResp.Result.ID, 10)
	} else {
		botLabel = "@" + botLabel
	}
	results := []string{"Бот доступен: " + botLabel}

	hadChatError := false
	for _, chatID := range chats {
		var chatResp struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
			Result      struct {
				ID       int64  `json:"id"`
				Title    string `json:"title"`
				Username string `json:"username"`
				Type     string `json:"type"`
			} `json:"result"`
		}
		if err := telegramGETJSON(client, base+"/getChat", url.Values{"chat_id": {chatID}}, &chatResp); err != nil {
			hadChatError = true
			results = append(results, fmt.Sprintf("Чат %s: %s", chatID, telegramAvailabilityErrorLabel(err)))
			continue
		}
		if !chatResp.OK {
			hadChatError = true
			results = append(results, fmt.Sprintf("Чат %s: %s", chatID, telegramAvailabilityErrorLabel(errors.New(chatResp.Description))))
			continue
		}
		label := strings.TrimSpace(chatResp.Result.Title)
		if label == "" && strings.TrimSpace(chatResp.Result.Username) != "" {
			label = "@" + strings.TrimSpace(chatResp.Result.Username)
		}
		if label == "" {
			label = chatID
		}
		results = append(results, fmt.Sprintf("Чат %s доступен: %s", chatID, label))
	}
	if hadChatError {
		return results, fmt.Errorf("один или несколько Telegram-чатов недоступны")
	}
	return results, nil
}

func telegramAvailabilityErrorLabel(err error) string {
	if err == nil {
		return "нет доступа"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "chat not found"):
		return "чат не найден"
	case strings.Contains(message, "bot was blocked") || strings.Contains(message, "bot is blocked"):
		return "бот заблокирован"
	case strings.Contains(message, "forbidden"):
		return "нет доступа (Forbidden)"
	default:
		return "нет доступа: " + err.Error()
	}
}

func telegramGETJSON(client *http.Client, endpoint string, values url.Values, dst any) error {
	if values != nil && len(values) > 0 {
		endpoint += "?" + values.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("не удалось создать Telegram-запрос")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram недоступен")
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Description string `json:"description"`
		}
		_ = json.Unmarshal(payload, &apiErr)
		if strings.TrimSpace(apiErr.Description) != "" {
			return fmt.Errorf("Telegram HTTP %d: %s", resp.StatusCode, strings.TrimSpace(apiErr.Description))
		}
		return fmt.Errorf("Telegram HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		return fmt.Errorf("Telegram вернул некорректный JSON")
	}
	return nil
}

func testStorageAvailability(s LogDeliverySettings) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s.StorageProvider)) {
	case "google_drive":
		return testGoogleDriveAvailability(s)
	case "yandex_disk":
		return testYandexDiskAvailability(s)
	case "webdav", "yandex_webdav", "owncloud", "nextcloud", "":
		return testWebDAVAvailability(s)
	default:
		return "", fmt.Errorf("неизвестный storage_provider=%s", s.StorageProvider)
	}
}

func testWebDAVAvailability(s LogDeliverySettings) (string, error) {
	base, err := url.Parse(strings.TrimSpace(s.StorageURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("некорректный WebDAV URL")
	}
	base.Path = path.Join(base.Path, strings.Trim(s.StorageRemotePath, "/"))
	body := strings.NewReader(`<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`)
	req, err := http.NewRequest("PROPFIND", base.String(), body)
	if err != nil {
		return "", fmt.Errorf("не удалось создать WebDAV-запрос")
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	if s.StorageUsername != "" {
		req.SetBasicAuth(s.StorageUsername, s.StoragePassword)
	} else if s.StoragePassword != "" {
		req.Header.Set("Authorization", "OAuth "+s.StoragePassword)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("WebDAV недоступен")
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("WebDAV HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return fmt.Sprintf("WebDAV доступен, каталог отвечает: %s", base.Path), nil
}

func yandexDiskFolderPath(remotePath string) string {
	remotePath = strings.TrimSpace(strings.ReplaceAll(remotePath, "\\", "/"))
	if remotePath == "" {
		return "app:/inverter-logs"
	}
	if !strings.HasPrefix(remotePath, "app:/") && !strings.HasPrefix(remotePath, "disk:/") {
		return "app:/" + strings.TrimLeft(remotePath, "/")
	}
	return strings.TrimRight(remotePath, "/")
}

func testYandexDiskAvailability(s LogDeliverySettings) (string, error) {
	token := strings.TrimSpace(s.StoragePassword)
	if token == "" {
		return "", fmt.Errorf("не заполнен OAuth-токен Yandex Disk")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(yandexDiskAPIBaseURL, "/")
	if err := yandexDiskGET(client, token, base, nil, nil); err != nil {
		return "", err
	}
	folder := yandexDiskFolderPath(s.StorageRemotePath)
	var folderInfo struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	err := yandexDiskGET(client, token, base+"/resources", url.Values{"path": {folder}}, &folderInfo)
	if err != nil {
		var notFound *storagePathNotFoundError
		if errors.As(err, &notFound) {
			return fmt.Sprintf("Yandex Disk доступен; каталог %s пока не существует и будет создан при загрузке", folder), nil
		}
		return "", err
	}
	label := strings.TrimSpace(folderInfo.Name)
	if label == "" {
		label = folder
	}
	return fmt.Sprintf("Yandex Disk доступен, каталог: %s", label), nil
}

type storagePathNotFoundError struct{ message string }

func (e *storagePathNotFoundError) Error() string { return e.message }

func yandexDiskGET(client *http.Client, token, endpoint string, values url.Values, dst any) error {
	if values != nil && len(values) > 0 {
		endpoint += "?" + values.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("не удалось создать Yandex Disk запрос")
	}
	req.Header.Set("Authorization", "OAuth "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Yandex Disk недоступен")
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return &storagePathNotFoundError{message: "Yandex Disk: каталог не найден"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Yandex Disk HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if dst != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, dst); err != nil {
			return fmt.Errorf("Yandex Disk вернул некорректный JSON")
		}
	}
	return nil
}

func googleDriveAccessToken(s LogDeliverySettings) (string, error) {
	if s.GoogleClientID == "" || s.GoogleClientSecret == "" || s.GoogleRefreshToken == "" {
		return "", fmt.Errorf("не заполнены Google client_id/client_secret/refresh_token")
	}
	form := url.Values{"client_id": {s.GoogleClientID}, "client_secret": {s.GoogleClientSecret}, "refresh_token": {s.GoogleRefreshToken}, "grant_type": {"refresh_token"}}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).PostForm(googleOAuthTokenURL, form)
	if err != nil {
		return "", fmt.Errorf("Google OAuth недоступен")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Google OAuth HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		return "", fmt.Errorf("Google OAuth: access_token не получен")
	}
	return token.AccessToken, nil
}

func testGoogleDriveAvailability(s LogDeliverySettings) (string, error) {
	accessToken, err := googleDriveAccessToken(s)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(googleDriveAPIBaseURL, "/")
	folderID := strings.TrimSpace(s.GoogleFolderID)
	endpoint := base + "/about?fields=user(displayName,emailAddress),storageQuota(limit,usage)"
	if folderID != "" {
		endpoint = base + "/files/" + url.PathEscape(folderID) + "?fields=id,name,mimeType&supportsAllDrives=true"
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("не удалось создать Google Drive запрос")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Google Drive недоступен")
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Google Drive HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if folderID == "" {
		return "Google Drive доступен; используется корень My Drive", nil
	}
	var info struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
	}
	if err := json.Unmarshal(payload, &info); err != nil {
		return "", fmt.Errorf("Google Drive вернул некорректный JSON")
	}
	if info.MimeType != "application/vnd.google-apps.folder" {
		return "", fmt.Errorf("Google Drive Folder ID указывает не на папку")
	}
	return fmt.Sprintf("Google Drive доступен, папка: %s", strings.TrimSpace(info.Name)), nil
}

func uploadGoogleDrive(s LogDeliverySettings, filename string, data []byte) error {
	accessToken, err := googleDriveAccessToken(s)
	if err != nil {
		return err
	}
	meta := map[string]any{"name": filename, "mimeType": xlsxContentType}
	if strings.TrimSpace(s.GoogleFolderID) != "" {
		meta["parents"] = []string{strings.TrimSpace(s.GoogleFolderID)}
	}
	var upload bytes.Buffer
	mw := multipart.NewWriter(&upload)
	metadataHeader := textproto.MIMEHeader{}
	metadataHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metadataPart, err := mw.CreatePart(metadataHeader)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(metadataPart).Encode(meta); err != nil {
		return err
	}
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Type", xlsxContentType)
	filePart, err := mw.CreatePart(fileHeader)
	if err != nil {
		return err
	}
	if _, err = filePart.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(googleDriveUploadBaseURL, "/")+"/files?uploadType=multipart", &upload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())
	uploadResp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("Google Drive upload request failed: %w", err)
	}
	defer uploadResp.Body.Close()
	uploadBody, _ := io.ReadAll(io.LimitReader(uploadResp.Body, 1<<20))
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return fmt.Errorf("Google Drive HTTP %d: %s", uploadResp.StatusCode, strings.TrimSpace(string(uploadBody)))
	}
	return nil
}
