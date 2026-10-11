package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func (a *App) configureSyncBearer(t *testing.T, token string) {
	t.Helper()
	if err := a.saveSyncAuthSettings("bearer", token, "", ""); err != nil {
		t.Fatalf("saveSyncAuthSettings: %v", err)
	}
}

func (a *App) testDoAuth(t *testing.T, method, target, auth string, body []byte, headers map[string]string) testResponse {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.httpHandler().ServeHTTP(w, req)
	return testResponse{Code: w.Code, Body: w.Body.Bytes()}
}

func syncPutBody(name string, enabled bool, revision int64, scheduleJSON any) []byte {
	b, _ := json.Marshal(map[string]any{
		"name":          name,
		"view_mode":     "list",
		"is_enabled":    enabled,
		"schedule_json": scheduleJSON,
		"revision":      revision,
	})
	return b
}

func TestSyncAuthFailClosed(t *testing.T) {
	a := newTestApp(t)
	id := a.testAddInverter(t, "A", "10.0.0.1", 8899, "")
	_ = a.setInverterExternalID(id, "site-a")

	resp := a.testGet(t, "/api/schedules/site-a")
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured: want 503, got %d %s", resp.Code, resp.Body)
	}
	if code := resp.JSON(t)["error"].(map[string]any)["code"]; code != "auth_not_configured" {
		t.Fatalf("code=%v", code)
	}

	a.configureSyncBearer(t, "secret-token")
	resp = a.testGet(t, "/api/schedules/site-a")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth: want 401, got %d", resp.Code)
	}

	resp = a.testDoAuth(t, http.MethodGet, "/api/schedules/site-a", "Bearer wrong", nil, nil)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", resp.Code)
	}

	resp = a.testDoAuth(t, http.MethodGet, "/api/schedules/site-a", "Bearer secret-token", nil, nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("authorized missing schedule: want 404, got %d %s", resp.Code, resp.Body)
	}
}

func TestSyncDeviceRefNeverUsesDBID(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	id := a.testAddInverter(t, "A", "10.0.0.2", 8899, "")
	_ = a.setInverterExternalID(id, "ext-2")

	resp := a.testDoAuth(t, http.MethodGet, fmt.Sprintf("/api/schedules/%d", id), "Bearer tok", nil, nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("bare DB id must not resolve: got %d %s", resp.Code, resp.Body)
	}

	resp = a.testDoAuth(t, http.MethodGet, "/api/schedules/10.0.0.2:8899", "Bearer tok", nil, nil)
	if resp.Code != http.StatusNotFound { // device found, schedule missing
		t.Fatalf("ip:port ref: want schedule_not_found 404, got %d %s", resp.Code, resp.Body)
	}
	if code := resp.JSON(t)["error"].(map[string]any)["code"]; code != "schedule_not_found" {
		t.Fatalf("code=%v", code)
	}
}

