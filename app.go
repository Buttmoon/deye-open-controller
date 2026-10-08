package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"inverter-schedule/internal/db"
	"inverter-schedule/internal/models"
)

type SchedulerState struct {
	LastRunUTC  time.Time
	NextRunUTC  time.Time
	Processed   int
	Triggered   int
	LastCommand string
}

type InverterLoggerState struct {
	Running        bool
	LastStartedUTC time.Time
	LastRunUTC     time.Time
	NextRunUTC     time.Time
	LastFile       string
	LastMessage    string
	LastStatus     string
	LastCount      int
	CurrentTarget  string
	CurrentStep    string
	LastErrors     []string
	LastDurationMS int64
	ActiveParams   int
	ZeroParams     int
}

type App struct {
	db                      *sql.DB
	tmplMain                *template.Template
	tmplSchedules           *template.Template
	tmplTemplatesList       *template.Template
	tmplTemplateEdit        *template.Template
	tmplTemplateApply       *template.Template
	tmplTemplateImport      *template.Template
	tmplTaskLogs            *template.Template
	tmplAppLogs             *template.Template
	tmplSettings            *template.Template
	tmplTaskSettings        *template.Template
	tmplInverterLogs        *template.Template
	tmplAPIDocs             *template.Template
	tmplIntegration         *template.Template
	tmplLogDelivery         *template.Template
	tmplHistory             *template.Template
	tmplRegisterTest        *template.Template
	tmplStatus              *template.Template
	tmplSimpleSchedule      *template.Template
	tmplScheduleTemplates   *template.Template
	mux                     *http.ServeMux
	schedulerState          SchedulerState
	inverterLogState        InverterLoggerState
	integrationPushState    integrationPushState
	appLogPath              string
	schedulerCtlMu          sync.Mutex
	schedulerStopCh         chan struct{}
	inverterLogCtlMu        sync.Mutex
	inverterLogStopCh       chan struct{}
	integrationPusherCtlMu  sync.Mutex
	integrationPusherStopCh chan struct{}
	logDeliveryCtlMu        sync.Mutex
	logDeliveryStopCh       chan struct{}
	logDeliveryRuntime      logDeliveryRuntime
	modbusMu                sync.Mutex
	settingsMu              sync.RWMutex
	settingsCache           models.Settings
	settingsCacheLoaded     bool
	kvMu                    sync.RWMutex
	kvCache                 map[string]string
	status                  *inverterStatusMonitor
	backgroundStopCh        chan struct{}
	backgroundWG            sync.WaitGroup
	startedAt               time.Time
	defaultsReport          defaultsInstallReport
}

func NewApp() (*App, error) {
	if dir := strings.TrimSpace(os.Getenv("INVERTER_SCHEDULE_WORKDIR")); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("workdir init error: %w", err)
		}
		if err := os.Chdir(dir); err != nil {
			return nil, fmt.Errorf("workdir chdir error: %w", err)
		}
	}
	report, err := installEmbeddedDefaults(".")
	if err != nil {
		return nil, fmt.Errorf("default profiles init error: %w", err)
	}
	database, err := db.InitSQLite()
	if err != nil {
		return nil, fmt.Errorf("database init error: %w", err)
	}
	app := newAppWithDB(database, "data/app.log")
	app.defaultsReport = report
	app.migrateFeatureFlags()
	app.appendAppLog("info", "application initialized", map[string]any{"component": "boot", "defaults": report.Summary()})
	app.startScheduler()
	app.restoreActiveSchedulesOnStartup()
	app.startInverterLogger()
	app.startIntegrationPusher()
	app.startLogDelivery()
	app.startBackgroundServices()
	return app, nil
}

// newAppWithDB builds a fully routed App without starting background workers.
// Tests use it directly with a temporary database.
func newAppWithDB(database *sql.DB, appLogPath string) *App {
	parseTemplate := func(data []byte) *template.Template {
		t := template.New("")
		t, err := t.Parse(string(data))
		if err != nil {
			panic("template parse error: " + err.Error())
		}
		return t
	}

	app := &App{
		db:                 database,
		tmplMain:           parseTemplate(tmplMainHTML),
		tmplSchedules:      parseTemplate(tmplSchedulesHTML),
		tmplTemplatesList:  parseTemplate(tmplTemplatesListHTML),
		tmplTemplateEdit:   parseTemplate(tmplTemplateEditHTML),
		tmplTemplateApply:  parseTemplate(tmplTemplateApplyHTML),
		tmplTemplateImport: parseTemplate(tmplTemplateImportHTML),
		tmplTaskLogs:       parseTemplate(tmplTaskLogsHTML),
		tmplAppLogs:        parseTemplate(tmplAppLogsHTML),
		tmplSettings:       parseTemplate(tmplSettingsHTML),
		tmplTaskSettings:   parseTemplate(tmplTaskSettingsHTML),
		tmplInverterLogs:   parseTemplate(tmplInverterLogsHTML),
		tmplAPIDocs:        parseTemplate(tmplAPIDocsHTML),
		tmplIntegration:    parseTemplate(tmplIntegrationHTML),
		tmplLogDelivery:    parseTemplate(tmplLogDeliveryHTML),
		tmplHistory:        parseTemplate(tmplHistoryHTML),
		tmplRegisterTest:   parseTemplate(tmplRegisterTestHTML),
		tmplStatus:         parseTemplate(tmplStatusHTML),
		tmplSimpleSchedule:    parseTemplate(tmplSimpleScheduleHTML),
		tmplScheduleTemplates: parseTemplate(tmplScheduleTemplatesHTML),
		mux:                   http.NewServeMux(),
		appLogPath:         appLogPath,
		startedAt:          time.Now(),
	}
	app.status = newInverterStatusMonitor(app)
	_ = app.ensureLogDir()
	app.registerRoutes()
	return app
}

func (a *App) Close() {
	a.stopScheduler()
	a.stopInverterLogger()
	a.stopIntegrationPusher()
	a.stopLogDelivery()
	a.stopBackgroundServices()
	if a.db != nil {
		_ = a.db.Close()
	}
}

func listenAddress() string {
	if addr := strings.TrimSpace(os.Getenv("INVERTER_SCHEDULE_ADDR")); addr != "" {
		return addr
	}
	return ":8081"
}

func (a *App) Server() *http.Server {
	return &http.Server{
		Addr:              listenAddress(),
		Handler:           a.httpHandler(),
		ReadHeaderTimeout: 15 * time.Second,
	}
}
