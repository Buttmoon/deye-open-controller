package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func previewThenWrite(t *testing.T, a *App, req map[string]any) (preview map[string]any, write testResponse) {
	t.Helper()
	res := a.testPostJSON(t, "/api/registers/write-preview", req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", res.Code, res.Body)
	}
	preview = res.JSON(t)
	confirmed := map[string]any{}
	for k, v := range req {
		confirmed[k] = v
	}
	confirmed["confirm"] = true
	confirmed["confirm_address"] = preview["address"]
	confirmed["expected_current"] = preview["expected_current"]
	confirmed["preview_token"] = preview["preview_token"]
	return preview, a.testPostJSON(t, "/api/registers/write", confirmed)
}

func TestRegisterReadDecodesRegister146AsBitmask(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(146, 0x01FF)
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")

	res := a.testPostJSON(t, "/api/registers/read", map[string]any{"inverter_id": id, "code": "use_timer"})
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body)
	}
	var body struct {
		Values []struct {
			Raw    int `json:"raw"`
			Fields []struct {
				Code    string               `json:"code"`
				Kind    string               `json:"kind"`
				Decoded DecodedRegisterValue `json:"decoded"`
			} `json:"fields"`
		} `json:"values"`
	}
	if err := json.Unmarshal(res.Body, &body); err != nil || len(body.Values) != 1 {
		t.Fatalf("unexpected body %s", res.Body)
	}
	var timer *DecodedRegisterValue
	for _, f := range body.Values[0].Fields {
		if f.Code == "use_timer" {
			if f.Kind != kindBitmask {
				t.Fatalf("use_timer kind=%s, want bitmask", f.Kind)
			}
			d := f.Decoded
			timer = &d
		}
	}
	if timer == nil {
		t.Fatalf("use_timer not decoded: %s", res.Body)
	}
	if len(timer.Violations) != 0 {
		t.Fatalf("value 0x01FF must not be reported as a violation: %v", timer.Violations)
	}
	on := map[int]bool{}
	for _, b := range timer.Bits {
		on[b.Bit] = b.Value == 1
	}
	for bit := 0; bit <= 8; bit++ {
		if !on[bit] {
			t.Fatalf("bit %d not decoded as set: %+v", bit, timer.Bits)
		}
	}
	if _, writes := mock.counts(); writes != 0 {
		t.Fatal("read performed a write")
	}
}

func TestRegisterWritePreviewDoesNotWriteAndConfirmedWriteIsVerified(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(146, 0x0100)
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")
	req := map[string]any{"inverter_id": id, "code": "use_timer", "mode": "logical", "value": 255}

	res := a.testPostJSON(t, "/api/registers/write-preview", req)
	if res.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", res.Code, res.Body)
	}
	preview := res.JSON(t)
	if preview["current_raw"].(float64) != 0x0100 || preview["target_raw"].(float64) != 0x01FF || preview["mask"].(float64) != 255 {
		t.Fatalf("unexpected preview %s", res.Body)
	}
	if _, writes := mock.counts(); writes != 0 {
		t.Fatal("preview wrote to the device")
	}

	unconfirmed := a.testPostJSON(t, "/api/registers/write", req)
	if unconfirmed.Code != http.StatusPreconditionRequired {
		t.Fatalf("write without confirmation: status=%d, want 428", unconfirmed.Code)
	}
	wrongAddress := map[string]any{"inverter_id": id, "code": "use_timer", "mode": "logical", "value": 255, "confirm": true,
		"confirm_address": 145, "expected_current": preview["expected_current"], "preview_token": preview["preview_token"]}
	if res := a.testPostJSON(t, "/api/registers/write", wrongAddress); res.Code != http.StatusPreconditionRequired {
		t.Fatalf("wrong confirmation address: status=%d, want 428", res.Code)
	}
	changedValue := map[string]any{"inverter_id": id, "code": "use_timer", "mode": "logical", "value": 1, "confirm": true,
		"confirm_address": 146, "expected_current": preview["expected_current"], "preview_token": preview["preview_token"]}
	if res := a.testPostJSON(t, "/api/registers/write", changedValue); res.Code != http.StatusConflict {
		t.Fatalf("value differing from preview: status=%d, want 409", res.Code)
	}
	if _, writes := mock.counts(); writes != 0 {
		t.Fatal("unconfirmed requests wrote to the device")
	}

	_, write := previewThenWrite(t, a, req)
	if write.Code != http.StatusOK {
		t.Fatalf("confirmed write status=%d body=%s", write.Code, write.Body)
	}
	body := write.JSON(t)
	if body["ok"] != true || body["status"] != histVerified {
		t.Fatalf("unexpected write result %s", write.Body)
	}
	if got := mock.get(146); got != 0x01FF {
		t.Fatalf("register 146=0x%04X, want 0x01FF", got)
	}
	_, writes := mock.counts()
	if writes != 1 {
		t.Fatalf("device received %d writes, want exactly 1", writes)
	}

	entries, _, err := a.queryHistory(HistoryFilter{OperationID: body["operation_id"].(string), Page: 1, PerPage: 50})
	if err != nil || len(entries) == 0 {
		t.Fatalf("history for operation missing: %v", err)
	}
	children, _, _ := a.queryHistory(HistoryFilter{OperationType: opRegisterReadback, Page: 1, PerPage: 50})
	if len(children) != 1 || children[0].Status != histVerified || children[0].VerifiedValue == nil || *children[0].VerifiedValue != 0x01FF {
		t.Fatalf("read-back entry missing or wrong: %+v", children)
	}
}