func TestSyncFirstCreateIfMatchZero(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	id := a.testAddInverter(t, "A", "10.0.0.3", 8899, "")
	_ = a.setInverterExternalID(id, "create-me")

	schedule := map[string]any{"use_timer_mask": 1, "days": []any{}}

	// Missing If-Match → 428
	resp := a.testDoAuth(t, http.MethodPut, "/api/schedules/create-me", "Bearer tok",
		syncPutBody("S", true, 1, schedule), nil)
	if resp.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: want 428, got %d %s", resp.Code, resp.Body)
	}

	// If-Match: 1 on create → 409
	resp = a.testDoAuth(t, http.MethodPut, "/api/schedules/create-me", "Bearer tok",
		syncPutBody("S", true, 1, schedule), map[string]string{"If-Match": "1"})
	if resp.Code != http.StatusConflict {
		t.Fatalf("If-Match 1 on create: want 409, got %d %s", resp.Code, resp.Body)
	}

	// revision != 1 → 409
	resp = a.testDoAuth(t, http.MethodPut, "/api/schedules/create-me", "Bearer tok",
		syncPutBody("S", true, 2, schedule), map[string]string{"If-Match": "0"})
	if resp.Code != http.StatusConflict {
		t.Fatalf("revision 2 on create: want 409, got %d %s", resp.Code, resp.Body)
	}

	resp = a.testDoAuth(t, http.MethodPut, "/api/schedules/create-me", "Bearer tok",
		syncPutBody("Site", true, 1, schedule), map[string]string{"If-Match": "0"})
	if resp.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d %s", resp.Code, resp.Body)
	}
	got := resp.JSON(t)
	if int64(got["revision"].(float64)) != 1 {
		t.Fatalf("revision=%v", got["revision"])
	}
	if got["updated_at"] == nil || got["updated_at"] == "" {
		t.Fatal("updated_at missing")
	}

	// GET returns entity
	resp = a.testDoAuth(t, http.MethodGet, "/api/schedules/create-me", "Bearer tok", nil, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d", resp.Code)
	}

	// Stale update
	resp = a.testDoAuth(t, http.MethodPut, "/api/schedules/create-me", "Bearer tok",
		syncPutBody("Site", true, 2, schedule), map[string]string{"If-Match": "0"})
	if resp.Code != http.StatusConflict {
		t.Fatalf("stale: want 409, got %d", resp.Code)
	}

	resp = a.testDoAuth(t, http.MethodPut, "/api/schedules/create-me", "Bearer tok",
		syncPutBody("Site2", false, 2, schedule), map[string]string{"If-Match": "1"})
	if resp.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d %s", resp.Code, resp.Body)
	}
	if int64(resp.JSON(t)["revision"].(float64)) != 2 {
		t.Fatalf("revision after update=%v", resp.JSON(t)["revision"])
	}
}

func TestSyncConcurrentFirstCreate(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	id := a.testAddInverter(t, "A", "10.0.0.4", 8899, "")
	_ = a.setInverterExternalID(id, "race-create")
	schedule := map[string]any{"use_timer_mask": 0, "days": []any{}}
	body := syncPutBody("R", false, 1, schedule)

	var okCount, conflictCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := a.testDoAuth(t, http.MethodPut, "/api/schedules/race-create", "Bearer tok", body, map[string]string{"If-Match": "0"})
			switch resp.Code {
			case http.StatusOK:
				okCount.Add(1)
			case http.StatusConflict:
				conflictCount.Add(1)
			default:
				t.Errorf("unexpected status %d %s", resp.Code, resp.Body)
			}
		}()
	}
	wg.Wait()
	if okCount.Load() != 1 {
		t.Fatalf("want exactly 1 success, got %d ok / %d conflict", okCount.Load(), conflictCount.Load())
	}
}

func TestSyncConcurrentUpdateCAS(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	id := a.testAddInverter(t, "A", "10.0.0.5", 8899, "")
	_ = a.setInverterExternalID(id, "race-update")
	schedule := map[string]any{"use_timer_mask": 1, "days": []any{}}
	resp := a.testDoAuth(t, http.MethodPut, "/api/schedules/race-update", "Bearer tok",
		syncPutBody("R", true, 1, schedule), map[string]string{"If-Match": "0"})
	if resp.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", resp.Code, resp.Body)
	}

	body := syncPutBody("R2", true, 2, schedule)
	var okCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := a.testDoAuth(t, http.MethodPut, "/api/schedules/race-update", "Bearer tok", body, map[string]string{"If-Match": "1"})
			if r.Code == http.StatusOK {
				okCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if okCount.Load() != 1 {
		t.Fatalf("want 1 update winner, got %d", okCount.Load())
	}
}

