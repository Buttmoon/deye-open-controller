package models

type Inverter struct {
	ID                    int64
	Name                  string
	IP                    string
	Port                  int
	ModelKey              string
	ModelName             string
	ModelCode             string
	ParametersFile        string
	ValidationStatus      string
	ProfileWriteConfirmed bool
	WriteRequiresConfirm  bool
	IsOnline              bool
	HasSchedule           bool
	ScheduleEnabled       bool
	HasTemplate           bool
	AppliedTemplateID     int64
	AppliedTemplate       string
}
