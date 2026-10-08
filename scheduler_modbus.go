package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/simonvetter/modbus"
)

type ScheduleWriteResult struct {
	Code           string `json:"code"`
	Address        uint16 `json:"address"`
	Value          uint16 `json:"value"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	RegisterType   string `json:"register_type,omitempty"`
	Writable       bool   `json:"writable"`
	WriteOrder     int    `json:"write_order,omitempty"`
	Attempts       int    `json:"attempts,omitempty"`
	Verified       bool   `json:"verified"`
	ActualValue    uint16 `json:"actual_value"`
	BatchStart     uint16 `json:"batch_start,omitempty"`
	BatchCount     int    `json:"batch_count,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Note           string `json:"note,omitempty"`
	RequestedValue int    `json:"requested_value,omitempty"`
}

type ScheduleRegisterValue struct {
	Code  string `json:"code"`
	Value int    `json:"value"`
	Note  string `json:"note,omitempty"`
}

type ScheduleWriteSummary struct {
	Total   int `json:"total"`
	OK      int `json:"ok"`
	Error   int `json:"error"`
	Skipped int `json:"skipped"`
}

type scheduleResolvedWrite struct {
	Code           string
	Address        uint16
	Value          uint16
	RequestedValue int
	RegisterType   string
	Writable       bool
	WriteMode      string
	WriteBitmask   uint16
	Note           string
	Phase          string
	OriginalOrder  int
}

type scheduleRegisterBatch struct {
	Start uint16
	Items []scheduleResolvedWrite
}

func clampScheduleRegisterValue(value int) uint16 {
	if value < 0 {
		return 0
	}
	if value > 65535 {
		return 65535
	}
	return uint16(value)
}

func isSchedulePointCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.HasPrefix(code, "sell_time_point_") ||
		strings.HasPrefix(code, "sell_mode_kw_point_") ||
		strings.HasPrefix(code, "sell_mode_batt_capacity_") ||
		strings.HasPrefix(code, "charge_mode_point_")
}

func schedulePointNumberFromCode(code string) int {
	code = strings.ToLower(strings.TrimSpace(code))
	idx := strings.LastIndex(code, "_")
	if idx < 0 || idx+1 >= len(code) {
		return 0
	}
	n := 0
	for _, ch := range code[idx+1:] {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	if n < 1 || n > 6 {
		return 0
	}
	return n
}

func schedulePointFieldRank(code string) int {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case strings.HasPrefix(code, "sell_time_point_"):
		return 1
	case strings.HasPrefix(code, "sell_mode_kw_point_"):
		return 2
	case strings.HasPrefix(code, "sell_mode_batt_capacity_"):
		return 3
	case strings.HasPrefix(code, "charge_mode_point_"):
		// ChargeMode пишем последним внутри точки: на Deye/Sunsynk он чаще всего
		// сбрасывается, если применить его раньше времени/мощности/ёмкости.
		return 4
	default:
		return 9
	}
}

func isUseTimerCode(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), "use_timer")
}

func scheduleWritePhase(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "use_timer" {
		// Use Timer пишем в самом конце, чтобы инвертор не применял расписание,
		// пока точки ещё только перезаписываются.
		return "apply_timer_last"
	}
	if isSchedulePointCode(code) {
		return "schedule_points"
	}
	return "global_settings"
}

func scheduleWritePhaseRank(phase string) int {
	switch phase {
	case "global_settings":
		return 1
	case "schedule_points":
		return 2
	case "apply_timer_last":
		return 3
	default:
		return 9
	}
}

func resolveScheduleWritePlan(values []ScheduleRegisterValue, paramByCode map[string]DeviceParameterFields) ([]scheduleResolvedWrite, []ScheduleWriteResult) {
	plan := make([]scheduleResolvedWrite, 0, len(values))
	skipped := make([]ScheduleWriteResult, 0)
	usedAddress := map[uint16]string{}

	for idx, itemValue := range values {
		code := strings.ToLower(strings.TrimSpace(itemValue.Code))
		field, ok := paramByCode[code]
		if !ok {
			skipped = append(skipped, ScheduleWriteResult{
				Code:           itemValue.Code,
				Status:         "skipped",
				Error:          fmt.Sprintf("code=%s: code not found in selected inverter parameter profile", itemValue.Code),
				Note:           itemValue.Note,
				RequestedValue: itemValue.Value,
			})
			continue
		}

		resBase := ScheduleWriteResult{
			Code:           itemValue.Code,
			Address:        field.ModbusAddress,
			RegisterType:   field.RegisterType,
			Writable:       field.IsWritable,
			Phase:          scheduleWritePhase(itemValue.Code),
			Note:           itemValue.Note,
			RequestedValue: itemValue.Value,
		}
		if !field.IsWritable {
			resBase.Status = "skipped"
			resBase.Error = fmt.Sprintf("code=%s address=%d: parameter is not writable", itemValue.Code, field.ModbusAddress)
			skipped = append(skipped, resBase)
			continue
		}
		if strings.ToLower(strings.TrimSpace(field.RegisterType)) != "holding" {
			resBase.Status = "skipped"
			resBase.Error = fmt.Sprintf("code=%s address=%d register_type=%s: write is supported only for holding registers", itemValue.Code, field.ModbusAddress, field.RegisterType)
			skipped = append(skipped, resBase)
			continue
		}
		if prevCode, exists := usedAddress[field.ModbusAddress]; exists {
			resBase.Status = "skipped"
			resBase.Error = fmt.Sprintf("code=%s address=%d: duplicate modbus_address with code=%s in write plan", itemValue.Code, field.ModbusAddress, prevCode)
			skipped = append(skipped, resBase)
			continue
		}
		usedAddress[field.ModbusAddress] = itemValue.Code

		rawValue, _, encodeErr := encodeScheduleDeviceParameterValue(itemValue.Value, field)
		if encodeErr != nil {
			resBase.Status = "skipped"
			resBase.Error = fmt.Sprintf("code=%s address=%d: encode failed: %v", itemValue.Code, field.ModbusAddress, encodeErr)
			skipped = append(skipped, resBase)
			continue
		}
		writeMode := strings.ToLower(strings.TrimSpace(field.WriteMode))
		if writeMode == "mapped_masked_bits" {
			mapped, exists := field.WriteValues[strconv.Itoa(itemValue.Value)]
			if !exists {
				resBase.Status = "skipped"
				resBase.Error = fmt.Sprintf("code=%s address=%d: no write_values mapping for logical value=%d", itemValue.Code, field.ModbusAddress, itemValue.Value)
				skipped = append(skipped, resBase)
				continue
			}
			rawValue = mapped
		}
		var writeBitmask uint16
		if field.WriteBitmask != nil {
			writeBitmask = *field.WriteBitmask
		}

		plan = append(plan, scheduleResolvedWrite{
			Code:           itemValue.Code,
			Address:        field.ModbusAddress,
			Value:          rawValue,
			RequestedValue: itemValue.Value,
			RegisterType:   field.RegisterType,
			Writable:       field.IsWritable,
			WriteMode:      writeMode,
			WriteBitmask:   writeBitmask,
			Note:           itemValue.Note,
			Phase:          scheduleWritePhase(itemValue.Code),
			OriginalOrder:  idx,
		})
	}

	sort.SliceStable(plan, func(i, j int) bool {
		ri := scheduleWritePhaseRank(plan[i].Phase)
		rj := scheduleWritePhaseRank(plan[j].Phase)
		if ri != rj {
			return ri < rj
		}
		if plan[i].Phase == "schedule_points" && plan[j].Phase == "schedule_points" {
			pi := schedulePointNumberFromCode(plan[i].Code)
			pj := schedulePointNumberFromCode(plan[j].Code)
			if pi != pj {
				return pi < pj
			}
			fi := schedulePointFieldRank(plan[i].Code)
			fj := schedulePointFieldRank(plan[j].Code)
			if fi != fj {
				return fi < fj
			}
		}
		if plan[i].Address != plan[j].Address {
			return plan[i].Address < plan[j].Address
		}
		return plan[i].OriginalOrder < plan[j].OriginalOrder
	})
	return plan, skipped
}

