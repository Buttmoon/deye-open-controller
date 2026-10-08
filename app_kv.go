package main

import (
	"net/http"
	"strconv"
	"strings"
)

// Keys of the app_kv table. Values are stored as strings; typed accessors below
// apply defaults so a missing key never changes existing behaviour.
const (
	kvFeatureGridPeakShaving     = "feature.grid_peak_shaving"
	kvFeatureRegisterTestWrites  = "feature.register_test_writes"
	kvFeatureSimpleScheduler     = "feature.simple_scheduler"
	kvHistoryRetentionDays       = "history.retention_days"
	kvHistoryMaxRows             = "history.max_rows"
	kvStatusPollEnabled          = "status.background_poll_enabled"
	kvStatusPollIntervalSeconds  = "status.poll_interval_seconds"
	kvStatusTimeoutMS            = "status.timeout_ms"
	kvStatusOfflineAfterFailures = "status.offline_after_failures"
	kvStatusStaleAfterSeconds    = "status.stale_after_seconds"
	kvUITheme                    = "ui.default_theme"
	kvScheduleDefaultEditor      = "schedule.default_editor"
)

type kvSettingSpec struct {
	Key         string
	Kind        string // bool | int | enum
	Default     string
	Min, Max    int
	Options     []string
	Label       string
	Description string
	Section     string
}

var kvSettingSpecs = []kvSettingSpec{
	{Key: kvUITheme, Kind: "enum", Default: "dark", Options: []string{"dark", "light", "auto"}, Section: "general",
		Label: "Тема оформления по умолчанию", Description: "Каждый пользователь может переключить тему в верхней панели; выбор хранится в браузере."},
	{Key: kvScheduleDefaultEditor, Kind: "enum", Default: "advanced", Options: []string{"advanced", "simple"}, Section: "schedules",
		Label: "Редактор расписания по умолчанию", Description: "Какой режим открывается из меню «Расписание». Оба режима всегда доступны."},
	{Key: kvFeatureGridPeakShaving, Kind: "bool", Default: "0", Section: "features",
		Label: "Ограничение мощности (Grid Peak Shaving)", Description: "Показывает блок управления регистрами 178 (биты 4–5) и 191. Когда выключено, приложение не пишет в эти регистры и не меняет текущее состояние функции на инверторе."},
	{Key: kvFeatureRegisterTestWrites, Kind: "bool", Default: "1", Section: "features",
		Label: "Запись на странице «Тестирование регистров»", Description: "Чтение и пробный расчёт (dry-run) доступны всегда. Реальная запись требует отдельного подтверждения каждой операции."},
	{Key: kvFeatureSimpleScheduler, Kind: "bool", Default: "1", Section: "features",
		Label: "Упрощённый редактор расписаний", Description: "Редактор для операторов: дни недели, интервалы, режимы работы вместо регистров."},
	{Key: kvHistoryRetentionDays, Kind: "int", Default: "180", Min: 1, Max: 3650, Section: "system",
		Label: "Хранить историю операций, дней", Description: "Записи старше этого срока удаляются автоматически (проверка каждые 10 минут и при запуске)."},
	{Key: kvHistoryMaxRows, Kind: "int", Default: "200000", Min: 1000, Max: 5000000, Section: "system",
		Label: "Максимум записей истории", Description: "При превышении удаляются самые старые записи."},
	{Key: kvStatusPollEnabled, Kind: "bool", Default: "0", Section: "inverters",
		Label: "Фоновый опрос состояния инверторов", Description: "Если выключено, живые данные читаются только по кнопке «Обновить» на странице «Состояние»; открытие страницы инверторы не опрашивает."},
	{Key: kvStatusPollIntervalSeconds, Kind: "int", Default: "60", Min: 10, Max: 3600, Section: "inverters",
		Label: "Интервал фонового опроса, сек", Description: "Пауза между циклами фонового опроса всех инверторов."},
	{Key: kvStatusTimeoutMS, Kind: "int", Default: "3000", Min: 500, Max: 30000, Section: "inverters",
		Label: "Таймаут связи при опросе, мс", Description: "Время ожидания ответа Modbus для одного блока регистров."},
	{Key: kvStatusOfflineAfterFailures, Kind: "int", Default: "3", Min: 1, Max: 20, Section: "inverters",
		Label: "Считать «Нет связи» после N ошибок подряд", Description: "Одиночный сбой не переводит инвертор в состояние «Нет связи»."},
	{Key: kvStatusStaleAfterSeconds, Kind: "int", Default: "300", Min: 30, Max: 86400, Section: "inverters",
		Label: "Данные устаревают через, сек", Description: "После этого срока значения помечаются как устаревшие."},
}

func kvSpec(key string) (kvSettingSpec, bool) {
	for _, spec := range kvSettingSpecs {
		if spec.Key == key {
			return spec, true
		}
	}
	return kvSettingSpec{}, false
}

