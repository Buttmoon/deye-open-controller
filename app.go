package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
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
}

func NewApp() (*App, error) {
	database, err := db.InitSQLite()
	if err != nil {
		return nil, fmt.Errorf("database init error: %w", err)
	}

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
		mux:                http.NewServeMux(),
		appLogPath:         "data/app.log",
	}

	_ = app.ensureLogDir()
	app.appendAppLog("info", "application initialized", map[string]any{"component": "boot"})
	app.registerRoutes()
	app.startScheduler()
	app.restoreActiveSchedulesOnStartup()
	app.startInverterLogger()
	app.startIntegrationPusher()
	app.startLogDelivery()
	return app, nil
}

func (a *App) Close() {
	a.stopScheduler()
	a.stopInverterLogger()
	a.stopIntegrationPusher()
	a.stopLogDelivery()
	if a.db != nil {
		_ = a.db.Close()
	}
}

func (a *App) Server() *http.Server {
	return &http.Server{
		Addr:    ":8080",
		Handler: a.mux,
	}
}