func TestRegisterWriteAbortsWhenValueChangedSincePreview(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(146, 0x0001)
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")
	req := map[string]any{"inverter_id": id, "code": "use_timer", "mode": "logical", "value": 255}
	preview := a.testPostJSON(t, "/api/registers/write-preview", req).JSON(t)

	mock.set(146, 0x0003) // someone else changed the register
	req["confirm"], req["confirm_address"], req["expected_current"], req["preview_token"] = true, 146, preview["expected_current"], preview["preview_token"]
	res := a.testPostJSON(t, "/api/registers/write", req)
	if res.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", res.Code, res.Body)
	}
	if _, writes := mock.counts(); writes != 0 {
		t.Fatal("write must be cancelled when the register changed after preview")
	}
	if mock.get(146) != 0x0003 {
		t.Fatal("register value was modified")
	}
}

func TestRegisterWriteNotAcknowledgedByReadBackIsUnverified(t *testing.T) {
	a := newTestApp(t)
	if err := a.kvSet(kvFeatureGridPeakShaving, "1"); err != nil {
		t.Fatal(err)
	}
	mock := newMockInverter(t)
	mock.set(191, 100)
	mock.ignoreWrites[191] = true
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")

	_, res := previewThenWrite(t, a, map[string]any{"inverter_id": id, "code": "grid_peak_shaving_power", "mode": "logical", "value": 5000})
	if res.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s, want 502", res.Code, res.Body)
	}
	body := res.JSON(t)
	if body["ok"] != false || body["status"] != histUnverified {
		t.Fatalf("a write that read-back does not confirm must be 'unverified': %s", res.Body)
	}
	verified, _, _ := a.queryHistory(HistoryFilter{Status: histVerified, Page: 1, PerPage: 50})
	if len(verified) != 0 {
		t.Fatalf("history must not contain 'verified' entries: %+v", verified)
	}
	var stored *int
	_ = a.db.QueryRow(`SELECT grid_peak_shaving_power FROM inverters WHERE id = ?`, id).Scan(&stored)
	if stored != nil {
		t.Fatalf("unverified write must not update the stored grid peak power, got %d", *stored)
	}
}

