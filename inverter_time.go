package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/simonvetter/modbus"
)

type InverterDateTime struct {
	Time       time.Time `json:"-"`
	Formatted  string    `json:"formatted"`
	Year       int       `json:"year"`
	Month      int       `json:"month"`
	Day        int       `json:"day"`
	Hour       int       `json:"hour"`
	Minute     int       `json:"minute"`
	Second     int       `json:"second"`
	Register62 uint16    `json:"register_62"`
	Register63 uint16    `json:"register_63"`
	Register64 uint16    `json:"register_64"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}

type InverterTimeSetAttempt struct {
	Register   uint16 `json:"register"`
	Value      uint16 `json:"value"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type InverterTimeSetResponse struct {
	OK          bool                     `json:"ok"`
	Message     string                   `json:"message"`
	IP          string                   `json:"ip"`
	Port        int                      `json:"port"`
	Source      string                   `json:"source"`
	Datetime    string                   `json:"datetime"`
	Registers   map[string]uint16        `json:"registers"`
	Attempts    []InverterTimeSetAttempt `json:"attempts"`
	WriteMethod string                   `json:"write_method"`
}

func packTwoBytes(high, low int) uint16 {
	return uint16((high&0xFF)<<8 | (low & 0xFF))
}

func unpackHighLow(v uint16) (int, int) {
	return int((v >> 8) & 0xFF), int(v & 0xFF)
}

func decodeInverterDateTime(reg62, reg63, reg64 uint16) InverterDateTime {
	yearOffset, month := unpackHighLow(reg62)
	day, hour := unpackHighLow(reg63)
	minute, second := unpackHighLow(reg64)
	year := 2000 + yearOffset
	status := "ok"
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 59 {
		status = "invalid"
	}
	t := time.Date(year, time.Month(clampInt(month, 1, 12)), clampInt(day, 1, 31), clampInt(hour, 0, 23), clampInt(minute, 0, 59), clampInt(second, 0, 59), 0, time.Local)
	return InverterDateTime{
		Time:       t,
		Formatted:  t.Format("2006-01-02 15:04:05"),
		Year:       year,
		Month:      month,
		Day:        day,
		Hour:       hour,
		Minute:     minute,
		Second:     second,
		Register62: reg62,
		Register63: reg63,
		Register64: reg64,
		Status:     status,
	}
}

func encodeInverterDateTime(t time.Time) (uint16, uint16, uint16) {
	yearOffset := t.Year() - 2000
	return packTwoBytes(yearOffset, int(t.Month())), packTwoBytes(t.Day(), t.Hour()), packTwoBytes(t.Minute(), t.Second())
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func inverterTimeRegisterAddresses(modelKey string) (uint16, uint16, uint16, error) {
	_, params, err := loadDeviceParametersForModel(modelKey)
	if err != nil {
		return 0, 0, 0, err
	}
	byCode := deviceParametersByCode(params)
	yearMonth, ok1 := byCode["inverter_time_year_month"]
	dayHour, ok2 := byCode["inverter_time_day_hour"]
	minuteSecond, ok3 := byCode["inverter_time_minute_second"]
	if !ok1 || !ok2 || !ok3 {
		return 0, 0, 0, fmt.Errorf("в профиле отсутствуют адреса времени инвертора")
	}
	return yearMonth.ModbusAddress, dayHour.ModbusAddress, minuteSecond.ModbusAddress, nil
}

func (a *App) readInverterDateTime(ip string, port int, modelKey string, timeout time.Duration) (InverterDateTime, error) {
	addrYearMonth, addrDayHour, addrMinSec, err := inverterTimeRegisterAddresses(modelKey)
	if err != nil {
		return InverterDateTime{Status: "error", Error: err.Error()}, err
	}
	url := fmt.Sprintf("tcp://%s:%d", ip, port)
	client, err := modbus.NewClient(&modbus.ClientConfiguration{URL: url, Timeout: timeout})
	if err != nil {
		return InverterDateTime{Status: "error", Error: err.Error()}, err
	}
	if err := client.Open(); err != nil {
		return InverterDateTime{Status: "error", Error: err.Error()}, err
	}
	defer client.Close()
	reg62, err := client.ReadRegister(addrYearMonth, modbus.HOLDING_REGISTER)
	if err != nil {
		return InverterDateTime{Status: "error", Error: err.Error()}, err
	}
	reg63, err := client.ReadRegister(addrDayHour, modbus.HOLDING_REGISTER)
	if err != nil {
		return InverterDateTime{Status: "error", Error: err.Error()}, err
	}
	reg64, err := client.ReadRegister(addrMinSec, modbus.HOLDING_REGISTER)
	if err != nil {
		return InverterDateTime{Status: "error", Error: err.Error()}, err
	}
	return decodeInverterDateTime(reg62, reg63, reg64), nil
}

func (a *App) setInverterDateTime(ip string, port int, modelKey string, t time.Time, retries int, timeoutMS int, delay time.Duration) InverterTimeSetResponse {
	if retries < 1 {
		retries = 1
	}
	if timeoutMS < 500 {
		timeoutMS = 500
	}
	reg62, reg63, reg64 := encodeInverterDateTime(t)
	addrYearMonth, addrDayHour, addrMinSec, addrErr := inverterTimeRegisterAddresses(modelKey)
	resp := InverterTimeSetResponse{
		OK:          false,
		IP:          ip,
		Port:        port,
		Datetime:    t.Format("2006-01-02 15:04:05"),
		Registers:   map[string]uint16{},
		Attempts:    []InverterTimeSetAttempt{},
		WriteMethod: "fc16_write_multiple_registers_one_register_at_a_time",
	}
	if addrErr != nil {
		resp.Message = "Не удалось определить адреса времени: " + addrErr.Error()
		return resp
	}
	resp.Registers[fmt.Sprintf("%d_year_month", addrYearMonth)] = reg62
	resp.Registers[fmt.Sprintf("%d_day_hour", addrDayHour)] = reg63
	resp.Registers[fmt.Sprintf("%d_minute_second", addrMinSec)] = reg64

	url := fmt.Sprintf("tcp://%s:%d", ip, port)
	type regVal struct {
		reg uint16
		val uint16
	}
	regs := []regVal{{addrYearMonth, reg62}, {addrDayHour, reg63}, {addrMinSec, reg64}}

	a.modbusMu.Lock()
	defer a.modbusMu.Unlock()

	for _, rv := range regs {
		var ok bool
		var lastErr error
		for attempt := 1; attempt <= retries; attempt++ {
			started := time.Now()
			item := InverterTimeSetAttempt{Register: rv.reg, Value: rv.val, Status: "error"}
			client, err := modbus.NewClient(&modbus.ClientConfiguration{URL: url, Timeout: time.Duration(timeoutMS) * time.Millisecond})
			if err == nil {
				err = client.Open()
			}
			if err == nil {
				err = client.WriteRegisters(rv.reg, []uint16{rv.val})
			}
			if client != nil {
				_ = client.Close()
			}
			item.DurationMS = time.Since(started).Milliseconds()
			if err == nil {
				item.Status = "ok"
				resp.Attempts = append(resp.Attempts, item)
				ok = true
				break
			}
			lastErr = err
			item.Error = fmt.Sprintf("register=%d value=%d: %v", rv.reg, rv.val, err)
			resp.Attempts = append(resp.Attempts, item)
			if attempt < retries && delay > 0 {
				time.Sleep(delay)
			}
		}
		if !ok {
			resp.Message = fmt.Sprintf("Не удалось записать время инвертора: register=%d: %v", rv.reg, lastErr)
			return resp
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	resp.OK = true
	resp.Message = "Время инвертора успешно записано"
	return resp
}

func (a *App) setInverterTimeHandler(w http.ResponseWriter, r *http.Request) {
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
	source := "custom"
	modelKey := strings.TrimSpace(r.FormValue("model_key"))
	if ip == "" || port <= 0 || modelKey == "" {
		item, src, err := a.defaultModbusTarget()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "IP/Port не указаны и инвертор не найден: " + err.Error()})
			return
		}
		if ip == "" {
			ip = item.IP
		}
		if port <= 0 {
			port = item.Port
		}
		if modelKey == "" {
			modelKey = item.ModelKey
		}
		source = src
	}
	retries := parseIntDefault(r.FormValue("retries"), 3)
	timeoutMS := parseIntDefault(r.FormValue("timeout_ms"), 5000)
	delaySeconds := parseIntDefault(r.FormValue("retry_delay_seconds"), 2)
	mode := strings.TrimSpace(r.FormValue("mode"))
	var t time.Time
	if mode == "now" || strings.TrimSpace(r.FormValue("datetime")) == "" {
		settings, _ := a.getSettings()
		loc := time.Local
		if settings.Timezone != "" {
			if loaded, err := time.LoadLocation(settings.Timezone); err == nil {
				loc = loaded
			}
		}
		t = time.Now().In(loc)
	} else {
		raw := strings.TrimSpace(r.FormValue("datetime"))
		parsed, err := time.ParseInLocation("2006-01-02T15:04", raw, time.Local)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректная дата/время. Используй формат datetime-local"})
			return
		}
		sec := parseIntDefault(r.FormValue("second"), 0)
		if sec < 0 {
			sec = 0
		}
		if sec > 59 {
			sec = 59
		}
		t = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), sec, 0, parsed.Location())
	}
	resp := a.setInverterDateTime(ip, port, modelKey, t, retries, timeoutMS, time.Duration(delaySeconds)*time.Second)
	resp.Source = source
	status := http.StatusOK
	if !resp.OK {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, resp)
}

func parseUint16Default(raw string, def uint16) uint16 {
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 16)
	if err != nil {
		return def
	}
	return uint16(v)
}