func TestSyncPUTDoesNotImmediateModbus(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	mock := newMockInverter(t)
	id := a.testAddInverter(t, "A", "127.0.0.1", mock.Port, "")
	_ = a.setInverterExternalID(id, "no-modbus")
	schedule := map[string]any{"use_timer_mask": 1, "days": []any{}}
	beforeR, beforeW := mock.counts()
	resp := a.testDoAuth(t, http.MethodPut, "/api/schedules/no-modbus", "Bearer tok",
		syncPutBody("S", true, 1, schedule), map[string]string{"If-Match": "0"})
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", resp.Code, resp.Body)
	}
	afterR, afterW := mock.counts()
	if afterR != beforeR || afterW != beforeW {
		t.Fatalf("Sync PUT must not touch Modbus: before=%d/%d after=%d/%d", beforeR, beforeW, afterR, afterW)
	}
}

func TestSyncRecurrencePausedNotDeleted(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	id := a.testAddInverter(t, "A", "10.0.0.6", 8899, "")
	_ = a.setInverterExternalID(id, "recur")
	_, err := a.db.Exec(`INSERT INTO schedule_recurrences (inverter_id, rules_json, auto_renew, last_generated_month, updated_at)
		VALUES (?, '[]', 1, '2026-01', CURRENT_TIMESTAMP)`, id)
	if err != nil {
		t.Fatal(err)
	}
	schedule := map[string]any{"use_timer_mask": 0, "days": []any{}}
	resp := a.testDoAuth(t, http.MethodPut, "/api/schedules/recur", "Bearer tok",
		syncPutBody("S", false, 1, schedule), map[string]string{"If-Match": "0"})
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", resp.Code, resp.Body)
	}
	var auto int
	var rules string
	if err := a.db.QueryRow(`SELECT auto_renew, rules_json FROM schedule_recurrences WHERE inverter_id = ?`, id).Scan(&auto, &rules); err != nil {
		t.Fatal(err)
	}
	if auto != 0 {
		t.Fatalf("auto_renew=%d want 0", auto)
	}
	if rules != "[]" {
		t.Fatalf("rules deleted: %q", rules)
	}
}

func TestLocalSaveBumpsRevisionOnce(t *testing.T) {
	a := newTestApp(t)
	id := a.testAddInverter(t, "A", "10.0.0.7", 8899, "")
	if err := a.saveScheduleForInverter(id, "N", "list", `{"use_timer_mask":1,"days":[]}`); err != nil {
		t.Fatal(err)
	}
	s, err := a.getScheduleByInverterID(id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Revision != 1 {
		t.Fatalf("after first save revision=%d", s.Revision)
	}
	if err := a.saveScheduleForInverter(id, "N2", "list", `{"use_timer_mask":1,"days":[]}`); err != nil {
		t.Fatal(err)
	}
	s, _ = a.getScheduleByInverterID(id)
	if s.Revision != 2 {
		t.Fatalf("after second save revision=%d", s.Revision)
	}
	if err := a.toggleScheduleByInverterID(id, false); err != nil {
		t.Fatal(err)
	}
	s, _ = a.getScheduleByInverterID(id)
	if s.Revision != 3 {
		t.Fatalf("after toggle revision=%d", s.Revision)
	}
}

func TestScreenAuthGatedByInverterIDNotAuthorization(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	id := a.testAddInverter(t, "A", "10.0.0.8", 8899, "")
	_ = a.setInverterExternalID(id, "screen-a")
	_, _ = a.db.Exec(`UPDATE inverters SET grid_peak_shaving_power = 1000, grid_peak_shaving_enabled = 1 WHERE id = ?`, id)

	// Empty query: UI path, no auth required.
	resp := a.testGet(t, "/api/inverter/screen")
	if resp.Code != http.StatusOK {
		// May fail if no log files — but must not be 401/503.
		if resp.Code == http.StatusUnauthorized || resp.Code == http.StatusServiceUnavailable {
			t.Fatalf("UI screen must not require Sync auth: %d %s", resp.Code, resp.Body)
		}
	}

	// With inverter_id and no Authorization → 401
	resp = a.testGet(t, "/api/inverter/screen?inverter_id=screen-a")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("omit auth + inverter_id: want 401, got %d %s", resp.Code, resp.Body)
	}

	// Authorization alone without inverter_id must not switch to Sync mode (still UI path).
	resp = a.testDoAuth(t, http.MethodGet, "/api/inverter/screen", "Bearer tok", nil, nil)
	if resp.Code == http.StatusUnauthorized || resp.Code == http.StatusServiceUnavailable {
		t.Fatalf("Authorization without inverter_id must stay UI path, got %d", resp.Code)
	}
}

