package models

type ScheduleTemplate struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ViewMode     string `json:"view_mode"`
	ScheduleJSON string `json:"schedule_json"`
	CreatedAt    string `json:"created_at"`
}

type ScheduleTemplateListItem struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	ViewMode        string   `json:"view_mode"`
	ScheduleJSON    string   `json:"schedule_json"`
	CreatedAt       string   `json:"created_at"`
	InverterCount   int      `json:"inverter_count"`
	MatchedInverter string   `json:"matched_inverter"`
	UsedInverters   []string `json:"used_inverters"`
}
