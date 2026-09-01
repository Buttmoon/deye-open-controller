package models

type Settings struct {
	ID                             int64
	ApplicationName                string
	Timezone                       string
	SchedulerEnabled               bool
	FileLoggingEnabled             bool
	SchedulerIntervalMinutes       int
	SchedulerFillAllPointsCurrent  bool
	LogLevel                       string
	AppLogMaxSizeMB                int
	TaskLogMaxRows                 int
	InverterLoggingEnabled         bool
	InverterLoggingIntervalSeconds int
	InverterLogMaxSizeMB           int
	IntegrationURL                 string
	IntegrationAuthEnabled         bool
	IntegrationAuthScheme          string
	IntegrationAuthToken           string
	IntegrationAuthUsername        string
	IntegrationAuthPassword        string
	IntegrationPushIntervalSeconds int
	IntegrationServerName          string
	IntegrationServerIP            string
}
