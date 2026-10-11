package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"inverter-schedule/internal/models"
)

type syncScheduleEntity struct {
	Ref             string          `json:"ref"`
	Name            string          `json:"name"`
	ViewMode        string          `json:"view_mode"`
	IsEnabled       bool            `json:"is_enabled"`
	ScheduleJSON    json.RawMessage `json:"schedule_json"`
	Revision        int64           `json:"revision"`
	UpdatedAt       string          `json:"updated_at"`
	ClientUpdatedAt string          `json:"client_updated_at,omitempty"`
}

type syncSchedulePutBody struct {
	Name            string          `json:"name"`
	ViewMode        string          `json:"view_mode"`
	IsEnabled       bool            `json:"is_enabled"`
	ScheduleJSON    json.RawMessage `json:"schedule_json"`
	Revision        int64           `json:"revision"`
	UpdatedAt       string          `json:"updated_at"`
	ClientUpdatedAt string          `json:"client_updated_at"`
}

func (a *App) apiScheduleSyncHandler(w http.ResponseWriter, r *http.Request) {
	if !a.requireSyncAuth(w, r) {
		return
	}
	refRaw := strings.TrimPrefix(r.URL.Path, "/api/schedules/")
	refRaw = strings.Trim(refRaw, "/")
	if refRaw == "" || strings.Contains(refRaw, "/") {
		// Leave other /api/schedules/* routes alone (bind-template, copy, timeline, send-preview).
		http.NotFound(w, r)
		return
	}
	// Reserved subpaths handled by more specific mux registrations; if we still get them, 404.
	switch refRaw {
	case "bind-template", "copy", "timeline", "send-preview":
		http.NotFound(w, r)
		return
	}

	inverterID, canonicalRef, err := a.resolveSyncDeviceRef(refRaw)
	if err != nil {
		var refErr *syncDeviceRefError
		if errors.As(err, &refErr) {
			writeSyncError(w, refErr.Status, refErr.Code, refErr.Message)
			return
		}
		writeSyncError(w, http.StatusInternalServerError, "internal_error", "failed to resolve device ref")
		return
	}

	switch r.Method {
	case http.MethodGet:
		a.handleSyncScheduleGET(w, r, inverterID, canonicalRef)
	case http.MethodPut:
		a.handleSyncSchedulePUT(w, r, inverterID, canonicalRef)
	default:
		writeSyncError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func formatScheduleUpdatedAtRFC3339(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC().Format(time.RFC3339)
	}
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return raw
}

func syncScheduleEntityFromRow(ref string, s models.Schedule) (syncScheduleEntity, error) {
	raw := strings.TrimSpace(s.ScheduleJSON)
	if raw == "" {
		raw = "{}"
	}
	if !json.Valid([]byte(raw)) {
		return syncScheduleEntity{}, fmt.Errorf("invalid schedule_json stored")
	}
	ent := syncScheduleEntity{
		Ref:          ref,
		Name:         s.Name,
		ViewMode:     s.ViewMode,
		IsEnabled:    s.IsEnabled,
		ScheduleJSON: json.RawMessage(raw),
		Revision:     s.Revision,
		UpdatedAt:    formatScheduleUpdatedAtRFC3339(s.UpdatedAt),
	}
	if s.ClientUpdatedAt.Valid && strings.TrimSpace(s.ClientUpdatedAt.String) != "" {
		ent.ClientUpdatedAt = strings.TrimSpace(s.ClientUpdatedAt.String)
	}
	return ent, nil
}

func (a *App) handleSyncScheduleGET(w http.ResponseWriter, r *http.Request, inverterID int64, ref string) {
	s, err := a.getScheduleByInverterID(inverterID)
	if err == sql.ErrNoRows {
		writeSyncError(w, http.StatusNotFound, "schedule_not_found", "schedule not found for device")
		return
	}
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, "internal_error", "failed to load schedule")
		return
	}
	ent, err := syncScheduleEntityFromRow(ref, s)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, ent.Revision))
	writeJSON(w, http.StatusOK, ent)
}

func parseIfMatchRevision(header string) (value int64, present bool, malformed bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false, false
	}
	header = strings.Trim(header, `"`)
	header = strings.TrimSpace(header)
	if header == "*" {
		return 0, true, true // * is not accepted for Sync create/update
	}
	n, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return 0, true, true
	}
	return n, true, false
}

