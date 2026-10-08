package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/simonvetter/modbus"
)

// Modbus protocol limits (Modbus Application Protocol v1.1b3, FC03/FC04/FC16).
const (
	modbusMaxReadRegisters  = 125
	modbusMaxWriteRegisters = 123
	modbusMaxUnitID         = 247
)

// tryLockModbus waits up to `wait` for the shared Modbus lock. The scheduler
// can hold the lock for minutes while writing a schedule; readers give up
// instead of piling up behind it.
func (a *App) tryLockModbus(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if a.modbusMu.TryLock() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const errModbusBusy = "канал Modbus занят другой операцией (например, записью расписания); повторите позже"

func dialModbus(ip string, port int, unitID uint8, timeout time.Duration) (*modbus.ModbusClient, error) {
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     fmt.Sprintf("tcp://%s", net.JoinHostPort(ip, fmt.Sprint(port))),
		Timeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	if err := client.Open(); err != nil {
		return nil, err
	}
	if unitID != 1 {
		if err := client.SetUnitId(unitID); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	return client, nil
}

// classifyModbusError returns a short machine-friendly error string for API
// clients ("timeout", "connection refused", Modbus exception names …).
func classifyModbusError(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	if errors.Is(err, modbus.ErrRequestTimedOut) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "no route to host"):
		return "no route to host"
	case strings.Contains(msg, "i/o timeout"):
		return "timeout"
	}
	return msg
}

func registerTypeFromString(raw string) (modbus.RegType, string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "holding", "holding_register", "holding_registers":
		return modbus.HOLDING_REGISTER, "holding", nil
	case "input", "input_register", "input_registers":
		return modbus.INPUT_REGISTER, "input", nil
	}
	return 0, "", fmt.Errorf("неподдерживаемый register_type %q: допустимо holding или input", raw)
}

type modbusBlockRead struct {
	Address      uint16
	Count        uint16
	RegisterType modbus.RegType
	Values       []uint16
	Attempts     int
	Err          error
}

// readModbusBlocks reads each block with up to `attempts` tries. After a failed
// attempt the TCP connection is re-established, because a timed-out Modbus/TCP
// stream can deliver the late answer to the next request.
func readModbusBlocks(ip string, port int, unitID uint8, timeout time.Duration, attempts int, retryDelay time.Duration, blocks []modbusBlockRead) []modbusBlockRead {
	if attempts < 1 {
		attempts = 1
	}
	var client *modbus.ModbusClient
	var dialErr error
	closeClient := func() {
		if client != nil {
			_ = client.Close()
			client = nil
		}
	}
	defer closeClient()
	for i := range blocks {
		b := &blocks[i]
		for attempt := 1; attempt <= attempts; attempt++ {
			b.Attempts = attempt
			if client == nil {
				client, dialErr = dialModbus(ip, port, unitID, timeout)
				if dialErr != nil {
					client = nil
					b.Err = dialErr
					if attempt < attempts {
						time.Sleep(retryDelay)
					}
					continue
				}
			}
			values, err := client.ReadRegisters(b.Address, b.Count, b.RegisterType)
			if err == nil {
				b.Values, b.Err = values, nil
				break
			}
			b.Err = err
			if errors.Is(err, modbus.ErrIllegalDataAddress) || errors.Is(err, modbus.ErrIllegalFunction) || errors.Is(err, modbus.ErrIllegalDataValue) {
				// A definitive exception answer from the device: retrying cannot change it.
				break
			}
			closeClient()
			if attempt < attempts {
				time.Sleep(retryDelay)
			}
		}
	}
	return blocks
}

