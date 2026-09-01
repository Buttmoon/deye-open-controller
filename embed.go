package main

import "embed"

//go:embed templates/assets/*
var staticAssets embed.FS

//go:embed templates/pages/main.html
var tmplMainHTML []byte

//go:embed templates/pages/schedules.html
var tmplSchedulesHTML []byte

//go:embed templates/pages/templates_list.html
var tmplTemplatesListHTML []byte

//go:embed templates/pages/template_edit.html
var tmplTemplateEditHTML []byte

//go:embed templates/pages/template_apply.html
var tmplTemplateApplyHTML []byte

//go:embed templates/pages/template_import.html
var tmplTemplateImportHTML []byte

//go:embed templates/pages/task_logs.html
var tmplTaskLogsHTML []byte

//go:embed templates/pages/app_logs.html
var tmplAppLogsHTML []byte

//go:embed templates/pages/settings.html
var tmplSettingsHTML []byte

//go:embed templates/pages/tasks_settings.html
var tmplTaskSettingsHTML []byte

//go:embed templates/pages/inverter_logging.html
var tmplInverterLogsHTML []byte

//go:embed templates/pages/api_docs.html
var tmplAPIDocsHTML []byte

//go:embed templates/pages/integration.html
var tmplIntegrationHTML []byte

//go:embed API_DOCUMENTATION.md
var apiDocumentationMD []byte

//go:embed templates/pages/log_delivery.html
var tmplLogDeliveryHTML []byte
