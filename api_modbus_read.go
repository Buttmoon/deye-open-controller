package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// healthHandler implements GET /health from distributor-custom-api.md.
// It does not touch Modbus or the database so it stays fast under load.
func (a *App) healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "deye-open-controller"})
}

type modbusReadRequestBlock struct {
	Address      *int   `json:"address"`
	Count        *int   `json:"count"`
	RegisterType string `json:"register_type"`
}

type modbusReadRequest struct {
	IP         string                   `json:"ip"`
	Port       int                      `json:"port"`
	UnitID     *int                     `json:"unit_id"`
	TimeoutMS  int                      `json:"timeout_ms"`
	Retries    *int                     `json:"retries"`
	InverterID int64                    `json:"inverter_id"`
	Blocks     []modbusReadRequestBlock `json:"blocks"`
}

type modbusReadResponseBlock struct {
	Address      int      `json:"address"`
	Count        int      `json:"count"`
	RegisterType string   `json:"register_type"`
	Values       []uint16 `json:"values,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type modbusReadResponse struct {
	OK         bool                      `json:"ok"`
	Message    string                    `json:"message"`
	Blocks     []modbusReadResponseBlock `json:"blocks"`
	Target     string                    `json:"target,omitempty"`
	DurationMS int64                     `json:"duration_ms"`
}

const (
	modbusReadDefaultTimeoutMS = 5000
	modbusReadDefaultRetries   = 3
	modbusReadMaxBlocks        = 64
)

// modbusReadAPIHandler implements POST /api/modbus/read (JSON).
//
// retries is the number of read attempts per block (minimum 1). Values are
// returned as raw unsigned 16-bit integers in request order.
func (a *App) modbusReadAPIHandler(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, modbusReadResponse{OK: false, Message: "Метод не поддерживается, используйте POST"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, modbusReadResponse{OK: false, Message: "Ошибка чтения тела запроса"})
		return
	}
	if len(body) > 64<<10 {
		writeJSON(w, http.StatusRequestEntityTooLarge, modbusReadResponse{OK: false, Message: "Слишком большой запрос (максимум 64 КБ)"})
		return
	}
	var req modbusReadRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, modbusReadResponse{OK: false, Message: "Некорректный JSON: " + err.Error()})
		return
	}
	blocks, unitID, timeout, attempts, verr := validateModbusReadRequest(req)
	if verr != nil {
		writeJSON(w, http.StatusBadRequest, modbusReadResponse{OK: false, Message: verr.Error()})
		return
	}

	ip, port := strings.TrimSpace(req.IP), req.Port
	if req.InverterID > 0 && ip == "" {
		if err := a.db.QueryRow(`SELECT ip, port FROM inverters WHERE id = ?`, req.InverterID).Scan(&ip, &port); err != nil {
			writeJSON(w, http.StatusBadRequest, modbusReadResponse{OK: false, Message: "Инвертор не найден: " + err.Error()})
			return
		}
	}
	if ip == "" || port <= 0 {
		item, _, err := a.defaultModbusTarget()
		if err != nil && (ip == "" || port <= 0) {
			writeJSON(w, http.StatusBadRequest, modbusReadResponse{OK: false, Message: "IP/Port не указаны и активный инвертор не найден: " + err.Error()})
			return
		}
		if ip == "" {
			ip = item.IP
		}
		if port <= 0 {
			port = item.Port
		}
	}
	if port <= 0 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, modbusReadResponse{OK: false, Message: "Некорректный порт"})
		return
	}

	lockWait := timeout*time.Duration(attempts) + 2*time.Second
	if !a.tryLockModbus(lockWait) {
		writeJSON(w, http.StatusServiceUnavailable, modbusReadResponse{OK: false, Message: errModbusBusy, Target: fmt.Sprintf("%s:%d", ip, port)})
		return
	}
	results := readModbusBlocks(ip, port, unitID, timeout, attempts, 250*time.Millisecond, blocks)
	a.modbusMu.Unlock()

	resp := modbusReadResponse{OK: false, Message: "ok", Blocks: make([]modbusReadResponseBlock, 0, len(results)), Target: fmt.Sprintf("%s:%d", ip, port)}
	failed := 0
	for i, b := range results {
		rb := modbusReadResponseBlock{Address: int(b.Address), Count: int(b.Count), RegisterType: normalizeReadRegisterType(req.Blocks[i].RegisterType)}
		if b.Err != nil {
			rb.Error = classifyModbusError(b.Err)
			failed++
		} else {
			rb.Values = b.Values
		}
		resp.Blocks = append(resp.Blocks, rb)
	}
	resp.DurationMS = time.Since(started).Milliseconds()
	status := http.StatusOK
	switch {
	case failed == 0:
		resp.OK = true
	case failed < len(results):
		resp.OK = true
		resp.Message = fmt.Sprintf("ok: прочитано блоков %d из %d", len(results)-failed, len(results))
	default:
		resp.Message = "Ни один блок не прочитан: " + resp.Blocks[0].Error
		status = http.StatusBadGateway
		a.appendAppLog("warn", "modbus read api: all blocks failed", map[string]any{"component": "api_modbus_read", "target": resp.Target, "error": resp.Blocks[0].Error, "duration_ms": resp.DurationMS})
	}
	writeJSON(w, status, resp)
}

func normalizeReadRegisterType(raw string) string {
	_, name, err := registerTypeFromString(raw)
	if err != nil {
		return raw
	}
	return name
}

func validateModbusReadRequest(req modbusReadRequest) ([]modbusBlockRead, uint8, time.Duration, int, error) {
	if len(req.Blocks) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("blocks: нужен хотя бы один блок чтения")
	}
	if len(req.Blocks) > modbusReadMaxBlocks {
		return nil, 0, 0, 0, fmt.Errorf("blocks: не более %d блоков за запрос", modbusReadMaxBlocks)
	}
	unit := 1
	if req.UnitID != nil {
		unit = *req.UnitID
	}
	if unit < 0 || unit > modbusMaxUnitID {
		return nil, 0, 0, 0, fmt.Errorf("unit_id должен быть 0–%d", modbusMaxUnitID)
	}
	timeoutMS := req.TimeoutMS
	if timeoutMS == 0 {
		timeoutMS = modbusReadDefaultTimeoutMS
	}
	if timeoutMS < 100 || timeoutMS > 60000 {
		return nil, 0, 0, 0, fmt.Errorf("timeout_ms должен быть 100–60000")
	}
	attempts := modbusReadDefaultRetries
	if req.Retries != nil {
		attempts = *req.Retries
	}
	if attempts < 0 || attempts > 10 {
		return nil, 0, 0, 0, fmt.Errorf("retries должен быть 0–10")
	}
	if attempts == 0 {
		attempts = 1
	}
	out := make([]modbusBlockRead, 0, len(req.Blocks))
	for i, b := range req.Blocks {
		if b.Address == nil || b.Count == nil {
			return nil, 0, 0, 0, fmt.Errorf("blocks[%d]: address и count обязательны", i)
		}
		if *b.Address < 0 || *b.Address > 65535 {
			return nil, 0, 0, 0, fmt.Errorf("blocks[%d]: address должен быть 0–65535", i)
		}
		if *b.Count < 1 || *b.Count > modbusMaxReadRegisters {
			return nil, 0, 0, 0, fmt.Errorf("blocks[%d]: count должен быть 1–%d (ограничение Modbus)", i, modbusMaxReadRegisters)
		}
		if *b.Address+*b.Count-1 > 65535 {
			return nil, 0, 0, 0, fmt.Errorf("blocks[%d]: диапазон выходит за адрес 65535", i)
		}
		regType, _, err := registerTypeFromString(b.RegisterType)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("blocks[%d]: %v", i, err)
		}
		out = append(out, modbusBlockRead{Address: uint16(*b.Address), Count: uint16(*b.Count), RegisterType: regType})
	}
	return out, uint8(unit), time.Duration(timeoutMS) * time.Millisecond, attempts, nil
}
