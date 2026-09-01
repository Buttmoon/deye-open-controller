package main

import (
	"html/template"

	"inverter-schedule/internal/models"
)

type TemplatesListPageData struct {
	Title      string
	Templates  []models.ScheduleTemplateListItem
	Search     string
	Page       int
	PerPage    int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
}

type TemplateEditPageData struct {
	Title                 string
	TemplateID            int64
	Name                  string
	Description           string
	ViewMode              string
	ScheduleJSON          string
	ChargeModeOptionsJSON template.JS
	SchedulePowerMaxW     int
	GridExportMaxW        int
	IsEdit                bool
}

type TemplateApplyPageData struct {
	Title         string
	Inverters     []models.Inverter
	Templates     []models.ScheduleTemplate
	SelectedIDs   string
	SelectedCount int
}

type TimezoneOption struct {
	Value string
	Label string
}

type jsonResponse struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message"`
	Redirect string `json:"redirect,omitempty"`
}

type SettingsStats struct {
	InverterLogDir                 string
	InverterLogSizeBytes           int64
	InverterLogSizeHuman           string
	InverterLogFiles               int
	LastInverterLogFile            string
	InverterLoggingEnabled         bool
	InverterLoggingIntervalSeconds int
	InverterLogMaxSizeMB           int
	LastInverterLoggerRunUTC       string
	LastInverterLoggerRunLocal     string
	NextInverterLoggerRunUTC       string
	NextInverterLoggerRunLocal     string
	LastInverterLoggerStatus       string
	LastInverterLoggerMessage      string
	LastInverterLoggerCount        int
	InverterLoggerRunning          bool
	InverterLoggerCurrentTarget    string
	InverterLoggerCurrentStep      string
	InverterLoggerLastErrors       []string
	InverterLoggerDurationMS       int64
	InverterLoggerActiveParams     int
	InverterLoggerZeroParams       int
	InverterLoggingTestCommand     string
	InverterLogsEndpoint           string
	InverterLogFileEndpoint        string
	DBPath                         string
	DBSizeBytes                    int64
	DBSizeHuman                    string
	AppLogPath                     string
	AppLogSizeBytes                int64
	AppLogSizeHuman                string
	TotalInverters                 int
	EnabledInverters               int
	DisabledInverters              int
	OnlineInverters                int
	OfflineInverters               int
	TotalTemplates                 int
	EnabledTemplates               int
	SchedulerEnabled               bool
	FileLoggingEnabled             bool
	SchedulerIntervalMinutes       int
	TaskLogRows                    int
	LastSchedulerRunUTC            string
	LastSchedulerRunLocal          string
	NextSchedulerRunUTC            string
	NextSchedulerRunLocal          string
	LastSchedulerProcessed         int
	LastSchedulerTriggered         int
	LastSchedulerCommand           string
	SchedulerTestCommand           string
	LogsAPIEndpoint                string
	RawLogsEndpoint                string
	TaskLogsEndpoint               string
	TemplateExampleURL             string
}

type MainPageData struct {
	Title                  string
	ApplicationDisplayName string
	Inverters              []models.Inverter
	InverterModels         []InverterModelDefinition
	Search                 string
	Settings               models.Settings
	TimezoneOptions        []TimezoneOption
	SettingsStats          SettingsStats
	TimeOverview           TimeOverviewData
	Dashboard              InverterDashboardData
	ActiveCodes            []InverterLogExportColumn
	Page                   int
	PerPage                int
	TotalPages             int
	HasPrev                bool
	HasNext                bool
	PrevPage               int
	NextPage               int
}

type SettingsPageData struct {
	Title           string
	Settings        models.Settings
	TimezoneOptions []TimezoneOption
	SettingsStats   SettingsStats
	InverterModels  []InverterModelDefinition
	TestCommand     string
}

type TasksSettingsPageData struct {
	Title         string
	Settings      models.Settings
	SettingsStats SettingsStats
	TestCommand   string
}

type APIDocsPageData struct {
	Title                  string
	MarkdownHTML           template.HTML
	ExampleCreateInverter  string
	ExampleCreateTemplate  string
	ExampleBindTemplate    string
	ExampleScheduleExport  string
	ExampleInverterLogList string
	ExampleInverterLogFile string
}

type SchedulesPageData struct {
	Title                 string
	Inverters             []models.Inverter
	ScheduleName          string
	ViewMode              string
	ScheduleJSON          string
	Templates             []models.ScheduleTemplate
	SelectedIDsCSV        string
	ChargeModeOptionsJSON template.JS
	SchedulePowerMaxW     int
	GridExportMaxW        int
}

type TimeOverviewData struct {
	SystemDate          string
	SystemClock         string
	SystemTimezoneDate  string
	SystemTimezoneClock string
	SystemTimezoneLabel string
	InverterDate        string
	InverterClock       string
	InverterTime        string
	InverterTimeStatus  string
	LastDataReceived    string
	LastDataAgo         string
}