func groupScheduleWritePlan(plan []scheduleResolvedWrite) []scheduleRegisterBatch {
	batches := make([]scheduleRegisterBatch, 0)
	for _, item := range plan {
		if len(batches) == 0 {
			batches = append(batches, scheduleRegisterBatch{Start: item.Address, Items: []scheduleResolvedWrite{item}})
			continue
		}
		lastIdx := len(batches) - 1
		last := &batches[lastIdx]
		lastItem := last.Items[len(last.Items)-1]
		if lastItem.Phase == item.Phase && item.Address == lastItem.Address+1 {
			last.Items = append(last.Items, item)
			continue
		}
		batches = append(batches, scheduleRegisterBatch{Start: item.Address, Items: []scheduleResolvedWrite{item}})
	}
	return batches
}

func scheduleBatchCodes(batch scheduleRegisterBatch) []string {
	codes := make([]string, 0, len(batch.Items))
	for _, item := range batch.Items {
		codes = append(codes, item.Code)
	}
	return codes
}

func scheduleBatchAddresses(batch scheduleRegisterBatch) []uint16 {
	addresses := make([]uint16, 0, len(batch.Items))
	for _, item := range batch.Items {
		addresses = append(addresses, item.Address)
	}
	return addresses
}

func scheduleBatchValues(batch scheduleRegisterBatch) []uint16 {
	values := make([]uint16, 0, len(batch.Items))
	for _, item := range batch.Items {
		values = append(values, item.Value)
	}
	return values
}

const (
	// Scheduler должен укладываться в свой периодический интервал.
	// Поэтому для записи расписания используем отдельные короткие настройки,
	// а не общие maxRetries=5 и modbusTimeout=10s, которые подходят для ручного теста/логирования.
	schedulerModbusTimeout               = 1500 * time.Millisecond
	schedulerWriteRetries                = 2
	schedulerRetryDelay                  = 250 * time.Millisecond
	schedulerNextRegisterDelay           = 2 * time.Second
	schedulerTimerApplyDelay             = 2 * time.Second
	schedulerRegisterVerifyRetries       = 5
	schedulerRegisterReadAfterWriteDelay = 150 * time.Millisecond
)

func modbusRegisterError(code string, address uint16, err error) string {
	if err == nil {
		return fmt.Sprintf("code=%s address=%d", code, address)
	}
	return fmt.Sprintf("code=%s address=%d: %v", code, address, err)
}

func scheduleWriteFailedDetails(results []ScheduleWriteResult) []map[string]any {
	details := make([]map[string]any, 0)
	for _, r := range results {
		if r.Status != "error" && r.Status != "skipped" {
			continue
		}
		details = append(details, map[string]any{
			"code":          r.Code,
			"address":       r.Address,
			"status":        r.Status,
			"error":         r.Error,
			"register_type": r.RegisterType,
			"writable":      r.Writable,
		})
	}
	return details
}

func scheduleWriteFailedText(results []ScheduleWriteResult) string {
	parts := make([]string, 0)
	for _, r := range results {
		if r.Status != "error" && r.Status != "skipped" {
			continue
		}
		if strings.Contains(r.Error, "code=") || strings.Contains(r.Error, "address=") {
			parts = append(parts, r.Error)
			continue
		}
		if r.Address == 0 {
			parts = append(parts, fmt.Sprintf("code=%s: %s", r.Code, r.Error))
			continue
		}
		parts = append(parts, fmt.Sprintf("code=%s address=%d: %s", r.Code, r.Address, r.Error))
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 8 {
		parts = append(parts[:8], fmt.Sprintf("... ещё %d", len(parts)-8))
	}
	return strings.Join(parts, "; ")
}

func (a *App) openScheduleClientWithRetry(client *modbus.ModbusClient, url string, inverterID int64, retries int, delay time.Duration, command string) error {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		attemptStarted := time.Now()
		a.appendAppLog("debug", "scheduler modbus open attempt started", map[string]any{
			"component":    "scheduler_modbus",
			"command":      command,
			"inverter_id":  inverterID,
			"url":          url,
			"attempt":      attempt,
			"max_attempts": retries,
		})
		err := client.Open()
		if err == nil {
			a.appendAppLog("debug", "scheduler modbus open attempt ok", map[string]any{
				"component":   "scheduler_modbus",
				"command":     command,
				"inverter_id": inverterID,
				"url":         url,
				"attempt":     attempt,
				"duration_ms": time.Since(attemptStarted).Milliseconds(),
			})
			return nil
		}
		lastErr = err
		a.appendAppLog("warn", "scheduler modbus open attempt failed", map[string]any{
			"component":    "scheduler_modbus",
			"command":      command,
			"inverter_id":  inverterID,
			"url":          url,
			"attempt":      attempt,
			"max_attempts": retries,
			"duration_ms":  time.Since(attemptStarted).Milliseconds(),
			"error":        err.Error(),
		})
		if attempt < retries {
			time.Sleep(delay)
		}
	}
	return lastErr
}

func (a *App) writeScheduleRegisterWithRetry(url string, inverterID int64, code string, address uint16, value uint16, retries int, delay time.Duration, command string) error {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		attemptStarted := time.Now()
		a.appendAppLog("debug", "scheduler register write attempt started", map[string]any{
			"component":    "scheduler_modbus",
			"command":      command,
			"inverter_id":  inverterID,
			"code":         code,
			"address":      address,
			"value":        value,
			"attempt":      attempt,
			"max_attempts": retries,
			"timeout_ms":   schedulerModbusTimeout.Milliseconds(),
			"connection":   "new_per_attempt",
			"write_method": "fc16_write_multiple_registers",
		})

		client, err := modbus.NewClient(&modbus.ClientConfiguration{URL: url, Timeout: schedulerModbusTimeout})
		if err != nil {
			lastErr = err
			a.appendAppLog("warn", "scheduler register write client creation failed", map[string]any{
				"component":    "scheduler_modbus",
				"command":      command,
				"inverter_id":  inverterID,
				"code":         code,
				"address":      address,
				"value":        value,
				"attempt":      attempt,
				"max_attempts": retries,
				"duration_ms":  time.Since(attemptStarted).Milliseconds(),
				"error":        modbusRegisterError(code, address, err),
			})
		} else {
			openErr := client.Open()
			if openErr != nil {
				lastErr = openErr
				a.appendAppLog("warn", "scheduler register write open failed", map[string]any{
					"component":    "scheduler_modbus",
					"command":      command,
					"inverter_id":  inverterID,
					"code":         code,
					"address":      address,
					"value":        value,
					"attempt":      attempt,
					"max_attempts": retries,
					"duration_ms":  time.Since(attemptStarted).Milliseconds(),
					"error":        modbusRegisterError(code, address, openErr),
				})
			} else {
				writeErr := client.WriteRegisters(address, []uint16{value})
				_ = client.Close()
				if writeErr == nil {
					a.appendAppLog("debug", "scheduler register write ok", map[string]any{
						"component":   "scheduler_modbus",
						"command":     command,
						"inverter_id": inverterID,
						"code":        code,
						"address":     address,
						"value":       value,
						"attempt":     attempt,
						"duration_ms": time.Since(attemptStarted).Milliseconds(),
					})
					return nil
				}
				lastErr = writeErr
				a.appendAppLog("warn", "scheduler register write attempt failed", map[string]any{
					"component":    "scheduler_modbus",
					"command":      command,
					"inverter_id":  inverterID,
					"code":         code,
					"address":      address,
					"value":        value,
					"attempt":      attempt,
					"max_attempts": retries,
					"duration_ms":  time.Since(attemptStarted).Milliseconds(),
					"error":        modbusRegisterError(code, address, writeErr),
				})
			}
			_ = client.Close()
		}
		if attempt < retries {
			time.Sleep(delay)
		}
	}
	return lastErr
}