func (a *App) loadKVCache() {
	a.kvMu.Lock()
	defer a.kvMu.Unlock()
	if a.kvCache != nil {
		return
	}
	a.kvCache = map[string]string{}
	rows, err := a.db.Query(`SELECT key, value FROM app_kv`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			a.kvCache[k] = v
		}
	}
}

func (a *App) kvGet(key string) (string, bool) {
	a.loadKVCache()
	a.kvMu.RLock()
	defer a.kvMu.RUnlock()
	v, ok := a.kvCache[key]
	return v, ok
}

func (a *App) kvSet(key, value string) error {
	a.loadKVCache()
	_, err := a.db.Exec(`INSERT INTO app_kv (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, key, value)
	if err != nil {
		return err
	}
	a.kvMu.Lock()
	a.kvCache[key] = value
	a.kvMu.Unlock()
	return nil
}

func (a *App) kvString(key string) string {
	if v, ok := a.kvGet(key); ok {
		return v
	}
	if spec, ok := kvSpec(key); ok {
		return spec.Default
	}
	return ""
}

func (a *App) kvBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(a.kvString(key)))
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

func (a *App) kvInt(key string) int {
	spec, _ := kvSpec(key)
	n, err := strconv.Atoi(strings.TrimSpace(a.kvString(key)))
	if err != nil {
		n, _ = strconv.Atoi(spec.Default)
	}
	if spec.Kind == "int" {
		if n < spec.Min {
			n = spec.Min
		}
		if spec.Max > 0 && n > spec.Max {
			n = spec.Max
		}
	}
	return n
}

func (a *App) gridPeakFeatureEnabled() bool { return a.kvBool(kvFeatureGridPeakShaving) }

// migrateFeatureFlags decides the Grid Peak Shaving default exactly once.
// New installations get the feature hidden. Existing installations where the
// feature was already used (a stored power/state exists) keep it visible, so an
// upgrade does not silently remove a control the operator relied on.
func (a *App) migrateFeatureFlags() {
	if _, ok := a.kvGet(kvFeatureGridPeakShaving); ok {
		return
	}
	var used int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM inverters WHERE grid_peak_shaving_power IS NOT NULL OR grid_peak_shaving_enabled IS NOT NULL`).Scan(&used)
	value := "0"
	if err == nil && used > 0 {
		value = "1"
	}
	if err := a.kvSet(kvFeatureGridPeakShaving, value); err == nil {
		a.recordHistory(HistoryEntry{
			OperationType: opConfigChange, Status: histApplied, Initiator: "система (миграция)",
			Message: "Начальное значение флага «Ограничение мощности»: " + map[string]string{"0": "выключено", "1": "включено (функция уже использовалась)"}[value],
			Details: map[string]any{"key": kvFeatureGridPeakShaving, "value": value, "existing_usage_rows": used},
		})
	}
}

type kvSettingView struct {
	kvSettingSpec
	Value string `json:"value"`
}

func (a *App) kvSettingViews() []kvSettingView {
	out := make([]kvSettingView, 0, len(kvSettingSpecs))
	for _, spec := range kvSettingSpecs {
		out = append(out, kvSettingView{kvSettingSpec: spec, Value: a.kvString(spec.Key)})
	}
	return out
}

func validateKVValue(spec kvSettingSpec, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	switch spec.Kind {
	case "bool":
		switch strings.ToLower(raw) {
		case "1", "true", "on", "yes":
			return "1", true
		case "0", "false", "off", "no", "":
			return "0", true
		}
		return "", false
	case "int":
		n, err := strconv.Atoi(raw)
		if err != nil || n < spec.Min || (spec.Max > 0 && n > spec.Max) {
			return "", false
		}
		return strconv.Itoa(n), true
	case "enum":
		for _, opt := range spec.Options {
			if opt == raw {
				return raw, true
			}
		}
		return "", false
	}
	return "", false
}

// apiAppSettingsHandler: GET returns all extended settings; POST (form) updates
// one or more keys. Each changed key is recorded in the operation history.
func (a *App) apiAppSettingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": a.kvSettingViews()})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
			return
		}
		changed := []string{}
		for key, values := range r.PostForm {
			spec, ok := kvSpec(key)
			if !ok || len(values) == 0 {
				continue
			}
			value, valid := validateKVValue(spec, values[len(values)-1])
			if !valid {
				writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Недопустимое значение для «" + spec.Label + "»"})
				return
			}
			old := a.kvString(key)
			if old == value {
				continue
			}
			if err := a.kvSet(key, value); err != nil {
				writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось сохранить: " + err.Error()})
				return
			}
			changed = append(changed, spec.Label)
			a.recordHistory(HistoryEntry{
				OperationType: opConfigChange, Status: histApplied, Initiator: requestInitiator(r),
				Message: "Изменена настройка «" + spec.Label + "»: " + old + " → " + value,
				Details: map[string]any{"key": key, "before": old, "after": value},
			})
		}
		if len(changed) > 0 {
			a.status.reconfigure()
		}
		msg := "Изменений нет"
		if len(changed) > 0 {
			msg = "Сохранено: " + strings.Join(changed, ", ")
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg, "settings": a.kvSettingViews()})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
	}
}