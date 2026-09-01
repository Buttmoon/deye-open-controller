package models

import "database/sql"

type Schedule struct {
	ID                int64
	InverterID        int64
	Name              string
	IsEnabled         bool
	ViewMode          string
	ScheduleJSON      string
	AppliedTemplateID sql.NullInt64
	UpdatedAt         string
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
	Day     int          `json:"day"`
	Enabled bool         `json:"enabled"`
	Hours   []HourConfig `json:"hours"`
}

type SchedulePayload struct {
	UseTimerMask int       `json:"use_timer_mask"`
	Days         []DayItem `json:"days"`
}