type registerWriteAttempt struct {
	Attempt    int    `json:"attempt"`
	Stage      string `json:"stage"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	ReadValue  *int   `json:"read_value,omitempty"`
}

type registerWriteOutcome struct {
	PreviousValue    *int                   `json:"previous_value,omitempty"`
	WrittenValue     uint16                 `json:"written_value"`
	VerifiedValue    *int                   `json:"verified_value,omitempty"`
	Verified         bool                   `json:"verified"`
	WriteAccepted    bool                   `json:"write_accepted"`
	ConcurrentChange bool                   `json:"concurrent_change"`
	Attempts         []registerWriteAttempt `json:"attempts"`
	Error            string                 `json:"error,omitempty"`
	DurationMS       int64                  `json:"duration_ms"`
}

type registerWriteSpec struct {
	IP              string
	Port            int
	UnitID          uint8
	Address         uint16
	Value           uint16
	Mask            uint16 // 0 = whole register; otherwise read-modify-write of these bits
	ExpectedCurrent *uint16
	Timeout         time.Duration
	VerifyReads     int
	VerifyDelay     time.Duration
}

// writeRegisterVerified performs exactly one FC16 write (never retried
// silently), optionally after a read-before-write, then verifies by reading the
// register back up to VerifyReads times. Every step is reported in Attempts.
func writeRegisterVerified(spec registerWriteSpec) registerWriteOutcome {
	started := time.Now()
	out := registerWriteOutcome{Attempts: []registerWriteAttempt{}}
	if spec.VerifyReads < 1 {
		spec.VerifyReads = 3
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 5 * time.Second
	}
	finish := func() registerWriteOutcome {
		out.DurationMS = time.Since(started).Milliseconds()
		return out
	}
	step := func(stage string, fn func() (*int, error)) error {
		t := time.Now()
		v, err := fn()
		a := registerWriteAttempt{Attempt: len(out.Attempts) + 1, Stage: stage, OK: err == nil, DurationMS: time.Since(t).Milliseconds(), ReadValue: v}
		if err != nil {
			a.Error = classifyModbusError(err)
		}
		out.Attempts = append(out.Attempts, a)
		return err
	}

	var client *modbus.ModbusClient
	if err := step("connect", func() (*int, error) {
		c, err := dialModbus(spec.IP, spec.Port, spec.UnitID, spec.Timeout)
		client = c
		return nil, err
	}); err != nil {
		out.Error = "подключение не установлено: " + classifyModbusError(err)
		return finish()
	}
	defer client.Close()

	var current uint16
	if err := step("read_before_write", func() (*int, error) {
		v, err := client.ReadRegister(spec.Address, modbus.HOLDING_REGISTER)
		if err != nil {
			return nil, err
		}
		current = v
		return intPtr(int(v)), nil
	}); err != nil {
		out.Error = "не удалось прочитать текущее значение перед записью: " + classifyModbusError(err)
		return finish()
	}
	out.PreviousValue = intPtr(int(current))
	if spec.ExpectedCurrent != nil && *spec.ExpectedCurrent != current {
		out.ConcurrentChange = true
		out.Error = fmt.Sprintf("значение регистра изменилось с момента предпросмотра: ожидалось %d, сейчас %d; запись отменена", *spec.ExpectedCurrent, current)
		return finish()
	}
	target := spec.Value
	if spec.Mask != 0 {
		target = mergeMaskedRegisterValue(current, spec.Value, spec.Mask)
	}
	out.WrittenValue = target
	if err := step("write_fc16", func() (*int, error) {
		return nil, client.WriteRegisters(spec.Address, []uint16{target})
	}); err != nil {
		out.Error = "инвертор не подтвердил запись: " + classifyModbusError(err)
		// The device may still have applied the value; read back once so the
		// operator sees the real state instead of a guess.
		_ = step("read_after_failed_write", func() (*int, error) {
			v, err := client.ReadRegister(spec.Address, modbus.HOLDING_REGISTER)
			if err != nil {
				return nil, err
			}
			out.VerifiedValue = intPtr(int(v))
			return intPtr(int(v)), nil
		})
		return finish()
	}
	out.WriteAccepted = true
	var lastErr error
	for i := 1; i <= spec.VerifyReads; i++ {
		if spec.VerifyDelay > 0 {
			time.Sleep(spec.VerifyDelay)
		}
		var actual uint16
		err := step("read_back", func() (*int, error) {
			v, err := client.ReadRegister(spec.Address, modbus.HOLDING_REGISTER)
			if err != nil {
				return nil, err
			}
			actual = v
			return intPtr(int(v)), nil
		})
		if err != nil {
			lastErr = err
			continue
		}
		out.VerifiedValue = intPtr(int(actual))
		if actual == target {
			out.Verified = true
			return finish()
		}
		lastErr = fmt.Errorf("прочитано %d, ожидалось %d", actual, target)
	}
	if lastErr != nil {
		out.Error = "запись принята, но не подтверждена чтением: " + classifyModbusError(lastErr)
	}
	return finish()
}
