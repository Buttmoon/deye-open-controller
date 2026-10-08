package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHealthEndpointContract(t *testing.T) {
	a := newTestApp(t)
	res := a.testGet(t, "/health")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /health status=%d", res.Code)
	}
	body := res.JSON(t)
	if body["ok"] != true || body["message"] != "deye-open-controller" || len(body) != 2 {
		t.Fatalf("GET /health body=%s, want exactly {\"ok\":true,\"message\":\"deye-open-controller\"}", res.Body)
	}
	if res := a.testDo(t, http.MethodPost, "/health", nil, ""); res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health status=%d, want 405", res.Code)
	}
}

func modbusReadBody(port int, extra string, blocks string) string {
	return fmt.Sprintf(`{"ip":"127.0.0.1","port":%d%s,"blocks":%s}`, port, extra, blocks)
}

func blockValues(t *testing.T, block any) []int {
	t.Helper()
	m, _ := block.(map[string]any)
	raw, _ := m["values"].([]any)
	out := []int{}
	for _, v := range raw {
		out = append(out, int(v.(float64)))
	}
	return out
}

func TestModbusReadReturnsBlocksInRequestOrder(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(100, 1)
	mock.set(101, 2)
	mock.set(102, 65535)
	mock.input[500] = 42

	res := a.testPostJSON(t, "/api/modbus/read", modbusReadBody(mock.Port, "", `[{"address":100,"count":3},{"address":500,"count":1,"register_type":"input"}]`))
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	body := res.JSON(t)
	blocks := body["blocks"].([]any)
	if body["ok"] != true || len(blocks) != 2 {
		t.Fatalf("unexpected body %s", res.Body)
	}
	if got := blockValues(t, blocks[0]); fmt.Sprint(got) != "[1 2 65535]" {
		t.Fatalf("holding block values=%v", got)
	}
	if got := blockValues(t, blocks[1]); fmt.Sprint(got) != "[42]" {
		t.Fatalf("input block values=%v", got)
	}
	if blocks[1].(map[string]any)["register_type"] != "input" || blocks[0].(map[string]any)["register_type"] != "holding" {
		t.Fatalf("register types not normalised: %s", res.Body)
	}
	if _, writes := mock.counts(); writes != 0 {
		t.Fatalf("read API performed %d writes", writes)
	}
}

func TestModbusReadReportsPerBlockErrors(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(10, 7)
	mock.illegal[300] = true

	res := a.testPostJSON(t, "/api/modbus/read", modbusReadBody(mock.Port, `,"retries":3`, `[{"address":10,"count":1},{"address":300,"count":2}]`))
	if res.Code != http.StatusOK {
		t.Fatalf("partial failure must still be 200, got %d %s", res.Code, res.Body)
	}
	body := res.JSON(t)
	blocks := body["blocks"].([]any)
	if body["ok"] != true || !strings.Contains(body["message"].(string), "1 из 2") {
		t.Fatalf("unexpected summary: %s", res.Body)
	}
	if got := blockValues(t, blocks[0]); fmt.Sprint(got) != "[7]" {
		t.Fatalf("first block=%v", got)
	}
	second := blocks[1].(map[string]any)
	if second["error"] != "illegal data address" || second["values"] != nil {
		t.Fatalf("second block must carry the device exception, got %v", second)
	}
	// A Modbus exception is a definitive answer: no retries for that block.
	if reads, _ := mock.counts(); reads != 1 {
		t.Fatalf("device received %d reads, want 1 (exception answers are not retried)", reads)
	}
}

func TestModbusReadAllBlocksFailWhenDeviceUnreachable(t *testing.T) {
	a := newTestApp(t)
	port := closedTCPPort(t)
	res := a.testPostJSON(t, "/api/modbus/read", modbusReadBody(port, `,"retries":1,"timeout_ms":500`, `[{"address":1,"count":1}]`))
	if res.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502; body=%s", res.Code, res.Body)
	}
	body := res.JSON(t)
	block := body["blocks"].([]any)[0].(map[string]any)
	if body["ok"] != false || block["error"] != "connection refused" {
		t.Fatalf("unexpected body %s", res.Body)
	}
}

func TestModbusReadRetriesTransientFailure(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(20, 1234)
	mock.failNextReads = 1

	res := a.testPostJSON(t, "/api/modbus/read", modbusReadBody(mock.Port, `,"retries":2`, `[{"address":20,"count":1}]`))
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	if got := blockValues(t, res.JSON(t)["blocks"].([]any)[0]); fmt.Sprint(got) != "[1234]" {
		t.Fatalf("values=%v", got)
	}
	if reads, _ := mock.counts(); reads != 2 {
		t.Fatalf("reads=%d, want 2 (one failure + one retry)", reads)
	}

	mock.failNextReads = 1
	res = a.testPostJSON(t, "/api/modbus/read", modbusReadBody(mock.Port, `,"retries":0`, `[{"address":20,"count":1}]`))
	if res.Code != http.StatusBadGateway || !strings.Contains(string(res.Body), "server device failure") {
		t.Fatalf("retries=0 means a single attempt; got %d %s", res.Code, res.Body)
	}
}

func TestModbusReadTimeout(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.slowAddr[50] = 700 * time.Millisecond

	started := time.Now()
	res := a.testPostJSON(t, "/api/modbus/read", modbusReadBody(mock.Port, `,"retries":1,"timeout_ms":200`, `[{"address":50,"count":1}]`))
	if res.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	if block := res.JSON(t)["blocks"].([]any)[0].(map[string]any); block["error"] != "timeout" {
		t.Fatalf("error=%v, want timeout", block["error"])
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("timeout_ms not honoured: took %s", time.Since(started))
	}
}

