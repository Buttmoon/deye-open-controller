package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/simonvetter/modbus"
)

type CustomModbusWriteAttempt struct {
	Attempt    int    `json:"attempt"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type CustomModbusWriteRequest struct {
	IP                string  `json:"ip"`
	Port              int     `json:"port"`
	Code              string  `json:"code,omitempty"`
	Address           uint16  `json:"address"`
	Value             uint16  `json:"value"`
	Retries           int     `json:"retries"`
	TimeoutMS         int     `json:"timeout_ms"`
	RetryDelaySeconds int     `json:"retry_delay_seconds"`
	TargetSource      string  `json:"target_source"`
	InverterID        int64   `json:"inverter_id,omitempty"`
	ModelKey          string  `json:"model_key,omitempty"`
	ModelName         string  `json:"model_name,omitempty"`
	LogicalValue      float64 `json:"logical_value,omitempty"`
	WriteMode         string  `json:"write_mode,omitempty"`
	WriteBitmask      uint16  `json:"write_bitmask,omitempty"`
}

type CustomModbusWriteResponse struct {
	OK          bool                       `json:"ok"`
	Message     string                     `json:"message"`
	Request     CustomModbusWriteRequest   `json:"request"`
	Attempts    []CustomModbusWriteAttempt `json:"attempts"`
	Result      string                     `json:"result"`
	WriteMethod string                     `json:"write_method"`
}

func (a *App) customModbusWriteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}

	ip := strings.TrimSpace(r.FormValue("ip"))
	port := parseIntDefault(r.FormValue("port"), 0)
	inverterID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("inverter_id")), 10, 64)
	modelKey := strings.TrimSpace(r.FormValue("model_key"))
	code := strings.ToLower(strings.TrimSpace(r.FormValue("code")))
	addressRaw := strings.TrimSpace(r.FormValue("address"))
	valueRaw := strings.TrimSpace(r.FormValue("value"))
	retries := parseIntDefault(r.FormValue("retries"), 5)
	timeoutMS := parseIntDefault(r.FormValue("timeout_ms"), 5000)
	retryDelaySeconds := parseIntDefault(r.FormValue("retry_delay_seconds"), 2)

	if retries < 1 {
		retries = 1
	}
	if retries > 20 {
		retries = 20
	}
	if timeoutMS < 500 {
		timeoutMS = 500
	}
	if timeoutMS > 60000 {
		timeoutMS = 60000
	}
	if retryDelaySeconds < 0 {
		retryDelaySeconds = 0
	}
	if retryDelaySeconds > 60 {
		retryDelaySeconds = 60
	}

	targetSource := "custom"
	var selectedTarget runtimeScheduledItem
	var profileWriteConfirmed int
	if inverterID > 0 {
		err := a.db.QueryRow(`SELECT id, name, ip, port, model_key, COALESCE(profile_write_confirmed, 0) FROM inverters WHERE id = ?`, inverterID).Scan(
			&selectedTarget.InverterID, &selectedTarget.InverterName, &selectedTarget.IP, &selectedTarget.Port, &selectedTarget.ModelKey, &profileWriteConfirmed)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "Инвертор не найден: " + err.Error()})
			return
		}
		selectedTarget.ProfileWriteConfirmed = profileWriteConfirmed == 1
		targetSource = "inverter_id"
	} else if ip != "" && port > 0 {
		profileWriteConfirmed = 0
		_ = a.db.QueryRow(`SELECT id, name, ip, port, model_key, COALESCE(profile_write_confirmed, 0) FROM inverters WHERE ip = ? AND port = ? LIMIT 1`, ip, port).Scan(
			&selectedTarget.InverterID, &selectedTarget.InverterName, &selectedTarget.IP, &selectedTarget.Port, &selectedTarget.ModelKey, &profileWriteConfirmed)
		selectedTarget.ProfileWriteConfirmed = profileWriteConfirmed == 1
	}
	if selectedTarget.InverterID == 0 && (ip == "" || port <= 0 || modelKey == "") {
		item, source, err := a.defaultModbusTarget()
		if err != nil && (ip == "" || port <= 0) {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "IP/Port не указаны и активный инвертор не найден: " + err.Error()})
			return
		}
		if err == nil {
			selectedTarget = item
			targetSource = source
		}
	}
	if ip == "" {
		ip = selectedTarget.IP
	}
	if port <= 0 {
		port = selectedTarget.Port
	}
	if modelKey == "" {
		modelKey = selectedTarget.ModelKey
	}
	if inverterID == 0 {
		inverterID = selectedTarget.InverterID
	}
	modelKey = normalizeInverterModelKey(modelKey)
	model, err := findInverterModel(modelKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: err.Error()})
		return
	}
	if model.WriteRequiresConfirm && (selectedTarget.InverterID == 0 || !selectedTarget.ProfileWriteConfirmed) {
		writeJSON(w, http.StatusForbidden, CustomModbusWriteResponse{OK: false, Message: "Запись заблокирована: выбранный профиль требует read-only проверки и явного подтверждения в настройках инвертора"})
		return
	}
	if port <= 0 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "Некорректный порт"})
		return
	}

	var address uint16
	var value uint16
	logicalValue := 0.0
	writeMode := ""
	var writeBitmask uint16
	if addressRaw != "" {
		parsedAddress, err := strconv.ParseUint(addressRaw, 10, 16)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "Некорректный адрес регистра"})
			return
		}
		parsedValue, err := strconv.ParseUint(valueRaw, 10, 16)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "Для записи по адресу нужно raw-значение 0..65535"})
			return
		}
		address = uint16(parsedAddress)
		value = uint16(parsedValue)
		logicalValue = float64(value)
		_, params, profileErr := loadDeviceParametersForModel(model.Key)
		if profileErr != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "Профиль модели не прошёл проверку: " + profileErr.Error()})
			return
		}
		matchedCode, decoded, matched, validationErr := validateRawProfileWrite(params, address, value)
		if validationErr != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: validationErr.Error()})
			return
		}
		if matched {
			logicalValue = decoded
			code = matchedCode
		}
	} else if code != "" {
		_, field, err := findDeviceParameterByCodeForModel(modelKey, code)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: err.Error()})
			return
		}
		if strings.ToLower(field.RegisterType) != "holding" || !field.IsWritable {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: fmt.Sprintf("code=%s address=%d: параметр должен быть writable holding", code, field.ModbusAddress)})
			return
		}
		logicalValue, err = strconv.ParseFloat(valueRaw, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "Некорректное логическое значение"})
			return
		}
		value, err = encodeDeviceParameterValue(logicalValue, field)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: fmt.Sprintf("code=%s: %v", code, err)})
			return
		}
		writeMode = strings.ToLower(strings.TrimSpace(field.WriteMode))
		if writeMode == "mapped_masked_bits" {
			rounded := int64(logicalValue)
			if float64(rounded) != logicalValue {
				writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: fmt.Sprintf("code=%s: mapped bitfield принимает только целое логическое значение", code)})
				return
			}
			mapped, ok := field.WriteValues[strconv.FormatInt(rounded, 10)]
			if !ok {
				writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: fmt.Sprintf("code=%s: нет безопасного write_values для значения %.0f", code, logicalValue)})
				return
			}
			value = mapped
		}
		if writeMode == "masked_bits" || writeMode == "mapped_masked_bits" {
			if field.WriteBitmask == nil || *field.WriteBitmask == 0 {
				writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: fmt.Sprintf("code=%s: профиль не содержит write_bitmask", code)})
				return
			}
			writeBitmask = *field.WriteBitmask
		}
		address = field.ModbusAddress
	} else {
		writeJSON(w, http.StatusBadRequest, CustomModbusWriteResponse{OK: false, Message: "Укажи address или code"})
		return
	}

	profileParams, _ := LoadDeviceParameters(model.ParametersFile)
	if blocked, msg := a.gridPeakRegisterBlocked(profileParams, address); blocked {
		a.recordHistory(HistoryEntry{OperationType: opRegisterWrite, Status: histBlocked, InverterID: inverterID, Initiator: requestInitiator(r),
			RegisterAddress: intPtr(int(address)), RegisterCode: code, RequestedValue: intPtr(int(value)), Message: "Запись через /api/modbus/write заблокирована", Error: msg})
		writeJSON(w, http.StatusForbidden, CustomModbusWriteResponse{OK: false, Message: msg})
		return
	}

	req := CustomModbusWriteRequest{
		IP:                ip,
		Port:              port,
		Code:              code,
		Address:           address,
		Value:             value,
		Retries:           retries,
		TimeoutMS:         timeoutMS,
		RetryDelaySeconds: retryDelaySeconds,
		TargetSource:      targetSource,
		InverterID:        inverterID,
		ModelKey:          model.Key,
		ModelName:         model.Name,
		LogicalValue:      logicalValue,
		WriteMode:         writeMode,
		WriteBitmask:      writeBitmask,
	}

	started := time.Now()
	resp := a.runCustomModbusWrite(req)
	histStatus := histError
	if resp.OK {
		histStatus = histVerified
	}
	opID := a.recordHistory(HistoryEntry{OperationType: opRegisterWrite, Status: histStatus, InverterID: inverterID, Endpoint: fmt.Sprintf("%s:%d", ip, port),
		Initiator: requestInitiator(r), RegisterAddress: intPtr(int(address)), RegisterCode: code, RequestedValue: intPtr(int(value)),
		Attempt: len(resp.Attempts), DurationMS: time.Since(started).Milliseconds(), Message: "Запись через /api/modbus/write (FC16 + контрольное чтение)",
		Error: map[bool]string{true: "", false: resp.Message}[resp.OK], Details: map[string]any{"write_mode": writeMode, "write_bitmask": writeBitmask, "logical_value": logicalValue, "model_key": model.Key}})
	for _, att := range resp.Attempts {
		if len(resp.Attempts) < 2 {
			break
		}
		st := histError
		if att.Status == "ok" {
			st = histVerified
		}
		a.recordHistory(HistoryEntry{ParentID: opID, OperationType: opRetryAttempt, Status: st, InverterID: inverterID, RegisterAddress: intPtr(int(address)), RegisterCode: code,
			Attempt: att.Attempt, DurationMS: att.DurationMS, Error: att.Error, Message: fmt.Sprintf("Попытка %d", att.Attempt)})
	}
	status := http.StatusOK
	if !resp.OK {
		status = http.StatusBadRequest
	} else if req.InverterID > 0 {
		switch req.Code {
		case "grid_peak_shaving_power":
			powerW := int(req.LogicalValue)
			if err := a.saveStoredGridPeakState(req.InverterID, &powerW, nil); err != nil {
				resp.OK = false
				resp.Message = "Значение записано в инвертор, но не сохранено в приложении: " + err.Error()
				status = http.StatusInternalServerError
			}
		case "grid_peak_shaving_enabled":
			enabled := req.LogicalValue == 1
			if err := a.saveStoredGridPeakState(req.InverterID, nil, &enabled); err != nil {
				resp.OK = false
				resp.Message = "Состояние записано в инвертор, но не сохранено в приложении: " + err.Error()
				status = http.StatusInternalServerError
			}
		}
	}
	writeJSON(w, status, resp)
}

func validateRawProfileWrite(params []DeviceParameterFixture, address, value uint16) (string, float64, bool, error) {
	active := make([]DeviceParameterFields, 0, 2)
	writable := make([]DeviceParameterFields, 0, 1)
	for _, fixture := range params {
		field := fixture.Fields
		if !field.IsActive || !strings.EqualFold(field.RegisterType, "holding") || field.ModbusAddress != address {
			continue
		}
		active = append(active, field)
		if field.IsWritable {
			writable = append(writable, field)
		}
	}
	if len(active) == 0 {
		// Unknown raw addresses remain available for engineering diagnostics, but
		// known read-only or masked registers can no longer be bypassed by address.
		return "", float64(value), false, nil
	}
	if len(writable) == 0 {
		codes := make([]string, 0, len(active))
		for _, field := range active {
			codes = append(codes, field.Code)
		}
		return "", 0, true, fmt.Errorf("address=%d относится к read-only параметру профиля (%s); raw-запись заблокирована", address, strings.Join(codes, ", "))
	}
	if len(writable) != 1 {
		return "", 0, true, fmt.Errorf("address=%d имеет несколько writable-описаний в профиле; запись заблокирована", address)
	}
	field := writable[0]
	if strings.TrimSpace(field.WriteMode) != "" {
		return "", 0, true, fmt.Errorf("address=%d относится к code=%s с безопасным режимом %s; используйте запись по code, чтобы сохранить остальные биты", address, field.Code, field.WriteMode)
	}
	decoded, err := decodeDeviceParameterRegisters([]uint16{value}, field)
	if err != nil {
		return "", 0, true, fmt.Errorf("address=%d code=%s: raw не удалось проверить: %v", address, field.Code, err)
	}
	encoded, err := encodeDeviceParameterValue(decoded, field)
	if err != nil || encoded != value {
		if err == nil {
			err = fmt.Errorf("канонический raw=%d", encoded)
		}
		return "", 0, true, fmt.Errorf("address=%d code=%s: raw=%d выходит за ограничения профиля: %v", address, field.Code, value, err)
	}
	return strings.ToLower(strings.TrimSpace(field.Code)), decoded, true, nil
}

func (a *App) runCustomModbusWrite(req CustomModbusWriteRequest) CustomModbusWriteResponse {
	started := time.Now()
	url := fmt.Sprintf("tcp://%s:%d", req.IP, req.Port)
	resp := CustomModbusWriteResponse{
		OK:          false,
		Message:     "Запись Modbus не выполнена",
		Request:     req,
		WriteMethod: "fc16_write_readback_verify",
		Attempts:    make([]CustomModbusWriteAttempt, 0, req.Retries),
	}

	a.appendAppLog("info", "custom modbus write requested", map[string]any{
		"component":     "custom_modbus",
		"ip":            req.IP,
		"port":          req.Port,
		"url":           url,
		"code":          req.Code,
		"address":       req.Address,
		"value":         req.Value,
		"retries":       req.Retries,
		"timeout_ms":    req.TimeoutMS,
		"retry_delay_s": req.RetryDelaySeconds,
		"target_source": req.TargetSource,
		"write_method":  "fc16_write_multiple_registers",
	})

	a.modbusMu.Lock()
	defer a.modbusMu.Unlock()

	var lastErr error
	for attempt := 1; attempt <= req.Retries; attempt++ {
		attemptStarted := time.Now()
		attemptResult := CustomModbusWriteAttempt{Attempt: attempt, Status: "started"}
		client, err := modbus.NewClient(&modbus.ClientConfiguration{URL: url, Timeout: time.Duration(req.TimeoutMS) * time.Millisecond})
		if err != nil {
			lastErr = err
			attemptResult.Status = "error"
			attemptResult.Error = modbusRegisterError(nonEmptyCode(req.Code), req.Address, err)
			attemptResult.DurationMS = time.Since(attemptStarted).Milliseconds()
			resp.Attempts = append(resp.Attempts, attemptResult)
			a.appendAppLog("warn", "custom modbus client creation failed", map[string]any{"component": "custom_modbus", "attempt": attempt, "address": req.Address, "code": req.Code, "error": attemptResult.Error, "duration_ms": attemptResult.DurationMS})
		} else {
			openErr := client.Open()
			if openErr != nil {
				lastErr = openErr
				attemptResult.Status = "error"
				attemptResult.Error = modbusRegisterError(nonEmptyCode(req.Code), req.Address, openErr)
				attemptResult.DurationMS = time.Since(attemptStarted).Milliseconds()
				resp.Attempts = append(resp.Attempts, attemptResult)
				a.appendAppLog("warn", "custom modbus open failed", map[string]any{"component": "custom_modbus", "attempt": attempt, "address": req.Address, "code": req.Code, "error": attemptResult.Error, "duration_ms": attemptResult.DurationMS})
			} else {
				writeValue := req.Value
				if req.WriteMode == "masked_bits" || req.WriteMode == "mapped_masked_bits" {
					if req.WriteBitmask == 0 {
						lastErr = fmt.Errorf("write_bitmask is required for %s", req.WriteMode)
						attemptResult.Status = "error"
						attemptResult.Error = lastErr.Error()
						attemptResult.DurationMS = time.Since(attemptStarted).Milliseconds()
						resp.Attempts = append(resp.Attempts, attemptResult)
						_ = client.Close()
						continue
					}
					current, readErr := client.ReadRegister(req.Address, modbus.HOLDING_REGISTER)
					if readErr != nil {
						lastErr = fmt.Errorf("read-before-write failed: %w", readErr)
						attemptResult.Status = "error"
						attemptResult.Error = modbusRegisterError(nonEmptyCode(req.Code), req.Address, lastErr)
						attemptResult.DurationMS = time.Since(attemptStarted).Milliseconds()
						resp.Attempts = append(resp.Attempts, attemptResult)
						_ = client.Close()
						continue
					}
					writeValue = mergeMaskedRegisterValue(current, req.Value, req.WriteBitmask)
				}
				// Deye register writes are deliberately paced. The scheduler uses the
				// same two-second interval and five read-back attempts.
				time.Sleep(schedulerNextRegisterDelay)
				writeErr := client.WriteRegisters(req.Address, []uint16{writeValue})
				if writeErr == nil {
					var actual uint16
					for verifyAttempt := 1; verifyAttempt <= schedulerRegisterVerifyRetries; verifyAttempt++ {
						actual, writeErr = client.ReadRegister(req.Address, modbus.HOLDING_REGISTER)
						if writeErr == nil && actual == writeValue {
							break
						}
						if writeErr == nil {
							writeErr = fmt.Errorf("read-back mismatch: expected=%d actual=%d", writeValue, actual)
						}
						if verifyAttempt < schedulerRegisterVerifyRetries {
							time.Sleep(schedulerRetryDelay)
						}
					}
				}
				_ = client.Close()
				attemptResult.DurationMS = time.Since(attemptStarted).Milliseconds()
				if writeErr == nil {
					attemptResult.Status = "ok"
					resp.Attempts = append(resp.Attempts, attemptResult)
					resp.OK = true
					resp.Message = fmt.Sprintf("OK: записано и подтверждено value=%d в address=%d", writeValue, req.Address)
					resp.Result = "WriteRegisters/FC16 + read-back успешно выполнены"
					a.appendAppLog("info", "custom modbus write ok", map[string]any{"component": "custom_modbus", "attempt": attempt, "address": req.Address, "code": req.Code, "requested_value": req.Value, "written_value": writeValue, "write_mode": req.WriteMode, "duration_ms": attemptResult.DurationMS, "total_duration_ms": time.Since(started).Milliseconds()})
					return resp
				}
				lastErr = writeErr
				attemptResult.Status = "error"
				attemptResult.Error = modbusRegisterError(nonEmptyCode(req.Code), req.Address, writeErr)
				resp.Attempts = append(resp.Attempts, attemptResult)
				a.appendAppLog("warn", "custom modbus write failed", map[string]any{"component": "custom_modbus", "attempt": attempt, "address": req.Address, "code": req.Code, "value": req.Value, "error": attemptResult.Error, "duration_ms": attemptResult.DurationMS})
			}
			_ = client.Close()
		}
		if attempt < req.Retries && req.RetryDelaySeconds > 0 {
			time.Sleep(time.Duration(req.RetryDelaySeconds) * time.Second)
		}
	}

	if lastErr != nil {
		resp.Message = modbusRegisterError(nonEmptyCode(req.Code), req.Address, lastErr)
		resp.Result = resp.Message
	}
	a.appendAppLog("error", "custom modbus write failed after all attempts", map[string]any{"component": "custom_modbus", "address": req.Address, "code": req.Code, "value": req.Value, "error": resp.Message, "total_duration_ms": time.Since(started).Milliseconds()})
	return resp
}

func mergeMaskedRegisterValue(current, desired, mask uint16) uint16 {
	return (current &^ mask) | (desired & mask)
}

func nonEmptyCode(code string) string {
	if strings.TrimSpace(code) == "" {
		return "custom"
	}
	return code
}

func parseIntDefault(raw string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return v
}

func (a *App) defaultModbusTarget() (runtimeScheduledItem, string, error) {
	items, err := a.getScheduledInvertersForExecution()
	if err == nil && len(items) > 0 {
		return items[0], "first_enabled_schedule", nil
	}
	var item runtimeScheduledItem
	var profileWriteConfirmed int
	err = a.db.QueryRow(`SELECT id, name, ip, port, model_key, COALESCE(profile_write_confirmed, 0) FROM inverters ORDER BY id ASC LIMIT 1`).Scan(&item.InverterID, &item.InverterName, &item.IP, &item.Port, &item.ModelKey, &profileWriteConfirmed)
	item.ProfileWriteConfirmed = profileWriteConfirmed == 1
	if err != nil {
		return item, "", err
	}
	model, modelErr := findInverterModel(item.ModelKey)
	if modelErr != nil {
		return item, "", modelErr
	}
	item.ModelKey, item.ModelName, item.ParametersFile = model.Key, model.Name, model.ParametersFile
	return item, "first_inverter", nil
}

func findDeviceParameterByCode(code string) (DeviceParameterFields, error) {
	_, field, err := findDeviceParameterByCodeForModel(defaultInverterModelKey, code)
	return field, err
}

func prettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