func (a *App) writeScheduleRegisterOnOpenClientWithRetry(client *modbus.ModbusClient, url string, inverterID int64, code string, address uint16, value uint16, retries int, delay time.Duration, command string) error {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		attemptStarted := time.Now()
		a.appendAppLog("debug", "scheduler register write attempt started", map[string]any{
			"component":    "scheduler_modbus",
			"command":      command,
			"inverter_id":  inverterID,
			"code":         code,
			"address":      address,
			"value":        value,
			"attempt":      attempt,
			"max_attempts": retries,
			"timeout_ms":   schedulerModbusTimeout.Milliseconds(),
			"connection":   "shared_for_cycle",
			"write_method": "fc16_write_multiple_registers",
		})

		if err := client.WriteRegisters(address, []uint16{value}); err == nil {
			a.appendAppLog("debug", "scheduler register write ok", map[string]any{
				"component":   "scheduler_modbus",
				"command":     command,
				"inverter_id": inverterID,
				"code":        code,
				"address":     address,
				"value":       value,
				"attempt":     attempt,
				"duration_ms": time.Since(attemptStarted).Milliseconds(),
			})
			return nil
		} else {
			lastErr = err
			a.appendAppLog("warn", "scheduler register write attempt failed", map[string]any{
				"component":   "scheduler_modbus",
				"command":     command,
				"inverter_id": inverterID,
				"code":        code,
				"address":     address,
				"value":       value,
				"attempt":     attempt,
				"duration_ms": time.Since(attemptStarted).Milliseconds(),
				"error":       modbusRegisterError(code, address, err),
			})
		}

		if attempt < retries {
			_ = client.Close()
			if delay > 0 {
				time.Sleep(delay)
			}
			if err := a.openScheduleClientWithRetry(client, url, inverterID, 1, 0, command); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

func (a *App) readScheduleRegisterOnOpenClientWithRetry(client *modbus.ModbusClient, url string, inverterID int64, code string, address uint16, retries int, delay time.Duration, command string) (uint16, error) {
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		attemptStarted := time.Now()
		a.appendAppLog("debug", "scheduler register read-back attempt started", map[string]any{
			"component":    "scheduler_modbus",
			"command":      command,
			"inverter_id":  inverterID,
			"code":         code,
			"address":      address,
			"attempt":      attempt,
			"max_attempts": retries,
			"timeout_ms":   schedulerModbusTimeout.Milliseconds(),
		})

		value, err := client.ReadRegister(address, modbus.HOLDING_REGISTER)
		if err == nil {
			a.appendAppLog("debug", "scheduler register read-back ok", map[string]any{
				"component":   "scheduler_modbus",
				"command":     command,
				"inverter_id": inverterID,
				"code":        code,
				"address":     address,
				"value":       value,
				"attempt":     attempt,
				"duration_ms": time.Since(attemptStarted).Milliseconds(),
			})
			return value, nil
		}
		lastErr = err
		a.appendAppLog("warn", "scheduler register read-back failed", map[string]any{
			"component":   "scheduler_modbus",
			"command":     command,
			"inverter_id": inverterID,
			"code":        code,
			"address":     address,
			"attempt":     attempt,
			"duration_ms": time.Since(attemptStarted).Milliseconds(),
			"error":       modbusRegisterError(code, address, err),
		})
		if attempt < retries {
			_ = client.Close()
			if delay > 0 {
				time.Sleep(delay)
			}
			if err := a.openScheduleClientWithRetry(client, url, inverterID, 1, 0, command); err != nil {
				lastErr = err
			}
		}
	}
	return 0, lastErr
}

func (a *App) prepareMaskedScheduleWriteOnOpenClient(client *modbus.ModbusClient, url string, inverterID int64, item scheduleResolvedWrite, command string) (scheduleResolvedWrite, error) {
	if item.WriteMode != "masked_bits" && item.WriteMode != "mapped_masked_bits" {
		return item, nil
	}
	if item.WriteBitmask == 0 {
		return item, fmt.Errorf("code=%s address=%d: write_bitmask is required for %s", item.Code, item.Address, item.WriteMode)
	}
	current, err := a.readScheduleRegisterOnOpenClientWithRetry(client, url, inverterID, item.Code, item.Address, schedulerWriteRetries, schedulerRetryDelay, command)
	if err != nil {
		return item, fmt.Errorf("read-before-write failed: %w", err)
	}
	item.Value = (current &^ item.WriteBitmask) | (item.Value & item.WriteBitmask)
	return item, nil
}

func (a *App) writeAndVerifyScheduleRegisterOnOpenClient(client *modbus.ModbusClient, url string, inverterID int64, item scheduleResolvedWrite, command string) (uint16, int, error) {
	var lastErr error
	var actual uint16

	for attempt := 1; attempt <= schedulerRegisterVerifyRetries; attempt++ {
		if schedulerNextRegisterDelay > 0 {
			a.appendAppLog("debug", "scheduler waiting before register write", map[string]any{
				"component":    "scheduler_modbus",
				"command":      command,
				"inverter_id":  inverterID,
				"code":         item.Code,
				"address":      item.Address,
				"value":        item.Value,
				"attempt":      attempt,
				"max_attempts": schedulerRegisterVerifyRetries,
				"sleep_ms":     schedulerNextRegisterDelay.Milliseconds(),
			})
			time.Sleep(schedulerNextRegisterDelay)
		}

		writeErr := a.writeScheduleRegisterOnOpenClientWithRetry(client, url, inverterID, item.Code, item.Address, item.Value, 1, 0, command)
		if writeErr != nil {
			lastErr = writeErr
			a.appendAppLog("warn", "scheduler register write failed before read-back", map[string]any{
				"component":    "scheduler_modbus",
				"command":      command,
				"inverter_id":  inverterID,
				"phase":        item.Phase,
				"code":         item.Code,
				"address":      item.Address,
				"expected":     item.Value,
				"attempt":      attempt,
				"max_attempts": schedulerRegisterVerifyRetries,
				"error":        modbusRegisterError(item.Code, item.Address, writeErr),
			})
			_ = client.Close()
			if err := a.openScheduleClientWithRetry(client, url, inverterID, 1, 0, command); err != nil {
				lastErr = err
			}
			continue
		}

		if schedulerRegisterReadAfterWriteDelay > 0 {
			time.Sleep(schedulerRegisterReadAfterWriteDelay)
		}
		readValue, readErr := a.readScheduleRegisterOnOpenClientWithRetry(client, url, inverterID, item.Code, item.Address, 1, 0, command)
		actual = readValue
		if readErr != nil {
			lastErr = readErr
			a.appendAppLog("warn", "scheduler register read-back failed after write", map[string]any{
				"component":    "scheduler_modbus",
				"command":      command,
				"inverter_id":  inverterID,
				"phase":        item.Phase,
				"code":         item.Code,
				"address":      item.Address,
				"expected":     item.Value,
				"attempt":      attempt,
				"max_attempts": schedulerRegisterVerifyRetries,
				"error":        modbusRegisterError(item.Code, item.Address, readErr),
			})
			_ = client.Close()
			if err := a.openScheduleClientWithRetry(client, url, inverterID, 1, 0, command); err != nil {
				lastErr = err
			}
			continue
		}

		if actual == item.Value {
			a.appendAppLog("info", "scheduler register write verified", map[string]any{
				"component":   "scheduler_modbus",
				"command":     command,
				"inverter_id": inverterID,
				"phase":       item.Phase,
				"code":        item.Code,
				"address":     item.Address,
				"expected":    item.Value,
				"actual":      actual,
				"attempt":     attempt,
			})
			return actual, attempt, nil
		}

		lastErr = fmt.Errorf("read-back mismatch: expected=%d actual=%d", item.Value, actual)
		a.appendAppLog("warn", "scheduler register read-back mismatch after write", map[string]any{
			"component":    "scheduler_modbus",
			"command":      command,
			"inverter_id":  inverterID,
			"phase":        item.Phase,
			"code":         item.Code,
			"address":      item.Address,
			"expected":     item.Value,
			"actual":       actual,
			"attempt":      attempt,
			"max_attempts": schedulerRegisterVerifyRetries,
		})
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("read-back verification failed")
	}
	return actual, schedulerRegisterVerifyRetries, lastErr
}

func (a *App) writeScheduleRegisterBatchOnOpenClientWithRetry(client *modbus.ModbusClient, url string, inverterID int64, batch scheduleRegisterBatch, retries int, delay time.Duration, command string) error {
	var lastErr error
	codes := scheduleBatchCodes(batch)
	addresses := scheduleBatchAddresses(batch)
	values := scheduleBatchValues(batch)
	codeText := strings.Join(codes, ",")
	for attempt := 1; attempt <= retries; attempt++ {
		attemptStarted := time.Now()
		a.appendAppLog("debug", "scheduler register batch write attempt started", map[string]any{
			"component":     "scheduler_modbus",
			"command":       command,
			"inverter_id":   inverterID,
			"phase":         batch.Items[0].Phase,
			"start_address": batch.Start,
			"count":         len(batch.Items),
			"addresses":     addresses,
			"codes":         codes,
			"values":        values,
			"attempt":       attempt,
			"max_attempts":  retries,
			"timeout_ms":    schedulerModbusTimeout.Milliseconds(),
			"connection":    "shared_for_cycle",
			"write_method":  "fc16_write_multiple_registers_batch",
		})

		if err := client.WriteRegisters(batch.Start, values); err == nil {
			a.appendAppLog("debug", "scheduler register batch write ok", map[string]any{
				"component":     "scheduler_modbus",
				"command":       command,
				"inverter_id":   inverterID,
				"phase":         batch.Items[0].Phase,
				"start_address": batch.Start,
				"count":         len(batch.Items),
				"addresses":     addresses,
				"codes":         codes,
				"values":        values,
				"attempt":       attempt,
				"duration_ms":   time.Since(attemptStarted).Milliseconds(),
			})
			return nil
		} else {
			lastErr = err
			a.appendAppLog("warn", "scheduler register batch write attempt failed", map[string]any{
				"component":     "scheduler_modbus",
				"command":       command,
				"inverter_id":   inverterID,
				"phase":         batch.Items[0].Phase,
				"start_address": batch.Start,
				"count":         len(batch.Items),
				"addresses":     addresses,
				"codes":         codes,
				"values":        values,
				"attempt":       attempt,
				"max_attempts":  retries,
				"duration_ms":   time.Since(attemptStarted).Milliseconds(),
				"error":         modbusRegisterError(codeText, batch.Start, err),
			})
		}

		if attempt < retries {
			_ = client.Close()
			if delay > 0 {
				time.Sleep(delay)
			}
			if err := a.openScheduleClientWithRetry(client, url, inverterID, 1, 0, command); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

type ScheduleExecutionPlan struct {
	Payload                  SchedulePayload
	SelectedDay              DayItem
	Slot                     MinuteSlot
	CurrentConfig            HourConfig
	PointConfigs             map[int]HourConfig
	Registers                map[string]any
	Values                   []ScheduleRegisterValue
	CandidateCount           int
	Source                   string
	FillAllPointsWithCurrent bool
}

type pointScheduleCandidate struct {
	Cfg        HourConfig
	Minutes    int
	FromCustom bool
}

func minutesFromSellTime(sellTime int) int {
	hour := sellTime / 100
	minute := sellTime % 100
	if hour < 0 {
		hour = 0
	}
	if hour > 23 {
		hour = 23
	}
	if minute < 0 {
		minute = 0
	}
	if minute > 59 {
		minute = 59
	}
	return hour*60 + minute
}

func candidateFromHourConfig(h HourConfig, fromCustom bool) pointScheduleCandidate {
	if h.Point < 1 || h.Point > 6 {
		h.Point = pointForHour(h.Hour)
	}
	if h.SellTime <= 0 && h.Hour != 0 {
		h.SellTime = sellTimeForSlot(h.Hour, 0)
	}
	if h.Hour == 0 && h.SellTime < 0 {
		h.SellTime = 0
	}
	if h.Label == "" {
		if h.SellTime%100 != 0 {
			h.Label = fmt.Sprintf("%02d:%02d", h.SellTime/100, h.SellTime%100)
		} else {
			h.Label = hourLabel(h.Hour)
		}
	}
	return pointScheduleCandidate{Cfg: h, Minutes: minutesFromSellTime(h.SellTime), FromCustom: fromCustom}
}

func candidateFromMinuteSlot(s MinuteSlot) pointScheduleCandidate {
	cfg := minuteSlotToHourConfig(s)
	cfg.Point = pointForHour(cfg.Hour)
	cfg.SellTime = sellTimeForSlot(s.Hour, s.Minute)
	cfg.Label = fmt.Sprintf("%02d:%02d", s.Hour, s.Minute)
	return candidateFromHourConfig(cfg, true)
}

func upsertScheduleCandidate(byTime map[int]pointScheduleCandidate, candidate pointScheduleCandidate) {
	if candidate.Cfg.SellTime < 0 {
		return
	}
	// Ключом является реальное время внутри текущих суток, а не номер точки инвертора.
	// Это исключает возврат старых sell_time из прошлой раскладки 1..6.
	byTime[candidate.Minutes] = candidate
}

func scheduleCandidatesForDay(day *DayItem) []pointScheduleCandidate {
	if day == nil {
		return nil
	}
	byTime := map[int]pointScheduleCandidate{}

	for _, h := range day.Hours {
		if !h.Enabled {
			continue
		}
		h.Point = pointForHour(h.Hour)
		h.SellTime = sellTimeForSlot(h.Hour, 0)
		h.Label = hourLabel(h.Hour)
		upsertScheduleCandidate(byTime, candidateFromHourConfig(h, false))
	}

	// 5-минутные точки участвуют в отправке только если они реально заполнены,
	// активны и отличаются от часового значения. Пустые 5-минутки наследуют HH:00.
	for _, s := range day.Slots {
		if !s.Enabled || s.Minute == 0 || s.Hour < 0 || s.Hour >= len(day.Hours) {
			continue
		}
		if !minuteSlotDiffersFromHour(s, day.Hours[s.Hour]) {
			continue
		}
		upsertScheduleCandidate(byTime, candidateFromMinuteSlot(s))
	}
	for _, s := range day.CustomSlots {
		if !s.Enabled || s.Minute == 0 || s.Hour < 0 || s.Hour >= len(day.Hours) {
			continue
		}
		if !minuteSlotDiffersFromHour(s, day.Hours[s.Hour]) {
			continue
		}
		upsertScheduleCandidate(byTime, candidateFromMinuteSlot(s))
	}

	candidates := make([]pointScheduleCandidate, 0, len(byTime))
	for _, candidate := range byTime {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Minutes < candidates[j].Minutes
	})
	return candidates
}

func reversePointCandidates(in []pointScheduleCandidate) []pointScheduleCandidate {
	out := make([]pointScheduleCandidate, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func nearestScheduleCandidate(candidates []pointScheduleCandidate, targetMinutes int) (pointScheduleCandidate, bool) {
	if len(candidates) == 0 {
		return pointScheduleCandidate{}, false
	}
	best := candidates[0]
	bestDistance := absInt(best.Minutes - targetMinutes)
	for _, c := range candidates[1:] {
		d := absInt(c.Minutes - targetMinutes)
		if d < bestDistance || (d == bestDistance && c.Minutes <= targetMinutes && best.Minutes > targetMinutes) {
			best = c
			bestDistance = d
		}
	}
	return best, true
}

func getScheduleConfigForExecution(day *DayItem, hour, minute int) (HourConfig, string, bool) {
	if day == nil {
		return HourConfig{}, "", false
	}
	if hour < 0 || hour > 23 {
		return HourConfig{}, "", false
	}
	minute = (minute / 5) * 5
	currentMinutes := hour*60 + minute

	// Основной путь: берём текущий час/5-минутный слот из расписания инвертора.
	// Никакие прочитанные значения с инвертора здесь не используются.
	if day.Enabled && hour < len(day.Hours) {
		hourCfg := day.Hours[hour]
		if hourCfg.Enabled {
			if idx := slotIndex(hour, minute); idx >= 0 && idx < len(day.Slots) {
				slot := day.Slots[idx]
				if minute != 0 && slot.Enabled && minuteSlotDiffersFromHour(slot, hourCfg) {
					cfg := minuteSlotToHourConfig(slot)
					cfg.SellTime = sellTimeForSlot(slot.Hour, slot.Minute)
					cfg.Label = minuteSlotLabel(slot.Hour, slot.Minute)
					cfg.Point = pointForHour(slot.Hour)
					return cfg, "custom_5_minute_slot_from_schedule", true
				}
			}
			hourCfg.SellTime = sellTimeForSlot(hourCfg.Hour, 0)
			hourCfg.Label = hourLabel(hourCfg.Hour)
			hourCfg.Point = pointForHour(hourCfg.Hour)
			return hourCfg, "hour_from_schedule", true
		}
	}

	// Если текущий час не заполнен, не берём нули и не берём состояние инвертора.
	// Берём ближайшую заполненную точку из этого же дня.
	if nearest, ok := nearestScheduleCandidate(scheduleCandidatesForDay(day), currentMinutes); ok {
		return nearest.Cfg, "nearest_filled_schedule_point_same_day", true
	}
	return HourConfig{}, "", false
}

func selectSixSchedulePointCandidates(day *DayItem, currentCandidate pointScheduleCandidate) []pointScheduleCandidate {
	const totalPoints = 6
	const preferredBeforeCount = 2

	currentMinutes := currentCandidate.Minutes
	before := make([]pointScheduleCandidate, 0)
	after := make([]pointScheduleCandidate, 0)
	all := scheduleCandidatesForDay(day)

	for _, c := range all {
		if c.Minutes == currentCandidate.Minutes {
			continue
		}
		// Пользовательская точка внутри часа заменяет базовую HH:00 для выбора
		// шести точек, а не добавляется рядом с ней. Иначе базовая точка того же
		// часа вытесняет одну из двух предыдущих точек.
		if currentCandidate.FromCustom && c.Minutes/60 == currentMinutes/60 && c.Minutes%60 == 0 {
			continue
		}
		if c.Minutes < currentMinutes {
			before = append(before, c)
			continue
		}
		if c.Minutes > currentMinutes {
			after = append(after, c)
		}
	}

	// Берём точки только в рамках текущих суток: без кругового перехода через 00:00.
	// До текущего времени — ближайшие предыдущие точки, после текущего времени — ближайшие следующие.
	sort.SliceStable(before, func(i, j int) bool {
		if before[i].Minutes != before[j].Minutes {
			return before[i].Minutes > before[j].Minutes
		}
		return before[i].Cfg.SellTime > before[j].Cfg.SellTime
	})
	sort.SliceStable(after, func(i, j int) bool {
		if after[i].Minutes != after[j].Minutes {
			return after[i].Minutes < after[j].Minutes
		}
		return after[i].Cfg.SellTime < after[j].Cfg.SellTime
	})

	beforeCount := preferredBeforeCount
	if beforeCount > len(before) {
		beforeCount = len(before)
	}
	afterCount := totalPoints - 1 - beforeCount
	if afterCount > len(after) {
		afterCount = len(after)
	}

	remaining := totalPoints - 1 - beforeCount - afterCount
	if remaining > 0 {
		extraBefore := len(before) - beforeCount
		if extraBefore > remaining {
			extraBefore = remaining
		}
		beforeCount += extraBefore
		remaining -= extraBefore
	}
	if remaining > 0 {
		extraAfter := len(after) - afterCount
		if extraAfter > remaining {
			extraAfter = remaining
		}
		afterCount += extraAfter
		remaining -= extraAfter
	}

	selected := make([]pointScheduleCandidate, 0, totalPoints)
	selected = append(selected, reversePointCandidates(before[:beforeCount])...)
	selected = append(selected, currentCandidate)
	selected = append(selected, after[:afterCount]...)

	// Если в расписании мало заполненных точек, не оставляем регистры нулевыми.
	// Добиваем ближайшими уже выбранными/доступными точками в рамках текущих суток.
	for len(selected) < totalPoints {
		if nearest, ok := nearestScheduleCandidate(all, currentMinutes); ok {
			selected = append(selected, nearest)
		} else {
			selected = append(selected, currentCandidate)
		}
	}
	return selected[:totalPoints]
}

func copySchedulePointValuesFromCurrent(dst HourConfig, current HourConfig) HourConfig {
	dst.Enabled = current.Enabled
	dst.SellModeKW = current.SellModeKW
	dst.SellModeBattCapacity = current.SellModeBattCapacity
	dst.ChargeMode = current.ChargeMode
	dst.GridExportLimit = current.GridExportLimit
	dst.GridChargeEnabled = current.GridChargeEnabled
	dst.SolarExport = current.SolarExport
	dst.LoadLimitMode = current.LoadLimitMode
	dst.UseTimer = current.UseTimer
	dst.UseTimerMask = current.UseTimerMask
	dst.PriorityLoad = current.PriorityLoad
	return dst
}

func buildPointConfigsWithMode(day *DayItem, currentHourCfg HourConfig, fillAllPointsWithCurrent bool) map[int]HourConfig {
	const totalPoints = 6
	if currentHourCfg.SellTime <= 0 && currentHourCfg.Hour != 0 {
		currentHourCfg.SellTime = sellTimeForSlot(currentHourCfg.Hour, 0)
	}
	if currentHourCfg.Label == "" {
		currentHourCfg.Label = minuteSlotLabel(currentHourCfg.Hour, currentHourCfg.SellTime%100)
	}
	currentCandidate := candidateFromHourConfig(currentHourCfg, currentHourCfg.SellTime%100 != 0)
	selected := selectSixSchedulePointCandidates(day, currentCandidate)

	result := map[int]HourConfig{}
	for i := 0; i < totalPoints && i < len(selected); i++ {
		cfg := selected[i].Cfg
		if fillAllPointsWithCurrent {
			cfg = copySchedulePointValuesFromCurrent(cfg, currentHourCfg)
		}
		cfg.Point = i + 1
		if cfg.SellTime <= 0 && cfg.Hour != 0 {
			cfg.SellTime = sellTimeForSlot(cfg.Hour, 0)
		}
		if cfg.Label == "" {
			cfg.Label = minuteSlotLabel(cfg.Hour, cfg.SellTime%100)
		}
		result[i+1] = cfg
	}
	for len(result) < totalPoints {
		idx := len(result) + 1
		cfg := currentHourCfg
		cfg.Point = idx
		result[idx] = cfg
	}
	return result
}

func buildPointConfigs(day *DayItem, currentHourCfg HourConfig) map[int]HourConfig {
	return buildPointConfigsWithMode(day, currentHourCfg, false)
}

func scheduleRegisterValuesFromPointConfigs(useTimerMask int, currentHourCfg HourConfig, pointConfigs map[int]HourConfig) []ScheduleRegisterValue {
	var values []ScheduleRegisterValue

	currentHourCfg.UseTimerMask = useTimerMask
	values = append(values, []ScheduleRegisterValue{
		{Code: "grid_export_limit", Value: currentHourCfg.GridExportLimit, Note: "global setting from current schedule slot"},
		{Code: "grid_charge_enable", Value: boolToInt(currentHourCfg.GridChargeEnabled), Note: "global setting from current schedule slot"},
		{Code: "solar_sell", Value: boolToInt(currentHourCfg.SolarExport), Note: "global setting from current schedule slot"},
		{Code: "inverter_work_mode", Value: currentHourCfg.LoadLimitMode, Note: "global setting from current schedule slot"},
		{Code: "use_timer", Value: normalizeUseTimerMask(useTimerMask), Note: "global use timer bitmask from schedule_json: bit0=enable, bit1=Monday ... bit7=Sunday"},
		{Code: "priority_load", Value: normalizePriorityLoadValue(currentHourCfg.PriorityLoad), Note: "global setting from current schedule slot"},
	}...)

	for point := 1; point <= 6; point++ {
		cfg, ok := pointConfigs[point]
		if !ok {
			cfg = currentHourCfg
		}
		values = append(values, []ScheduleRegisterValue{
			{Code: fmt.Sprintf("sell_time_point_%d", point), Value: cfg.SellTime, Note: fmt.Sprintf("point %d sell time from schedule = %d", point, cfg.SellTime)},
			{Code: fmt.Sprintf("sell_mode_batt_capacity_%d", point), Value: cfg.SellModeBattCapacity, Note: fmt.Sprintf("point %d battery capacity from schedule", point)},
			{Code: fmt.Sprintf("charge_mode_point_%d", point), Value: cfg.ChargeMode, Note: fmt.Sprintf("point %d charge mode from schedule", point)},
			{Code: fmt.Sprintf("sell_mode_kw_point_%d", point), Value: cfg.SellModeKW, Note: fmt.Sprintf("point %d power from schedule", point)},
		}...)
	}

	return values
}

func scheduleRegistersMapFromPointConfigs(useTimerMask int, currentHourCfg HourConfig, pointConfigs map[int]HourConfig) map[string]any {
	registers := map[string]any{
		"GridExportLimit":  currentHourCfg.GridExportLimit,
		"GridChargeEnable": boolToInt(currentHourCfg.GridChargeEnabled),
		"SolarSell":        boolToInt(currentHourCfg.SolarExport),
		"InverterWorkMode": currentHourCfg.LoadLimitMode,
		"UseTimer":         normalizeUseTimerMask(useTimerMask),
		"PriorityLoad":     normalizePriorityLoadValue(currentHourCfg.PriorityLoad),
	}
	for point := 1; point <= 6; point++ {
		cfg, ok := pointConfigs[point]
		if !ok {
			cfg = currentHourCfg
		}
		registers[fmt.Sprintf("SellTimePoint%d", point)] = cfg.SellTime
		registers[fmt.Sprintf("SellModeKWPoint%d", point)] = cfg.SellModeKW
		registers[fmt.Sprintf("SellModeBattCapacity%d", point)] = cfg.SellModeBattCapacity
		registers[fmt.Sprintf("ChargeModePoint%d", point)] = cfg.ChargeMode
	}
	return registers
}

func buildScheduleExecutionPlanFromJSON(scheduleJSON string, day int, hour int, minute int, fillAllPointsWithCurrent bool) (ScheduleExecutionPlan, bool, error) {
	payload, err := parseSchedulePayloadLoose(scheduleJSON)
	if err != nil {
		return ScheduleExecutionPlan{}, false, fmt.Errorf("ошибка разбора schedule_json: %w", err)
	}
	if !isValidScheduleJSON(scheduleJSON) {
		return ScheduleExecutionPlan{}, false, fmt.Errorf("некорректный schedule_json")
	}
	normalizeSchedulePayloadSellTimes(&payload)

	var selectedDay *DayItem
	for i := range payload.Days {
		if payload.Days[i].Day == day {
			selectedDay = &payload.Days[i]
			break
		}
	}
	if selectedDay == nil {
		return ScheduleExecutionPlan{}, false, nil
	}

	currentCfg, source, ok := getScheduleConfigForExecution(selectedDay, hour, minute)
	if !ok {
		return ScheduleExecutionPlan{}, false, nil
	}
	currentCfg.UseTimerMask = payload.UseTimerMask
	currentCfg.UseTimer = payload.UseTimerMask&1 != 0

	pointConfigs := buildPointConfigsWithMode(selectedDay, currentCfg, fillAllPointsWithCurrent)
	registers := scheduleRegistersMapFromPointConfigs(payload.UseTimerMask, currentCfg, pointConfigs)
	values := scheduleRegisterValuesFromPointConfigs(payload.UseTimerMask, currentCfg, pointConfigs)
	slot := minuteSlotFromHourConfig(currentCfg, currentCfg.SellTime%100)
	slot.Label = currentCfg.Label
	slot.SellTime = currentCfg.SellTime
	slot.Point = pointForHour(currentCfg.Hour)

	plan := ScheduleExecutionPlan{
		Payload:                  payload,
		SelectedDay:              *selectedDay,
		Slot:                     slot,
		CurrentConfig:            currentCfg,
		PointConfigs:             pointConfigs,
		Registers:                registers,
		Values:                   values,
		CandidateCount:           len(scheduleCandidatesForDay(selectedDay)),
		Source:                   source,
		FillAllPointsWithCurrent: fillAllPointsWithCurrent,
	}
	return plan, true, nil
}

func allPointsScheduleRegisterValues(day *DayItem, useTimerMask int, currentHourCfg HourConfig) []ScheduleRegisterValue {
	pointConfigs := buildPointConfigs(day, currentHourCfg)
	return scheduleRegisterValuesFromPointConfigs(useTimerMask, currentHourCfg, pointConfigs)
}

func (a *App) sendScheduleToInverter(item runtimeScheduledItem, cfg HourConfig, day int) ([]ScheduleWriteResult, error) {
	return a.sendScheduleToInverterWithCommand(item, cfg, "", day)
}

func (a *App) sendScheduleToInverterWithCommand(item runtimeScheduledItem, cfg HourConfig, command string, day int) ([]ScheduleWriteResult, error) {
	payload, parseErr := parseSchedulePayloadLoose(item.ScheduleJSON)
	if parseErr != nil {
		return nil, fmt.Errorf("ошибка разбора schedule_json: %w", parseErr)
	}
	var selectedDay *DayItem
	for i := range payload.Days {
		if payload.Days[i].Day == day {
			selectedDay = &payload.Days[i]
			break
		}
	}
	if selectedDay == nil {
		return nil, fmt.Errorf("день %d не найден в расписании", day)
	}
	values := allPointsScheduleRegisterValues(selectedDay, payload.UseTimerMask, cfg)
	return a.sendScheduleRegisterValuesToInverterWithCommand(item, values, command)
}

func (a *App) sendScheduleExecutionPlanToInverterWithCommand(item runtimeScheduledItem, plan ScheduleExecutionPlan, command string) ([]ScheduleWriteResult, error) {
	if len(plan.Values) == 0 {
		return nil, fmt.Errorf("план отправки пуст: нет регистров для записи")
	}
	return a.sendScheduleRegisterValuesToInverterWithCommand(item, plan.Values, command)
}

// sendScheduleRegisterValuesToInverterWithCommand writes a schedule and records
// the execution with one history row per register (read-back status included).
func (a *App) sendScheduleRegisterValuesToInverterWithCommand(item runtimeScheduledItem, values []ScheduleRegisterValue, command string) ([]ScheduleWriteResult, error) {
	started := time.Now()
	results, err := a.writeScheduleRegisterValues(item, values, command)
	a.recordScheduleExecutionHistory(item, command, results, err, time.Since(started))
	return results, err
}

func (a *App) recordScheduleExecutionHistory(item runtimeScheduledItem, command string, results []ScheduleWriteResult, err error, duration time.Duration) {
	summary := summarizeScheduleWriteResults(results)
	status := histVerified
	switch {
	case err != nil && summary.OK > 0:
		status = histPartial
	case err != nil:
		status = histError
	}
	jobRef := "schedule:" + item.ScheduleName
	if item.TemplateName != "" {
		jobRef += " template:" + item.TemplateName
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	opID := a.recordHistory(HistoryEntry{OperationType: opScheduleExecution, Status: status, InverterID: item.InverterID, Initiator: "планировщик (" + command + ")",
		JobRef: jobRef, DurationMS: duration.Milliseconds(), Error: errText,
		Message: fmt.Sprintf("Запись расписания: подтверждено %d, ошибок %d, пропущено %d из %d", summary.OK, summary.Error, summary.Skipped, summary.Total),
		Details: map[string]any{"command": command, "summary": summary}})
	for _, r := range results {
		st := histVerified
		switch r.Status {
		case "skipped":
			st = histSkipped
		case "ok":
			if !r.Verified {
				st = histUnverified
			}
		default:
			st = histError
		}
		var actual *int
		if r.Status == "ok" || r.ActualValue != 0 {
			actual = intPtr(int(r.ActualValue))
		}
		msg := r.Phase
		if r.Note != "" {
			msg = strings.TrimSpace(msg + " · " + r.Note)
		}
		a.recordHistory(HistoryEntry{ParentID: opID, OperationType: opRegisterWrite, Status: st, InverterID: item.InverterID, Initiator: "планировщик",
			JobRef: jobRef, RegisterAddress: intPtr(int(r.Address)), RegisterCode: r.Code, RequestedValue: intPtr(r.RequestedValue),
			WrittenValue: intPtr(int(r.Value)), VerifiedValue: actual, Attempt: r.Attempts, Message: msg, Error: r.Error})
	}
}

func (a *App) writeScheduleRegisterValues(item runtimeScheduledItem, values []ScheduleRegisterValue, command string) ([]ScheduleWriteResult, error) {
	a.modbusMu.Lock()
	defer a.modbusMu.Unlock()

	model, params, err := loadDeviceParametersForModel(item.ModelKey)
	if err != nil {
		a.appendAppLog("error", "scheduler inverter parameter profile read failed", map[string]any{
			"component": "scheduler_modbus", "command": command, "inverter_id": item.InverterID,
			"model_key": item.ModelKey, "error": err.Error(),
		})
		return nil, err
	}
	item.ModelKey = model.Key
	item.ModelName = model.Name
	item.ParametersFile = model.ParametersFile
	a.appendAppLog("info", "scheduler loaded inverter parameter profile for modbus write", map[string]any{
		"component": "scheduler_modbus", "command": command, "inverter_id": item.InverterID,
		"model_key": model.Key, "model_name": model.Name, "path": model.ParametersFile,
	})
	paramByCode := make(map[string]DeviceParameterFields, len(params))
	writableCount := 0
	for _, p := range params {
		code := strings.ToLower(strings.TrimSpace(p.Fields.Code))
		if code != "" {
			paramByCode[code] = p.Fields
		}
		if p.Fields.IsWritable {
			writableCount++
		}
	}
	a.appendAppLog("info", "scheduler device parameters loaded for modbus write", map[string]any{
		"component":       "scheduler_modbus",
		"command":         command,
		"inverter_id":     item.InverterID,
		"params_total":    len(params),
		"writable_count":  writableCount,
		"model_key":       model.Key,
		"parameters_file": model.ParametersFile,
	})

	a.appendAppLog("info", "scheduler prepared register values from inverter schedule only", map[string]any{
		"component":                          "scheduler_modbus",
		"command":                            command,
		"inverter_id":                        item.InverterID,
		"scheduler_timeout_ms":               schedulerModbusTimeout.Milliseconds(),
		"scheduler_open_retries":             schedulerWriteRetries,
		"scheduler_retry_delay_ms":           schedulerRetryDelay.Milliseconds(),
		"register_pre_write_sleep_ms":        schedulerNextRegisterDelay.Milliseconds(),
		"register_read_after_write_delay_ms": schedulerRegisterReadAfterWriteDelay.Milliseconds(),
		"register_verify_retries":            schedulerRegisterVerifyRetries,
		"values_count":                       len(values),
		"source":                             "schedule_json_only",
	})

	writePlan, skippedResults := resolveScheduleWritePlan(values, paramByCode)
	if len(skippedResults) > 0 {
		parts := make([]string, 0, len(skippedResults))
		for _, skipped := range skippedResults {
			parts = append(parts, skipped.Error)
		}
		return skippedResults, fmt.Errorf("предварительная проверка расписания не пройдена; запись не начата: %s", strings.Join(parts, "; "))
	}
	if len(writePlan) != len(values) {
		return nil, fmt.Errorf("предварительная проверка расписания: ожидалось %d записей, подготовлено %d", len(values), len(writePlan))
	}

	url := fmt.Sprintf("tcp://%s:%d", item.IP, item.Port)
	client, err := modbus.NewClient(&modbus.ClientConfiguration{URL: url, Timeout: schedulerModbusTimeout})
	if err != nil {
		return nil, fmt.Errorf("ошибка создания Modbus-клиента: %w", err)
	}
	if err := a.openScheduleClientWithRetry(client, url, item.InverterID, schedulerWriteRetries, schedulerRetryDelay, command); err != nil {
		return nil, fmt.Errorf("ошибка подключения к Modbus %s: %w", url, err)
	}
	defer client.Close()

	results := make([]ScheduleWriteResult, 0, len(values))
	a.appendAppLog("info", "scheduler resolved device-parameter write plan", map[string]any{
		"component":        "scheduler_modbus",
		"command":          command,
		"inverter_id":      item.InverterID,
		"values_count":     len(values),
		"plan_count":       len(writePlan),
		"skipped_count":    len(skippedResults),
		"write_order_rule": "use_timer_off -> global_settings_by_address -> schedule_points_by_point -> use_timer_last",
		"write_mode":       "single_register_fc16_sleep_2s_write_read_compare_5_attempts",
	})

	writeOrder := 0
	applyTimerPlan := make([]scheduleResolvedWrite, 0, 1)
	mainPlan := make([]scheduleResolvedWrite, 0, len(writePlan))
	for _, planItem := range writePlan {
		if isUseTimerCode(planItem.Code) {
			applyTimerPlan = append(applyTimerPlan, planItem)
			continue
		}
		mainPlan = append(mainPlan, planItem)
	}

	writePlanItem := func(planItem scheduleResolvedWrite) ScheduleWriteResult {
		writeOrder++
		effectiveItem, prepareErr := a.prepareMaskedScheduleWriteOnOpenClient(client, url, item.InverterID, planItem, command)
		if prepareErr != nil {
			return ScheduleWriteResult{Code: planItem.Code, Address: planItem.Address, Value: planItem.Value, Status: "error", Error: prepareErr.Error(), RegisterType: planItem.RegisterType, Writable: planItem.Writable, WriteOrder: writeOrder, Phase: planItem.Phase, Note: planItem.Note, RequestedValue: planItem.RequestedValue}
		}
		planItem = effectiveItem
		a.appendAppLog("debug", "scheduler register resolved from selected inverter profile", map[string]any{
			"component":          "scheduler_modbus",
			"command":            command,
			"inverter_id":        item.InverterID,
			"write_order":        writeOrder,
			"phase":              planItem.Phase,
			"code":               planItem.Code,
			"address":            planItem.Address,
			"value":              planItem.Value,
			"requested_value":    planItem.RequestedValue,
			"note":               planItem.Note,
			"verify_retries":     schedulerRegisterVerifyRetries,
			"pre_write_sleep_ms": schedulerNextRegisterDelay.Milliseconds(),
		})
		actual, attempts, err := a.writeAndVerifyScheduleRegisterOnOpenClient(client, url, item.InverterID, planItem, command)
		res := ScheduleWriteResult{
			Code:           planItem.Code,
			Address:        planItem.Address,
			Value:          planItem.Value,
			Status:         "ok",
			RegisterType:   planItem.RegisterType,
			Writable:       planItem.Writable,
			WriteOrder:     writeOrder,
			Attempts:       attempts,
			Verified:       true,
			ActualValue:    actual,
			Phase:          planItem.Phase,
			Note:           planItem.Note,
			RequestedValue: planItem.RequestedValue,
		}
		if err != nil {
			res.Status = "error"
			res.Verified = false
			res.Error = modbusRegisterError(planItem.Code, planItem.Address, err)
			a.appendAppLog("error", "scheduler register write/read-back verification failed; continuing with next register", map[string]any{
				"component":    "scheduler_modbus",
				"command":      command,
				"inverter_id":  item.InverterID,
				"write_order":  writeOrder,
				"phase":        planItem.Phase,
				"code":         planItem.Code,
				"address":      planItem.Address,
				"expected":     planItem.Value,
				"actual":       actual,
				"attempts":     attempts,
				"max_attempts": schedulerRegisterVerifyRetries,
				"error":        res.Error,
			})
		}
		return res
	}

	// Если Use Timer уже включён на инверторе, простая запись точек по одной может
	// приводить к тому, что инвертор применяет частично обновлённое расписание.
	// Поэтому сначала временно выключаем таймер, затем пишем и проверяем точки,
	// и только после успешной попытки возвращаем целевую маску UseTimer.
	preDisableFailed := false
	for _, timerItem := range applyTimerPlan {
		if timerItem.Value == 0 {
			continue
		}
		preDisable := timerItem
		preDisable.Value = 0
		preDisable.RequestedValue = 0
		preDisable.Phase = "prepare_timer_off"
		preDisable.Note = "temporary use_timer=0 before schedule rewrite"
		result := writePlanItem(preDisable)
		results = append(results, result)
		if result.Status != "ok" {
			preDisableFailed = true
		}
		if schedulerTimerApplyDelay > 0 {
			time.Sleep(schedulerTimerApplyDelay)
		}
	}

	if preDisableFailed {
		for _, planItem := range mainPlan {
			results = append(results, ScheduleWriteResult{
				Code: planItem.Code, Address: planItem.Address, Value: planItem.Value, Status: "skipped",
				Error:        "schedule rewrite aborted because temporary use_timer=0 could not be verified",
				RegisterType: planItem.RegisterType, Writable: planItem.Writable, Phase: planItem.Phase, Note: planItem.Note, RequestedValue: planItem.RequestedValue,
			})
		}
		for _, timerItem := range applyTimerPlan {
			results = append(results, ScheduleWriteResult{
				Code: timerItem.Code, Address: timerItem.Address, Value: timerItem.Value, Status: "skipped",
				Error:        "target use_timer mask was not applied because pre-disable failed",
				RegisterType: timerItem.RegisterType, Writable: timerItem.Writable, Phase: timerItem.Phase, Note: timerItem.Note, RequestedValue: timerItem.RequestedValue,
			})
		}
	} else {
		for _, planItem := range mainPlan {
			results = append(results, writePlanItem(planItem))
		}

		mainFailed := scheduleWriteResultsHaveFailure(results)
		if len(applyTimerPlan) > 0 && schedulerTimerApplyDelay > 0 {
			time.Sleep(schedulerTimerApplyDelay)
		}
		for _, timerItem := range applyTimerPlan {
			if mainFailed && timerItem.Value != 0 {
				results = append(results, ScheduleWriteResult{
					Code: timerItem.Code, Address: timerItem.Address, Value: timerItem.Value, Status: "skipped",
					Error:        "target use_timer mask was not applied because one or more schedule registers failed verification; timer remains disabled",
					RegisterType: timerItem.RegisterType, Writable: timerItem.Writable, Phase: timerItem.Phase, Note: timerItem.Note, RequestedValue: timerItem.RequestedValue,
				})
				continue
			}
			results = append(results, writePlanItem(timerItem))
		}
	}

	summary := summarizeScheduleWriteResults(results)
	failed := scheduleWriteFailedDetails(results)
	a.appendAppLog("info", "scheduler modbus write cycle completed", map[string]any{
		"component":     "scheduler_modbus",
		"command":       command,
		"inverter_id":   item.InverterID,
		"summary":       summary,
		"results_count": len(results),
		"results":       results,
		"failed":        failed,
	})
	if summary.Error > 0 || summary.Skipped > 0 {
		failedText := scheduleWriteFailedText(results)
		if failedText != "" {
			return results, fmt.Errorf("расписание записано не полностью: ok=%d, ошибок=%d, пропущено=%d; %s", summary.OK, summary.Error, summary.Skipped, failedText)
		}
		return results, fmt.Errorf("расписание записано не полностью: ok=%d, ошибок=%d, пропущено=%d", summary.OK, summary.Error, summary.Skipped)
	}
	return results, nil
}

func scheduleWriteResultsHaveFailure(results []ScheduleWriteResult) bool {
	for _, result := range results {
		if result.Status != "ok" {
			return true
		}
	}
	return false
}

func currentHourSellTime(hour, minute int) int {
	return sellTimeForSlot(hour, minute)
}

func scheduleRegisterValues(cfg HourConfig) []ScheduleRegisterValue {
	point := cfg.Point
	if point < 1 || point > 6 {
		point = pointForHour(cfg.Hour)
	}
	return []ScheduleRegisterValue{
		{Code: fmt.Sprintf("sell_time_point_%d", point), Value: cfg.SellTime, Note: fmt.Sprintf("sell time = %d", cfg.SellTime)},
		{Code: fmt.Sprintf("sell_mode_batt_capacity_%d", point), Value: cfg.SellModeBattCapacity, Note: "current slot battery capacity"},
		{Code: fmt.Sprintf("charge_mode_point_%d", point), Value: cfg.ChargeMode, Note: "current slot charge mode"},
		{Code: "grid_export_limit", Value: cfg.GridExportLimit, Note: "global setting"},
		{Code: "grid_charge_enable", Value: boolToInt(cfg.GridChargeEnabled), Note: "global setting"},
		{Code: "solar_sell", Value: boolToInt(cfg.SolarExport), Note: "global setting"},
		{Code: "inverter_work_mode", Value: cfg.LoadLimitMode, Note: "global setting"},
		{Code: "use_timer", Value: normalizeUseTimerMask(cfg.UseTimerMask), Note: "global use timer bitmask: bit0=enable, bit1=Monday ... bit7=Sunday"},
		{Code: "priority_load", Value: normalizePriorityLoadValue(cfg.PriorityLoad), Note: "global setting"},
		{Code: fmt.Sprintf("sell_mode_kw_point_%d", point), Value: cfg.SellModeKW, Note: "current slot point power; written last because some inverters reject 0 here"},
	}
}

func summarizeScheduleWriteResults(results []ScheduleWriteResult) ScheduleWriteSummary {
	summary := ScheduleWriteSummary{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case "ok":
			summary.OK++
		case "error":
			summary.Error++
		case "skipped":
			summary.Skipped++
		}
	}
	return summary
}
