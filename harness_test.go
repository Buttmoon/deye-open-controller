package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simonvetter/modbus"

	"inverter-schedule/internal/db"
)

// newTestApp builds a fully routed App in a temporary working directory with
// the embedded default profiles and a fresh SQLite database. No background
// workers are started.
func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	if _, err := installEmbeddedDefaults("."); err != nil {
		t.Fatalf("installEmbeddedDefaults: %v", err)
	}
	database, err := db.InitSQLiteAt(filepath.Join("data", "app.db"))
	if err != nil {
		t.Fatalf("InitSQLiteAt: %v", err)
	}
	a := newAppWithDB(database, filepath.Join("data", "app.log"))
	t.Cleanup(func() { _ = database.Close() })
	if _, err := a.getSettings(); err != nil {
		t.Fatalf("getSettings: %v", err)
	}
	// Saving a schedule restarts the scheduler; it must never run real cycles in tests.
	if _, err := database.Exec(`UPDATE settings SET scheduler_enabled = 0, inverter_logging_enabled = 0`); err != nil {
		t.Fatalf("disable scheduler: %v", err)
	}
	a.invalidateSettingsCache()
	if s, _ := a.getSettings(); s.SchedulerEnabled {
		t.Fatal("scheduler must be disabled in tests")
	}
	t.Cleanup(a.stopScheduler)
	return a
}

func (a *App) testAddInverter(t *testing.T, name, ip string, port int, modelKey string) int64 {
	t.Helper()
	if modelKey == "" {
		modelKey = defaultInverterModelKey
	}
	res, err := a.db.Exec(`INSERT INTO inverters (name, ip, port, model_key, profile_write_confirmed) VALUES (?, ?, ?, ?, 1)`, name, ip, port, modelKey)
	if err != nil {
		t.Fatalf("insert inverter: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

type testResponse struct {
	Code int
	Body []byte
}

func (r testResponse) JSON(t *testing.T) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("response is not JSON (status %d): %v\n%s", r.Code, err, r.Body)
	}
	return out
}

func (a *App) testDo(t *testing.T, method, target string, body io.Reader, contentType string) testResponse {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	a.httpHandler().ServeHTTP(w, req)
	return testResponse{Code: w.Code, Body: w.Body.Bytes()}
}

func (a *App) testGet(t *testing.T, target string) testResponse {
	return a.testDo(t, http.MethodGet, target, nil, "")
}

func (a *App) testPostJSON(t *testing.T, target string, payload any) testResponse {
	t.Helper()
	var body []byte
	switch v := payload.(type) {
	case string:
		body = []byte(v)
	default:
		var err error
		if body, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	return a.testDo(t, http.MethodPost, target, bytes.NewReader(body), "application/json")
}

func (a *App) testPostForm(t *testing.T, target string, form url.Values) testResponse {
	return a.testDo(t, http.MethodPost, target, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

// mockInverter is an in-process Modbus/TCP server used instead of real
// hardware. It records every request so tests can assert that no write (or no
// request at all) reached the device.
type mockInverter struct {
	mu            sync.Mutex
	holding       map[uint16]uint16
	input         map[uint16]uint16
	illegal       map[uint16]bool // addresses answered with "illegal data address"
	ignoreWrites  map[uint16]bool // writes acknowledged but not applied
	slowAddr      map[uint16]time.Duration
	failNextReads int // next N reads answer "server device failure"
	reads         int
	writes        []mockWrite
	server        *modbus.ModbusServer
	Port          int
}

type mockWrite struct {
	Address uint16
	Values  []uint16
}

func newMockInverter(t *testing.T) *mockInverter {
	t.Helper()
	m := &mockInverter{holding: map[uint16]uint16{}, input: map[uint16]uint16{}, illegal: map[uint16]bool{}, ignoreWrites: map[uint16]bool{}, slowAddr: map[uint16]time.Duration{}}
	m.Port = freeTCPPort(t)
	srv, err := modbus.NewServer(&modbus.ServerConfiguration{URL: fmt.Sprintf("tcp://127.0.0.1:%d", m.Port), Timeout: 10 * time.Second, MaxClients: 8}, m)
	if err != nil {
		t.Fatalf("modbus.NewServer: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("mock start: %v", err)
	}
	m.server = srv
	t.Cleanup(func() { _ = srv.Stop() })
	return m
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// closedTCPPort returns a local port with no listener (connection refused).
func closedTCPPort(t *testing.T) int { return freeTCPPort(t) }

func (m *mockInverter) set(addr, value uint16) {
	m.mu.Lock()
	m.holding[addr] = value
	m.mu.Unlock()
}

func (m *mockInverter) get(addr uint16) uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holding[addr]
}

func (m *mockInverter) counts() (reads, writes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads, len(m.writes)
}

func (m *mockInverter) HandleCoils(*modbus.CoilsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}

func (m *mockInverter) HandleDiscreteInputs(*modbus.DiscreteInputsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}

func (m *mockInverter) rangeCheck(addr, qty uint16) (time.Duration, error) {
	var delay time.Duration
	for i := uint16(0); i < qty; i++ {
		if m.illegal[addr+i] {
			return 0, modbus.ErrIllegalDataAddress
		}
		if d := m.slowAddr[addr+i]; d > delay {
			delay = d
		}
	}
	return delay, nil
}

func (m *mockInverter) HandleHoldingRegisters(req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	m.mu.Lock()
	delay, err := m.rangeCheck(req.Addr, req.Quantity)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if req.IsWrite {
		m.writes = append(m.writes, mockWrite{Address: req.Addr, Values: append([]uint16(nil), req.Args...)})
		for i, v := range req.Args {
			if !m.ignoreWrites[req.Addr+uint16(i)] {
				m.holding[req.Addr+uint16(i)] = v
			}
		}
		m.mu.Unlock()
		return nil, nil
	}
	m.reads++
	if m.failNextReads > 0 {
		m.failNextReads--
		m.mu.Unlock()
		return nil, modbus.ErrServerDeviceFailure
	}
	out := make([]uint16, req.Quantity)
	for i := range out {
		out[i] = m.holding[req.Addr+uint16(i)]
	}
	m.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return out, nil
}

func (m *mockInverter) HandleInputRegisters(req *modbus.InputRegistersRequest) ([]uint16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.rangeCheck(req.Addr, req.Quantity); err != nil {
		return nil, err
	}
	m.reads++
	out := make([]uint16, req.Quantity)
	for i := range out {
		out[i] = m.input[req.Addr+uint16(i)]
	}
	return out, nil
}
