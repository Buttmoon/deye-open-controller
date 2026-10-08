package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/simonvetter/modbus"
)

const (
	modbusTimeout = 1500 * time.Millisecond
	maxRetries    = 2
	retryDelay    = 250 * time.Millisecond
)

type DeviceParameterFixture struct {
	Model  string                `json:"model"`
	PK     string                `json:"pk"`
	Fields DeviceParameterFields `json:"fields"`
}

type DeviceParameterFields struct {
	DeviceConfigUUID string `json:"device_config_uuid"`

	Code         string `json:"code"`
	RegisterType string `json:"register_type"`

	ModbusAddress   uint16   `json:"modbus_address"`
	ModbusAddresses []uint16 `json:"modbus_addresses,omitempty"`
	RegisterCount   int      `json:"register_count"`
	DataType        string   `json:"data_type,omitempty"`
	WordOrder       string   `json:"word_order,omitempty"`

	ScaleFactor              *float64 `json:"scale_factor"`
	WriteScaleFactor         *float64 `json:"write_scale_factor,omitempty"`
	ScheduleInputScaleFactor *float64 `json:"schedule_input_scale_factor,omitempty"`
	Offset                   *float64 `json:"offset,omitempty"`
	RawMin                   *float64 `json:"raw_min,omitempty"`
	RawMax                   *float64 `json:"raw_max,omitempty"`
	EnforceBounds            bool     `json:"enforce_bounds,omitempty"`
	Signed                   bool     `json:"signed,omitempty"`

	Source          string `json:"source,omitempty"`
	Confidence      string `json:"confidence,omitempty"`
	Notes           string `json:"notes,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	ProfileVariant  string `json:"profile_variant,omitempty"`

	Suffix      *string `json:"suffix"`
	Name        string  `json:"name"`
	Description string  `json:"description"`

	Value      any             `json:"value"`
	Type       string          `json:"type"`
	EnumValues json.RawMessage `json:"enum_values"`

	Min  *float64 `json:"min"`
	Max  *float64 `json:"max"`
	Step *float64 `json:"step"`

	CreatedAt *string `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`

	ChartLineColor string `json:"chart_line_color"`

	Bitmask       *int              `json:"bitmask"`
	WriteMode     string            `json:"write_mode,omitempty"`
	WriteBitmask  *uint16           `json:"write_bitmask,omitempty"`
	WriteValues   map[string]uint16 `json:"write_values,omitempty"`
	AllowedValues []int             `json:"allowed_values,omitempty"`
	// ValueKind: boolean | uint | int | enum | bitmask | scaled | multi | time.
	// Empty means "infer from the other fields" (see inferRegisterValueKind).
	ValueKind string           `json:"value_kind,omitempty"`
	Bits      []RegisterBitDef `json:"bits,omitempty"`

	IsWritable bool `json:"is_writable"`

	ShowInCharts bool `json:"show_in_charts"`
	ShowInTable  bool `json:"show_in_table"`

	DisplayDecimalPlaces *int `json:"display_decimal_places"`

	IsActive bool `json:"is_active"`
}

type deviceParameterCacheEntry struct {
	modTime time.Time
	size    int64
	params  []DeviceParameterFixture
}

var deviceParametersCache struct {
	sync.RWMutex
	entries map[string]deviceParameterCacheEntry
}

func LoadDeviceParameters(path string) ([]DeviceParameterFixture, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	deviceParametersCache.RLock()
	entry, ok := deviceParametersCache.entries[path]
	if ok && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
		params := append([]DeviceParameterFixture(nil), entry.params...)
		deviceParametersCache.RUnlock()
		return params, nil
	}
	deviceParametersCache.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var params []DeviceParameterFixture
	if err := json.Unmarshal(data, &params); err != nil {
		return nil, err
	}
	enrichProfileBitLayouts(params)

	deviceParametersCache.Lock()
	if deviceParametersCache.entries == nil {
		deviceParametersCache.entries = make(map[string]deviceParameterCacheEntry)
	}
	deviceParametersCache.entries[path] = deviceParameterCacheEntry{
		modTime: info.ModTime(),
		size:    info.Size(),
		params:  append([]DeviceParameterFixture(nil), params...),
	}
	deviceParametersCache.Unlock()

	return params, nil
}

func deviceParameterReadScale(fields DeviceParameterFields) float64 {
	if fields.ScaleFactor == nil || *fields.ScaleFactor == 0 {
		return 1
	}
	return *fields.ScaleFactor
}

func deviceParameterWriteScale(fields DeviceParameterFields) float64 {
	if fields.WriteScaleFactor != nil && *fields.WriteScaleFactor != 0 {
		return *fields.WriteScaleFactor
	}
	if fields.ScaleFactor != nil && *fields.ScaleFactor > 0 {
		return *fields.ScaleFactor
	}
	return 1
}

func deviceParameterScheduleInputScale(fields DeviceParameterFields) float64 {
	if fields.ScheduleInputScaleFactor == nil || *fields.ScheduleInputScaleFactor == 0 {
		return 1
	}
	return *fields.ScheduleInputScaleFactor
}

func deviceParameterAddresses(fields DeviceParameterFields) []uint16 {
	if len(fields.ModbusAddresses) > 0 {
		return append([]uint16(nil), fields.ModbusAddresses...)
	}
	count := fields.RegisterCount
	if count <= 1 {
		return []uint16{fields.ModbusAddress}
	}
	addresses := make([]uint16, count)
	for i := range addresses {
		addresses[i] = fields.ModbusAddress + uint16(i)
	}
	return addresses
}

func decodeDeviceParameterRegisters(raw []uint16, fields DeviceParameterFields) (float64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("нет значений регистров")
	}
	dataType := strings.ToLower(strings.TrimSpace(fields.DataType))
	if dataType == "" {
		if len(raw) >= 2 {
			dataType = "uint32"
		} else if fields.Signed {
			dataType = "int16"
		} else {
			dataType = "uint16"
		}
	}
	var base float64
	switch dataType {
	case "uint16":
		base = float64(raw[0])
	case "int16":
		base = float64(int16(raw[0]))
	case "uint32", "int32":
		if len(raw) < 2 {
			return 0, fmt.Errorf("для %s требуется 2 регистра, получен %d", dataType, len(raw))
		}
		var combined uint32
		if strings.EqualFold(strings.TrimSpace(fields.WordOrder), "low_high") {
			combined = uint32(raw[1])<<16 | uint32(raw[0])
		} else {
			combined = uint32(raw[0])<<16 | uint32(raw[1])
		}
		if dataType == "int32" {
			base = float64(int32(combined))
		} else {
			base = float64(combined)
		}
	default:
		return 0, fmt.Errorf("неподдерживаемый data_type=%s", fields.DataType)
	}
	value := base * deviceParameterReadScale(fields)
	if fields.Offset != nil {
		value += *fields.Offset
	}
	return value, nil
}

func decodeDeviceParameterValue(raw uint16, fields DeviceParameterFields) float64 {
	value, err := decodeDeviceParameterRegisters([]uint16{raw}, fields)
	if err != nil {
		return 0
	}
	return value
}

func encodeScheduleDeviceParameterValue(value int, fields DeviceParameterFields) (uint16, float64, error) {
	logicalValue := float64(value) * deviceParameterScheduleInputScale(fields)
	rawValue, err := encodeDeviceParameterValue(logicalValue, fields)
	return rawValue, logicalValue, err
}

func encodeDeviceParameterValue(value float64, fields DeviceParameterFields) (uint16, error) {
	if strings.EqualFold(strings.TrimSpace(fields.Type), "time") {
		rounded := math.Round(value)
		if math.Abs(value-rounded) > 0.000001 || !isValidTOUHHMM(int(rounded)) {
			return 0, fmt.Errorf("значение %.4f не является временем HHMM 00:00–23:55 с шагом 5 минут", value)
		}
	}
	if len(fields.AllowedValues) > 0 && !allowedValuesSupersededByBitLayout(fields) {
		allowed := false
		for _, candidate := range fields.AllowedValues {
			if math.Abs(value-float64(candidate)) < 0.000001 {
				allowed = true
				break
			}
		}
		if !allowed {
			return 0, fmt.Errorf("значение %.4f отсутствует в allowed_values=%v", value, fields.AllowedValues)
		}
	}
	if fields.EnforceBounds {
		if fields.Min != nil && value < *fields.Min {
			return 0, fmt.Errorf("значение %.4f меньше min %.4f", value, *fields.Min)
		}
		if fields.Max != nil && value > *fields.Max {
			return 0, fmt.Errorf("значение %.4f больше max %.4f", value, *fields.Max)
		}
	}
	offset := 0.0
	if fields.Offset != nil {
		offset = *fields.Offset
	}
	rawFloat := (value - offset) / deviceParameterWriteScale(fields)
	rawRounded := math.Round(rawFloat)
	if math.Abs(rawFloat-rawRounded) > 0.000001 {
		return 0, fmt.Errorf("значение %.4f не кратно шагу регистра %.4f", value, deviceParameterWriteScale(fields))
	}
	if fields.RawMin != nil && rawRounded < *fields.RawMin {
		return 0, fmt.Errorf("raw %.0f меньше raw_min %.0f", rawRounded, *fields.RawMin)
	}
	if fields.RawMax != nil && rawRounded > *fields.RawMax {
		return 0, fmt.Errorf("raw %.0f больше raw_max %.0f", rawRounded, *fields.RawMax)
	}
	if fields.Signed {
		if rawRounded < -32768 || rawRounded > 32767 {
			return 0, fmt.Errorf("raw %.0f вне диапазона int16", rawRounded)
		}
		return uint16(int16(rawRounded)), nil
	}
	if rawRounded < 0 || rawRounded > 65535 {
		return 0, fmt.Errorf("raw %.0f вне диапазона uint16", rawRounded)
	}
	if strings.EqualFold(strings.TrimSpace(fields.WriteMode), "masked_bits") && fields.WriteBitmask != nil && *fields.WriteBitmask != 0 {
		if extra := uint16(rawRounded) &^ *fields.WriteBitmask; extra != 0 {
			return 0, fmt.Errorf("raw %d затрагивает биты 0x%04X вне маски записи 0x%04X", uint16(rawRounded), extra, *fields.WriteBitmask)
		}
	}
	return uint16(rawRounded), nil
}

func openClientWithRetry(client *modbus.ModbusClient, retries int, delay time.Duration) error {
	var lastErr error

	for attempt := 1; attempt <= retries; attempt++ {
		err := client.Open()
		if err == nil {
			return nil
		}

		lastErr = err

		log.Printf(
			"Ошибка подключения к Modbus, попытка %d/%d: %v",
			attempt,
			retries,
			err,
		)

		time.Sleep(delay)
	}

	return lastErr
}

func readRegisterWithRetry(
	client *modbus.ModbusClient,
	address uint16,
	registerType modbus.RegType,
	retries int,
	delay time.Duration,
) (uint16, error) {
	var lastErr error

	for attempt := 1; attempt <= retries; attempt++ {
		value, err := client.ReadRegister(address, registerType)
		if err == nil {
			return value, nil
		}

		lastErr = err

		log.Printf(
			"Ошибка чтения регистра %d, попытка %d/%d: %v",
			address,
			attempt,
			retries,
			err,
		)

		time.Sleep(delay)
	}

	return 0, lastErr
}

func getRegisterType(registerType string) (modbus.RegType, bool) {
	switch registerType {
	case "holding":
		return modbus.HOLDING_REGISTER, true
	case "input":
		return modbus.INPUT_REGISTER, true
	default:
		return modbus.HOLDING_REGISTER, false
	}
}

// Before test run `go mod tidy` to add modbus dependency, and go mod vendor to create vendor directory with dependencies. Then run `go run check.go` to execute the test.
// To test change name to main, then run `go run check.go`
func TestModbusIntegration() {
	modbusURL := strings.TrimSpace(os.Getenv("MODBUS_TEST_URL"))
	if modbusURL == "" {
		log.Print("MODBUS_TEST_URL не задан; интеграционная проверка пропущена")
		return
	}
	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     modbusURL,
		Timeout: modbusTimeout,
	})
	if err != nil {
		log.Fatal("Ошибка создания Modbus-клиента:", err)
	}

	err = openClientWithRetry(client, maxRetries, retryDelay)
	if err != nil {
		log.Fatal("Не удалось подключиться к Modbus после всех попыток:", err)
	}
	defer client.Close()

	_, params, err := loadDeviceParametersForModel(defaultInverterModelKey)
	if err != nil {
		log.Fatal("Ошибка чтения JSON:", err)
	}

	for _, param := range params {
		fields := param.Fields

		if !fields.IsActive {
			continue
		}

		registerType, ok := getRegisterType(fields.RegisterType)
		if !ok {
			log.Printf(
				"Пропускаю параметр %s: неподдерживаемый register_type=%s",
				fields.Code,
				fields.RegisterType,
			)
			continue
		}

		fmt.Println("Название:", fields.Name)
		fmt.Println("Код:", fields.Code)
		fmt.Println("Адрес Modbus:", fields.ModbusAddress)
		fmt.Println("Тип регистра:", fields.RegisterType)
		fmt.Println("Тип значения:", fields.Type)

		value, err := readRegisterWithRetry(
			client,
			fields.ModbusAddress,
			registerType,
			maxRetries,
			retryDelay,
		)
		if err != nil {
			log.Printf(
				"Не удалось прочитать %s address=%d после %d попыток: %v",
				fields.Code,
				fields.ModbusAddress,
				maxRetries,
				err,
			)

			fmt.Println("-----")
			continue
		}

		resultValue := decodeDeviceParameterValue(value, fields)

		suffix := ""
		if fields.Suffix != nil {
			suffix = *fields.Suffix
		}

		fmt.Printf("Значение сырое: %v\n", value)
		fmt.Printf("Значение итоговое: %.2f %s\n", resultValue, suffix)

		fmt.Println("-----")
	}
}