func TestRegisterWriteGridPeakRequiresFeatureFlag(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(178, 0x0010)
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")
	for _, req := range []map[string]any{
		{"inverter_id": id, "code": "grid_peak_shaving_enabled", "mode": "logical", "value": 1},
		{"inverter_id": id, "code": "grid_peak_shaving_power", "mode": "logical", "value": 5000},
		{"inverter_id": id, "address": 191, "mode": "raw", "value": 500},
	} {
		if res := a.testPostJSON(t, "/api/registers/write-preview", req); res.Code != http.StatusForbidden {
			t.Errorf("%v: status=%d, want 403 while the feature is disabled", req, res.Code)
		}
	}
	if reads, writes := mock.counts(); reads != 0 || writes != 0 {
		t.Fatalf("blocked requests reached the device: reads=%d writes=%d", reads, writes)
	}

	if err := a.kvSet(kvFeatureGridPeakShaving, "1"); err != nil {
		t.Fatal(err)
	}
	mock.set(178, 0x0F0F)
	preview, res := previewThenWrite(t, a, map[string]any{"inverter_id": id, "code": "grid_peak_shaving_enabled", "mode": "logical", "value": 1})
	if preview["mask"].(float64) != 0x30 {
		t.Fatalf("register 178 must be written with mask 0x30, preview=%v", preview)
	}
	if res.Code != http.StatusOK {
		t.Fatalf("enabled feature: status=%d body=%s", res.Code, res.Body)
	}
	if got := mock.get(178); got != 0x0F3F {
		t.Fatalf("register 178=0x%04X, want 0x0F3F (bits 4–5 = 11, other bits preserved)", got)
	}
}

func TestRegisterWriteBitEditorOnlyTouchesDocumentedWritableBits(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	mock.set(146, 0x0101)
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")

	for bits, want := range map[string]int{`{"8":0}`: http.StatusForbidden, `{"9":1}`: http.StatusBadRequest, `{"3":2}`: http.StatusBadRequest} {
		var m map[string]int
		_ = json.Unmarshal([]byte(bits), &m)
		res := a.testPostJSON(t, "/api/registers/write-preview", map[string]any{"inverter_id": id, "code": "use_timer", "mode": "bits", "bits": m})
		if res.Code != want {
			t.Errorf("bits %s: status=%d body=%s, want %d", bits, res.Code, res.Body, want)
		}
	}
	if reads, writes := mock.counts(); reads != 0 || writes != 0 {
		t.Fatalf("rejected bit edits reached the device: reads=%d writes=%d", reads, writes)
	}

	preview, res := previewThenWrite(t, a, map[string]any{"inverter_id": id, "code": "use_timer", "mode": "bits", "bits": map[string]int{"3": 1}})
	if preview["mask"].(float64) != 0x08 || preview["target_raw"].(float64) != 0x0109 {
		t.Fatalf("unexpected preview %v", preview)
	}
	if res.Code != http.StatusOK || mock.get(146) != 0x0109 {
		t.Fatalf("bit write failed: status=%d register=0x%04X", res.Code, mock.get(146))
	}
}

func TestRegisterWriteRefusesRawModeForMaskedFieldAndReadOnlyAddress(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")
	cases := []struct {
		req  map[string]any
		want int
		text string
	}{
		{map[string]any{"inverter_id": id, "code": "use_timer", "mode": "raw", "value": 255}, http.StatusBadRequest, "masked_bits"},
		{map[string]any{"inverter_id": id, "code": "prog_monday_enabled", "mode": "logical", "value": 1}, http.StatusForbidden, ""},
		{map[string]any{"inverter_id": id, "address": 60000, "mode": "raw", "value": 1}, http.StatusBadRequest, "отсутствует в профиле"},
	}
	for _, tc := range cases {
		res := a.testPostJSON(t, "/api/registers/write-preview", tc.req)
		if res.Code != tc.want || !strings.Contains(string(res.Body), tc.text) {
			t.Errorf("%v: status=%d body=%s, want %d containing %q", tc.req, res.Code, res.Body, tc.want, tc.text)
		}
	}
	if reads, writes := mock.counts(); reads != 0 || writes != 0 {
		t.Fatalf("rejected requests reached the device: reads=%d writes=%d", reads, writes)
	}
}

func TestRegisterWritesCanBeDisabledInSettings(t *testing.T) {
	a := newTestApp(t)
	mock := newMockInverter(t)
	id := a.testAddInverter(t, "mock", "127.0.0.1", mock.Port, "")
	if err := a.kvSet(kvFeatureRegisterTestWrites, "0"); err != nil {
		t.Fatal(err)
	}
	res := a.testPostJSON(t, "/api/registers/write-preview", map[string]any{"inverter_id": id, "code": "use_timer", "mode": "logical", "value": 1})
	if res.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", res.Code)
	}
	if reads, _ := mock.counts(); reads != 0 {
		t.Fatal("disabled writes must not even read the device")
	}
}