func TestModbusReadValidationNeverContactsDevice(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	manyBlocks := "[" + strings.TrimSuffix(strings.Repeat(`{"address":1,"count":1},`, 65), ",") + "]"
	cases := map[string]string{
		"invalid json":      `{"blocks":`,
		"no blocks":         modbusReadBody(mock.Port, "", `[]`),
		"missing count":     modbusReadBody(mock.Port, "", `[{"address":1}]`),
		"count zero":        modbusReadBody(mock.Port, "", `[{"address":1,"count":0}]`),
		"count over 125":    modbusReadBody(mock.Port, "", `[{"address":1,"count":126}]`),
		"negative address":  modbusReadBody(mock.Port, "", `[{"address":-1,"count":1}]`),
		"range past 65535":  modbusReadBody(mock.Port, "", `[{"address":65535,"count":2}]`),
		"coil register":     modbusReadBody(mock.Port, "", `[{"address":1,"count":1,"register_type":"coil"}]`),
		"unit id 248":       modbusReadBody(mock.Port, `,"unit_id":248`, `[{"address":1,"count":1}]`),
		"retries 11":        modbusReadBody(mock.Port, `,"retries":11`, `[{"address":1,"count":1}]`),
		"timeout too small": modbusReadBody(mock.Port, `,"timeout_ms":50`, `[{"address":1,"count":1}]`),
		"too many blocks":   modbusReadBody(mock.Port, "", manyBlocks),
	}
	for name, body := range cases {
		res := a.testPostJSON(t, "/api/modbus/read", body)
		if res.Code != http.StatusBadRequest || res.JSON(t)["ok"] != false {
			t.Errorf("%s: status=%d body=%s, want 400", name, res.Code, res.Body)
		}
	}
	if res := a.testGet(t, "/api/modbus/read"); res.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status=%d, want 405", res.Code)
	}
	if reads, writes := mock.counts(); reads != 0 || writes != 0 {
		t.Fatalf("invalid requests reached the device: reads=%d writes=%d", reads, writes)
	}
}

// The legacy form endpoint must keep its contract: masked write with
// read-modify-write and read-back verification.
func TestLegacyModbusWriteMaskedRegisterPreservesUnrelatedBits(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(146, 0x0100) // bit 8 is not managed by the application

	form := url.Values{"ip": {"127.0.0.1"}, "port": {fmt.Sprint(mock.Port)}, "code": {"use_timer"}, "value": {"255"}, "retries": {"1"}, "retry_delay_seconds": {"0"}}
	res := a.testPostForm(t, "/api/modbus/write", form)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	body := res.JSON(t)
	if body["ok"] != true || body["write_method"] != "fc16_write_readback_verify" {
		t.Fatalf("unexpected body %s", res.Body)
	}
	if got := mock.get(146); got != 0x01FF {
		t.Fatalf("register 146=0x%04X, want 0x01FF (mask 255 applied, bit 8 preserved)", got)
	}
}

func TestLegacyModbusWriteRejectsOutOfBoundsWithoutWriting(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	form := url.Values{"ip": {"127.0.0.1"}, "port": {fmt.Sprint(mock.Port)}, "code": {"use_timer"}, "value": {"256"}, "retries": {"1"}}
	res := a.testPostForm(t, "/api/modbus/write", form)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", res.Code, res.Body)
	}
	if reads, writes := mock.counts(); reads != 0 || writes != 0 {
		t.Fatalf("rejected request reached the device: reads=%d writes=%d", reads, writes)
	}
}

func TestLegacyModbusWriteBlocksGridPeakRegistersWhenFeatureOff(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	if a.gridPeakFeatureEnabled() {
		t.Fatal("grid peak shaving must be disabled on a new installation")
	}
	for _, tc := range []struct {
		form url.Values
		want int
	}{
		{url.Values{"code": {"grid_peak_shaving_power"}, "value": {"5000"}}, http.StatusForbidden},
		{url.Values{"code": {"grid_peak_shaving_enabled"}, "value": {"1"}}, http.StatusForbidden},
		{url.Values{"address": {"191"}, "value": {"500"}}, http.StatusForbidden},
		// A raw write to a masked bitfield is refused even earlier (must use code).
		{url.Values{"address": {"178"}, "value": {"48"}}, http.StatusBadRequest},
	} {
		tc.form.Set("ip", "127.0.0.1")
		tc.form.Set("port", fmt.Sprint(mock.Port))
		tc.form.Set("retries", "1")
		res := a.testPostForm(t, "/api/modbus/write", tc.form)
		if res.Code != tc.want {
			t.Errorf("%v: status=%d body=%s, want %d", tc.form, res.Code, res.Body, tc.want)
		}
	}
	if reads, writes := mock.counts(); reads != 0 || writes != 0 {
		t.Fatalf("blocked writes reached the device: reads=%d writes=%d", reads, writes)
	}
	entries, total, err := a.queryHistory(HistoryFilter{Status: histBlocked, Page: 1, PerPage: 50})
	if err != nil || total != 3 || len(entries) != 3 {
		t.Fatalf("blocked writes must be recorded in history: total=%d err=%v", total, err)
	}
}