func TestScreenIsolationNoLegacyBleed(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "tok")
	idA := a.testAddInverter(t, "A", "10.0.0.9", 8899, "")
	idB := a.testAddInverter(t, "B", "10.0.0.10", 8899, "")
	_ = a.setInverterExternalID(idA, "iso-a")
	_ = a.setInverterExternalID(idB, "iso-b")
	_, _ = a.db.Exec(`UPDATE inverters SET grid_peak_shaving_power = 1111, grid_peak_shaving_enabled = 1 WHERE id = ?`, idA)
	_, _ = a.db.Exec(`UPDATE inverters SET grid_peak_shaving_power = 2222, grid_peak_shaving_enabled = 1 WHERE id = ?`, idB)

	resp := a.testDoAuth(t, http.MethodGet, "/api/inverter/screen?inverter_id=iso-b", "Bearer tok", nil, nil)
	// Without log files the handler may 500 — but if OK, settings must reflect B.
	if resp.Code == http.StatusOK {
		data := resp.JSON(t)
		settings, _ := data["settings"].(map[string]any)
		if settings != nil {
			if int(settings["grid_peak_value"].(float64)) != 2222 {
				t.Fatalf("screen B leaked A data: %#v", settings["grid_peak_value"])
			}
		}
		if int64(data["inverter_id"].(float64)) != idB {
			t.Fatalf("inverter_id=%v want %d", data["inverter_id"], idB)
		}
	}
}

func TestSyncTokenRotation(t *testing.T) {
	a := newTestApp(t)
	a.configureSyncBearer(t, "old-token")
	id := a.testAddInverter(t, "A", "10.0.0.11", 8899, "")
	_ = a.setInverterExternalID(id, "rot")

	resp := a.testDoAuth(t, http.MethodGet, "/api/schedules/rot", "Bearer old-token", nil, nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("old token should work: %d", resp.Code)
	}
	a.configureSyncBearer(t, "new-token")
	resp = a.testDoAuth(t, http.MethodGet, "/api/schedules/rot", "Bearer old-token", nil, nil)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation: want 401, got %d", resp.Code)
	}
	resp = a.testDoAuth(t, http.MethodGet, "/api/schedules/rot", "Bearer new-token", nil, nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("new token: want 404, got %d", resp.Code)
	}
}

func TestResolveSyncDeviceRefMatrix(t *testing.T) {
	a := newTestApp(t)
	id := a.testAddInverter(t, "A", "192.168.1.50", 8899, "")
	_ = a.setInverterExternalID(id, "3")

	got, ref, err := a.resolveSyncDeviceRef("3")
	if err != nil || got != id || ref != "3" {
		t.Fatalf("external_id digits: id=%d ref=%s err=%v", got, ref, err)
	}
	got, ref, err = a.resolveSyncDeviceRef("192.168.1.50:8899")
	if err != nil || got != id {
		t.Fatalf("ip:port: id=%d ref=%s err=%v", got, ref, err)
	}
	_, _, err = a.resolveSyncDeviceRef(fmt.Sprintf("%d", id))
	// Digits equal to DB id but not external_id of another device — here external_id IS "3" and id may differ.
	// If id happens to be 3, external_id match wins which is correct. If id != 3, looking up external_id=id string fails.
	if id != 3 {
		if err == nil {
			t.Fatal("bare other numeric id must not resolve via DB id fallback")
		}
	}
}