type DashboardRegisterData struct {
	Code        string
	Name        string
	Description string
	Value       string
	Suffix      string
	Group       string
}

type DashboardMetricValue struct {
	Code  string
	Label string
	Value string
}

type DashboardLogRowData struct {
	Timestamp         string
	InverterTimestamp string
	Values            []DashboardMetricValue
}

type InverterDashboardData struct {
	LatestTimestamp         string
	LatestInverterTimestamp string
	LatestLogFile           string
	InverterModelKey        string
	InverterModelName       string
	ParametersFile          string
	ActiveTemplateName      string
	ActiveScheduleName      string
	ActiveUseTimerMask      int
	RecentRows              []DashboardLogRowData
	CompareHeaders          []DashboardMetricValue
	Setpoints               []DashboardRegisterData
	Actuals                 []DashboardRegisterData
	RegisterDescriptions    []DashboardRegisterData
}

type MinuteSlot struct {
	Hour                 int    `json:"hour"`
	Minute               int    `json:"minute"`
	Label                string `json:"label"`
	Point                int    `json:"point"`
	SellTime             int    `json:"sell_time"`
	Enabled              bool   `json:"enabled"`
	SellModeKW           int    `json:"sell_mode_kw"`
	SellModeBattCapacity int    `json:"sell_mode_batt_capacity"`
	ChargeMode           int    `json:"charge_mode"`
	GridExportLimit      int    `json:"grid_export_limit"`
	GridChargeEnabled    bool   `json:"grid_charge_enabled"`
	SolarExport          bool   `json:"solar_export"`
	LoadLimitMode        int    `json:"load_limit_mode"`
	UseTimer             bool   `json:"use_timer"`
	UseTimerMask         int    `json:"use_timer_mask,omitempty"`
	PriorityLoad         int    `json:"priority_load"`
	DefaultCustomSlot    bool   `json:"default_custom_slot,omitempty"`
}

type HourConfig struct {
	Hour                 int    `json:"hour"`
	Label                string `json:"label"`
	Point                int    `json:"point"`
	SellTime             int    `json:"sell_time"`
	Enabled              bool   `json:"enabled"`
	SellModeKW           int    `json:"sell_mode_kw"`
	SellModeBattCapacity int    `json:"sell_mode_batt_capacity"`
	ChargeMode           int    `json:"charge_mode"`
	GridExportLimit      int    `json:"grid_export_limit"`
	GridChargeEnabled    bool   `json:"grid_charge_enabled"`
	SolarExport          bool   `json:"solar_export"`
	LoadLimitMode        int    `json:"load_limit_mode"`
	UseTimer             bool   `json:"use_timer"`
	UseTimerMask         int    `json:"use_timer_mask,omitempty"`
	PriorityLoad         int    `json:"priority_load"`
}

type DayItem struct {
	Day         int          `json:"day"`
	Enabled     bool         `json:"enabled"`
	Hours       []HourConfig `json:"hours"`
	Slots       []MinuteSlot `json:"slots,omitempty"`
	CustomSlots []MinuteSlot `json:"customSlots,omitempty"`
}

type SchedulePayload struct {
	UseTimerMask int       `json:"use_timer_mask"`
	Days         []DayItem `json:"days"`
}

type templateApplyResponse struct {
	OK           bool   `json:"ok"`
	Message      string `json:"message"`
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	ViewMode     string `json:"view_mode"`
	ScheduleJSON string `json:"schedule_json"`
}

type TemplateImportPageData struct {
	Title        string
	Imported     []ImportedTemplateResult
	ExampleSheet string
}

type ImportedTemplateResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Created     bool   `json:"created"`
	Error       string `json:"error"`
}

type TaskLogPageData struct {
	Title string
	Logs  []TaskRunLog
}

type TaskRunLog struct {
	ID              int64
	ExecutedAtUTC   string
	ExecutedAtLocal string
	InverterName    string
	InverterIP      string
	InverterPort    int
	TemplateName    string
	Status          string
	Message         string
	PayloadJSON     string
}

type AppLogsPageData struct {
	Title string
	Lines []string
}

type InverterLoggingPageData struct {
	Title         string
	Settings      models.Settings
	SettingsStats SettingsStats
	Files         []InverterLogFileInfo
	ExportColumns []InverterLogExportColumn
}

type InverterLogFileInfo struct {
	Name        string `json:"name"`
	SizeBytes   int64  `json:"size_bytes"`
	SizeHuman   string `json:"size_human"`
	ModifiedUTC string `json:"modified_utc"`
	DownloadURL string `json:"download_url"`
}

type InverterLogExportColumn struct {
	Number      int
	Code        string
	Name        string
	Description string
	Checked     bool
}

type IntegrationPageData struct {
	Title    string
	Settings models.Settings
}
