package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func baseDeliverySettings() LogDeliverySettings {
	return LogDeliverySettings{IntervalHours: 6, LookbackHours: 6, LookbackPeriod: logPeriod6Hours, LookbackCustomDays: 2}
}

func TestSplitAddressListDeduplicatesAndSupportsSeparators(t *testing.T) {
	got := splitAddressList("a@example.com; b@example.com\na@example.com, -100123")
	want := []string{"a@example.com", "b@example.com", "-100123"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

func TestValidateLogDeliverySMTP(t *testing.T) {
	s := baseDeliverySettings()
	s.SMTPEnabled = true
	s.LocationName = "Тестовая локация"
	s.SMTPHost = "smtp.example.com"
	s.SMTPPort = 587
	s.SMTPTLSMode = "starttls"
	s.SMTPFrom = "Inverter <inverter@example.com>"
	s.SMTPRecipients = "ops@example.com, admin@example.com"
	s.SMTPSubject = "Deye XLSX"
	if err := validateLogDeliverySettings(s); err != nil {
		t.Fatalf("valid SMTP settings rejected: %v", err)
	}
	s.SMTPSubject = "bad\r\nBcc: attacker@example.com"
	if err := validateLogDeliverySettings(s); err == nil || !strings.Contains(err.Error(), "headers") {
		t.Fatalf("CRLF header injection must be rejected: %v", err)
	}
}

func TestValidateLogDeliveryTelegram(t *testing.T) {
	s := baseDeliverySettings()
	s.TelegramEnabled = true
	s.TelegramChatIDs = "-1001234567890"
	if err := validateLogDeliverySettings(s); err == nil {
		t.Fatal("missing bot token must be rejected")
	}
	s.TelegramBotToken = "123456:ABCDEF"
	if err := validateLogDeliverySettings(s); err != nil {
		t.Fatalf("valid Telegram settings rejected: %v", err)
	}
}

func TestValidateLogDeliveryStorageVariants(t *testing.T) {
	s := baseDeliverySettings()
	s.StorageEnabled = true
	s.StorageProvider = "nextcloud"
	s.StorageURL = "https://cloud.example.com/remote.php/dav/files/user/"
	if err := validateLogDeliverySettings(s); err != nil {
		t.Fatalf("valid WebDAV settings rejected: %v", err)
	}
	s.StorageURL = "not-a-url"
	if err := validateLogDeliverySettings(s); err == nil {
		t.Fatal("invalid WebDAV URL must be rejected")
	}

	s = baseDeliverySettings()
	s.StorageEnabled = true
	s.StorageProvider = "google_drive"
	s.GoogleClientID = "client"
	s.GoogleClientSecret = "secret"
	if err := validateLogDeliverySettings(s); err == nil {
		t.Fatal("missing Google refresh token must be rejected")
	}
	s.GoogleRefreshToken = "refresh"
	if err := validateLogDeliverySettings(s); err != nil {
		t.Fatalf("valid Google Drive settings rejected: %v", err)
	}
}

func TestValidateLogDeliveryRequiresChannelAndBounds(t *testing.T) {
	s := baseDeliverySettings()
	if err := validateLogDeliverySettings(s); err == nil {
		t.Fatal("at least one channel must be required")
	}
	s.TelegramEnabled = true
	s.TelegramBotToken = "token"
	s.TelegramChatIDs = "1"
	s.IntervalHours = 0
	if err := validateLogDeliverySettings(s); err == nil {
		t.Fatal("zero interval must be rejected")
	}
}

func TestValidateLogDeliveryYandexDiskOAuth(t *testing.T) {
	s := baseDeliverySettings()
	s.StorageEnabled = true
	s.StorageProvider = "yandex_disk"
	if err := validateLogDeliverySettings(s); err == nil {
		t.Fatal("missing Yandex OAuth token must be rejected")
	}
	s.StoragePassword = "oauth-token"
	if err := validateLogDeliverySettings(s); err != nil {
		t.Fatalf("valid Yandex Disk REST settings rejected: %v", err)
	}
}

func TestNormalizeYandexDiskPath(t *testing.T) {
	cases := map[string]string{
		"":                         "app:/inverter-logs/log.xlsx",
		"inverter-logs":            "app:/inverter-logs/log.xlsx",
		"/reports/deye/":           "app:/reports/deye/log.xlsx",
		"app:/reports/deye":        "app:/reports/deye/log.xlsx",
		"disk:/operations/reports": "disk:/operations/reports/log.xlsx",
	}
	for input, want := range cases {
		if got := normalizeYandexDiskPath(input, "log.xlsx"); got != want {
			t.Fatalf("input=%q got=%q want=%q", input, got, want)
		}
	}
}

func TestLogDeliveryTemplateParses(t *testing.T) {
	if _, err := template.New("log-delivery").Parse(string(tmplLogDeliveryHTML)); err != nil {
		t.Fatalf("log delivery template is invalid: %v", err)
	}
}

func TestValidateLogDeliveryPeriods(t *testing.T) {
	for _, period := range []string{logPeriod6Hours, logPeriod12Hours, logPeriodDay, logPeriodWeek, logPeriodMonth} {
		s := baseDeliverySettings()
		s.LookbackPeriod = period
		if err := validateLogDeliveryPeriod(s); err != nil {
			t.Fatalf("period %s rejected: %v", period, err)
		}
	}
	s := baseDeliverySettings()
	s.LookbackPeriod = logPeriodCustomDays
	s.LookbackCustomDays = 3
	if err := validateLogDeliveryPeriod(s); err != nil {
		t.Fatalf("custom days rejected: %v", err)
	}
	s.LookbackCustomDays = 1
	if err := validateLogDeliveryPeriod(s); err == nil {
		t.Fatal("custom period shorter than two days must be rejected")
	}
	s.LookbackCustomDays = 366
	if err := validateLogDeliveryPeriod(s); err == nil {
		t.Fatal("custom period longer than 365 days must be rejected")
	}
	s.LookbackPeriod = "unknown"
	if err := validateLogDeliveryPeriod(s); err == nil {
		t.Fatal("unknown period must be rejected")
	}
}

func TestLogDeliveryTimeRanges(t *testing.T) {
	to := time.Date(2026, 8, 4, 20, 30, 0, 0, time.FixedZone("MSK", 3*60*60))
	cases := []struct {
		period string
		days   int
		want   time.Time
	}{
		{logPeriod6Hours, 2, to.Add(-6 * time.Hour)},
		{logPeriod12Hours, 2, to.Add(-12 * time.Hour)},
		{logPeriodDay, 2, to.Add(-24 * time.Hour)},
		{logPeriodCustomDays, 3, to.AddDate(0, 0, -3)},
		{logPeriodWeek, 2, to.AddDate(0, 0, -7)},
		{logPeriodMonth, 2, time.Date(2026, 7, 4, 20, 30, 0, 0, to.Location())},
	}
	for _, tc := range cases {
		from, gotTo, err := logDeliveryTimeRange(LogDeliverySettings{LookbackPeriod: tc.period, LookbackCustomDays: tc.days}, to)
		if err != nil {
			t.Fatalf("period %s: %v", tc.period, err)
		}
		if !from.Equal(tc.want) || !gotTo.Equal(to) {
			t.Fatalf("period %s: from=%s to=%s, want from=%s to=%s", tc.period, from, gotTo, tc.want, to)
		}
	}
}

func TestSubtractCalendarMonthClampsEndOfMonth(t *testing.T) {
	loc := time.FixedZone("UTC+3", 3*60*60)
	cases := []struct {
		input time.Time
		want  time.Time
	}{
		{time.Date(2026, 3, 31, 10, 15, 0, 0, loc), time.Date(2026, 2, 28, 10, 15, 0, 0, loc)},
		{time.Date(2024, 3, 31, 10, 15, 0, 0, loc), time.Date(2024, 2, 29, 10, 15, 0, 0, loc)},
		{time.Date(2026, 1, 30, 10, 15, 0, 0, loc), time.Date(2025, 12, 30, 10, 15, 0, 0, loc)},
	}
	for _, tc := range cases {
		if got := subtractCalendarMonthClamped(tc.input); !got.Equal(tc.want) {
			t.Fatalf("input=%s got=%s want=%s", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeLegacyLookbackHours(t *testing.T) {
	cases := []struct {
		hours      int
		wantPeriod string
		wantDays   int
	}{
		{6, logPeriod6Hours, 2},
		{12, logPeriod12Hours, 2},
		{24, logPeriodDay, 2},
		{72, logPeriodCustomDays, 3},
		{168, logPeriodWeek, 2},
		{720, logPeriodMonth, 2},
	}
	for _, tc := range cases {
		s := LogDeliverySettings{LookbackHours: tc.hours}
		normalizeLogDeliveryPeriod(&s)
		if s.LookbackPeriod != tc.wantPeriod || s.LookbackCustomDays != tc.wantDays {
			t.Fatalf("hours=%d got period=%s days=%d want period=%s days=%d", tc.hours, s.LookbackPeriod, s.LookbackCustomDays, tc.wantPeriod, tc.wantDays)
		}
	}
}

func TestBuildLogDeliverySubjectUsesLocationAndLocalTime(t *testing.T) {
	loc := time.FixedZone("MSK", 3*60*60)
	at := time.Date(2026, 8, 13, 10, 57, 30, 0, loc)
	got := buildLogDeliverySubject("СПБ, Склад №2", at)
	want := "Deye Log — СПБ, Склад №2 — 13.08.2026 10:57"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestValidateLogDeliverySMTPDoesNotRequireDuplicatedLocationSetting(t *testing.T) {
	s := baseDeliverySettings()
	s.SMTPEnabled = true
	s.SMTPHost = "smtp.example.com"
	s.SMTPPort = 587
	s.SMTPTLSMode = "starttls"
	s.SMTPFrom = "inverter@example.com"
	s.SMTPRecipients = "ops@example.com"
	if err := validateLogDeliverySettings(s); err != nil {
		t.Fatalf("SMTP settings must not require a separate location field: %v", err)
	}
}

func TestSelectApplicationDisplayNameUsesSameFallbackOrderEverywhere(t *testing.T) {
	cases := []struct {
		appName, inverterName, wantName, wantSource string
	}{
		{" СПБ Склад ", "Deye 60KW #1", "СПБ Склад", "Имя приложения"},
		{"", " Deye 60KW #1 ", "Deye 60KW #1", "имя первого инвертора (Имя приложения не задано)"},
		{"", "", "Управление расписанием инверторов", "стандартное имя (Имя приложения и инверторы не заданы)"},
	}
	for _, tc := range cases {
		name, source := selectApplicationDisplayName(tc.appName, tc.inverterName)
		if name != tc.wantName || source != tc.wantSource {
			t.Fatalf("app=%q inverter=%q got=(%q,%q) want=(%q,%q)", tc.appName, tc.inverterName, name, source, tc.wantName, tc.wantSource)
		}
	}
}

func TestValidateSMTPHostAllowsArbitraryHostNames(t *testing.T) {
	for _, host := range []string{
		"e.b5g.ru",
		"b5g,ru",
		"mail",
		"smtp01",
		"smtp_internal",
		"smtp.internal",
		"127.0.0.1",
		"2001:db8::1",
		"smtp://custom-endpoint",
		"host with spaces",
	} {
		if err := validateSMTPHost(host); err != nil {
			t.Fatalf("SMTP host %q must be accepted and left for the network stack to resolve: %v", host, err)
		}
	}
}

func TestValidateSMTPHostRejectsOnlyEmptyAndControlCharacters(t *testing.T) {
	for _, host := range []string{"", "   ", "mail\rserver", "mail\nserver", "mail\x00server"} {
		if err := validateSMTPHost(host); err == nil {
			t.Fatalf("SMTP host %q must be rejected", host)
		}
	}
}

func TestValidateLogDeliverySMTPAllowsNonDNSStyleHost(t *testing.T) {
	s := baseDeliverySettings()
	s.SMTPEnabled = true
	s.SMTPHost = "b5g,ru"
	s.SMTPPort = 587
	s.SMTPTLSMode = "starttls"
	s.SMTPFrom = "inverter@example.com"
	s.SMTPRecipients = "ops@example.com"
	if err := validateLogDeliverySettings(s); err != nil {
		t.Fatalf("SMTP host syntax must not be restricted by the application: %v", err)
	}
}

func TestBuildSMTPTextBodyIncludesLocationPeriodRangeAndInverters(t *testing.T) {
	loc := time.FixedZone("MSK", 3*60*60)
	from := time.Date(2026, 8, 13, 4, 57, 0, 0, loc)
	to := time.Date(2026, 8, 13, 10, 57, 0, 0, loc)
	body := buildSMTPTextBody("СПБ, Склад №2", "последние 6 часов", from, to, []string{"Deye 60kW #1", "Deye 25kW #2"})
	for _, needle := range []string{"СПБ, Склад №2", "последние 6 часов", "13.08.2026 04:57", "13.08.2026 10:57", "Deye 60kW #1", "Deye 25kW #2"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("body must contain %q; body=%q", needle, body)
		}
	}
}

func TestTelegramAvailabilityUsesReadOnlyBotMethods(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"username": "deye_test_bot"}})
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": int64(1001), "title": "Test chat"}})
		default:
			t.Fatalf("unexpected Telegram method: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	oldBase := telegramAPIBaseURL
	telegramAPIBaseURL = server.URL
	defer func() { telegramAPIBaseURL = oldBase }()

	s := baseDeliverySettings()
	s.TelegramBotToken = "123456:SECRET"
	s.TelegramChatIDs = "-1001, -1002"
	results, err := testTelegramAvailability(s)
	if err != nil {
		t.Fatalf("availability failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got results=%v", results)
	}
	for _, p := range paths {
		if strings.Contains(p, "sendDocument") {
			t.Fatalf("availability must not send documents: %v", paths)
		}
	}
}

func TestWebDAVAvailabilityUsesPropfindAndNeverPut(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method != "PROPFIND" {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("Depth") != "0" {
			t.Fatalf("expected Depth: 0, got %q", r.Header.Get("Depth"))
		}
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer server.Close()
	s := baseDeliverySettings()
	s.StorageProvider = "nextcloud"
	s.StorageURL = server.URL + "/remote.php/dav/files/user/"
	s.StorageRemotePath = "inverter-logs"
	message, err := testStorageAvailability(s)
	if err != nil {
		t.Fatalf("availability failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(message), "webdav") {
		t.Fatalf("unexpected message: %s", message)
	}
	for _, m := range methods {
		if m == http.MethodPut {
			t.Fatalf("availability must not PUT files: %v", methods)
		}
	}
}

func TestYandexDiskAvailabilityUsesReadOnlyAPI(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "OAuth oauth-secret" {
			t.Fatalf("missing OAuth auth")
		}
		if r.Method != http.MethodGet {
			t.Fatalf("availability must be GET-only, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "inverter-logs", "type": "dir"})
	}))
	defer server.Close()
	oldBase := yandexDiskAPIBaseURL
	yandexDiskAPIBaseURL = server.URL + "/v1/disk"
	defer func() { yandexDiskAPIBaseURL = oldBase }()
	s := baseDeliverySettings()
	s.StorageProvider = "yandex_disk"
	s.StoragePassword = "oauth-secret"
	s.StorageRemotePath = "app:/inverter-logs"
	if _, err := testStorageAvailability(s); err != nil {
		t.Fatalf("availability failed: %v", err)
	}
	for _, m := range methods {
		if !strings.HasPrefix(m, "GET ") {
			t.Fatalf("unexpected mutating request: %v", methods)
		}
	}
}

func TestGoogleDriveAvailabilityRefreshesTokenAndReadsFolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			if r.Method != http.MethodPost {
				t.Fatalf("token method=%s", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-secret"})
		case "/drive/v3/files/folder-123":
			if r.Method != http.MethodGet {
				t.Fatalf("drive method=%s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer access-secret" {
				t.Fatalf("missing bearer token")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "folder-123", "name": "Inverter Logs", "mimeType": "application/vnd.google-apps.folder"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	oldToken, oldDrive := googleOAuthTokenURL, googleDriveAPIBaseURL
	googleOAuthTokenURL, googleDriveAPIBaseURL = server.URL+"/token", server.URL+"/drive/v3"
	defer func() { googleOAuthTokenURL, googleDriveAPIBaseURL = oldToken, oldDrive }()
	s := baseDeliverySettings()
	s.StorageProvider = "google_drive"
	s.GoogleClientID = "client"
	s.GoogleClientSecret = "secret"
	s.GoogleRefreshToken = "refresh"
	s.GoogleFolderID = "folder-123"
	message, err := testStorageAvailability(s)
	if err != nil {
		t.Fatalf("availability failed: %v", err)
	}
	if !strings.Contains(message, "Inverter Logs") {
		t.Fatalf("folder name missing: %s", message)
	}
}

func TestValidateLogDeliveryChannelIgnoresEnabledCheckbox(t *testing.T) {
	s := baseDeliverySettings()
	s.LocationName = "СПБ, Склад №2"
	s.SMTPHost = "smtp.example.com"
	s.SMTPPort = 587
	s.SMTPTLSMode = "starttls"
	s.SMTPFrom = "inverter@example.com"
	s.SMTPRecipients = "ops@example.com"
	if err := validateLogDeliveryChannel(s, "smtp"); err != nil {
		t.Fatalf("SMTP test must validate configured channel even when disabled: %v", err)
	}

	s = baseDeliverySettings()
	s.TelegramBotToken = "123:ABC"
	s.TelegramChatIDs = "-1001"
	if err := validateLogDeliveryChannel(s, "telegram"); err != nil {
		t.Fatalf("Telegram test must validate configured channel even when disabled: %v", err)
	}

	s = baseDeliverySettings()
	s.StorageProvider = "nextcloud"
	s.StorageURL = "https://cloud.example.com/remote.php/dav/files/user/"
	if err := validateLogDeliveryChannel(s, "storage"); err != nil {
		t.Fatalf("storage test must validate configured channel even when disabled: %v", err)
	}
}

func TestValidateLogDeliveryChannelRejectsUnknownChannel(t *testing.T) {
	if err := validateLogDeliveryChannel(baseDeliverySettings(), "unknown"); err == nil {
		t.Fatal("unknown channel must be rejected")
	}
}

func TestLogDeliveryTemplateHasIndependentChannelTestControls(t *testing.T) {
	html := string(tmplLogDeliveryHTML)
	for _, needle := range []string{
		`Локация отчёта:`,
		`Источник:`,
		`id="testSMTPDelivery"`,
		`id="testTelegramAvailability"`,
		`id="testTelegramDelivery"`,
		`id="testStorageAvailability"`,
		`id="testStorageDelivery"`,
		`Deye Log —`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("log-delivery template must contain %q", needle)
		}
	}
	if strings.Contains(html, `name="smtp_subject"`) {
		t.Fatal("SMTP subject must be automatic, not manually editable")
	}
	if strings.Contains(html, `name="location_name"`) {
		t.Fatal("location must come from application display name, not an editable delivery field")
	}
}

func TestBuildLogDeliverySubjectPreservesInjectionForFinalHeaderGuard(t *testing.T) {
	subject := buildLogDeliverySubject("СПБ\r\nBcc: attacker@example.com", time.Now())
	if !strings.ContainsAny(subject, "\r\n") {
		t.Fatal("test precondition failed: injected location must reach final SMTP header guard")
	}
}

func TestTelegramAvailabilityReportsEveryChatIndependently(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/botTOKEN/getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":42,"username":"report_bot"}}`))
		case "/botTOKEN/getChat":
			switch r.URL.Query().Get("chat_id") {
			case "100":
				_, _ = w.Write([]byte(`{"ok":true,"result":{"id":100,"title":"Рабочий чат","type":"group"}}`))
			case "200":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
			case "300":
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"ok":false,"description":"Forbidden: bot was blocked by the user"}`))
			default:
				t.Fatalf("unexpected chat_id %q", r.URL.Query().Get("chat_id"))
			}
		default:
			t.Fatalf("unexpected Telegram method %s", r.URL.Path)
		}
	}))
	defer server.Close()

	oldBase := telegramAPIBaseURL
	telegramAPIBaseURL = server.URL
	defer func() { telegramAPIBaseURL = oldBase }()

	results, err := testTelegramAvailability(LogDeliverySettings{
		TelegramBotToken: "TOKEN",
		TelegramChatIDs:  "100,200,300",
	})
	if err == nil {
		t.Fatal("expected aggregate availability error when some chats are unavailable")
	}
	joined := strings.Join(results, "; ")
	for _, want := range []string{
		"Бот доступен: @report_bot",
		"Чат 100 доступен: Рабочий чат",
		"Чат 200: чат не найден",
		"Чат 300: бот заблокирован",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("availability results missing %q: %s", want, joined)
		}
	}
	for _, chatID := range []string{"100", "200", "300"} {
		found := false
		for _, request := range seen {
			if strings.Contains(request, "chat_id="+chatID) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("chat %s was not checked; requests=%v", chatID, seen)
		}
	}
}