func (a *App) handleSyncSchedulePUT(w http.ResponseWriter, r *http.Request, inverterID int64, ref string) {
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeSyncError(w, http.StatusBadRequest, "invalid_body", "failed to read body")
		return
	}
	var body syncSchedulePutBody
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeSyncError(w, http.StatusBadRequest, "invalid_json", "body must be JSON")
		return
	}
	ifMatch, present, malformed := parseIfMatchRevision(r.Header.Get("If-Match"))
	if malformed {
		writeSyncError(w, http.StatusBadRequest, "invalid_if_match", "If-Match must be an integer revision")
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Расписание"
	}
	viewMode := strings.TrimSpace(body.ViewMode)
	if viewMode == "" {
		viewMode = "list"
	}

	scheduleJSON := strings.TrimSpace(string(body.ScheduleJSON))
	if scheduleJSON == "" || scheduleJSON == "null" {
		writeSyncError(w, http.StatusBadRequest, "invalid_schedule_json", "schedule_json is required")
		return
	}
	// Accept object or JSON-encoded string.
	var asString string
	if err := json.Unmarshal(body.ScheduleJSON, &asString); err == nil {
		scheduleJSON = asString
	}
	if compact, err := normalizeScheduleJSONCompact(scheduleJSON); err == nil {
		scheduleJSON = compact
	} else {
		writeSyncError(w, http.StatusBadRequest, "invalid_schedule_json", "schedule_json is invalid: "+err.Error())
		return
	}

	invs, err := a.getBasicInvertersByIDs([]int64{inverterID})
	if err != nil || len(invs) == 0 {
		writeSyncError(w, http.StatusNotFound, "device_not_found", "device not found")
		return
	}
	model, modelErr := findInverterModel(invs[0].ModelKey)
	if modelErr != nil {
		writeSyncError(w, http.StatusInternalServerError, "profile_error", modelErr.Error())
		return
	}
	if err := validateScheduleJSONForModels(scheduleJSON, []InverterModelDefinition{model}); err != nil {
		writeSyncError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	// Align Use Timer bit0 with is_enabled.
	scheduleJSON = setScheduleUseTimerEnabled(scheduleJSON, body.IsEnabled)
	if compact, err := normalizeScheduleJSONCompact(scheduleJSON); err == nil {
		scheduleJSON = compact
	}

	clientUpdated := strings.TrimSpace(body.ClientUpdatedAt)
	if clientUpdated == "" {
		clientUpdated = strings.TrimSpace(body.UpdatedAt)
	}

	result, casErr := a.upsertScheduleWithRevisionCAS(r.Context(), syncScheduleUpsertInput{
		InverterID:      inverterID,
		Name:            name,
		ViewMode:        viewMode,
		IsEnabled:       body.IsEnabled,
		ScheduleJSON:    scheduleJSON,
		IfMatch:         ifMatch,
		IfMatchPresent:  present,
		BodyRevision:    body.Revision,
		ClientUpdatedAt: clientUpdated,
	})
	if casErr != nil {
		var sc *syncCASError
		if errors.As(casErr, &sc) {
			writeSyncError(w, sc.Status, sc.Code, sc.Message)
			return
		}
		if errors.Is(casErr, context.Canceled) {
			writeSyncError(w, 499, "canceled", "request canceled")
			return
		}
		writeSyncError(w, http.StatusInternalServerError, "internal_error", "failed to persist schedule: "+casErr.Error())
		return
	}

	a.recordHistory(HistoryEntry{
		OperationType: opJobUpdate,
		Status:        histApplied,
		InverterID:    inverterID,
		Initiator:     "sync-api",
		Message:       "Remote Sync superseded schedule (auto-renew paused)",
		Details: map[string]any{
			"ref":      ref,
			"revision": result.Schedule.Revision,
			"created":  result.Created,
		},
	})
	// Intentionally do NOT call recreateActiveSchedulerTask — next 5-min tick applies.

	ent, err := syncScheduleEntityFromRow(ref, result.Schedule)
	if err != nil {
		writeSyncError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, ent.Revision))
	writeJSON(w, http.StatusOK, ent)
}
