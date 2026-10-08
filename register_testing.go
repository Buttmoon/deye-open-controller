package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/simonvetter/modbus"
)

type registerTarget struct {
	InverterID            int64
	Name                  string
	IP                    string
	Port                  int
	Model                 InverterModelDefinition
	ProfileWriteConfirmed bool
}

func (a *App) loadRegisterTarget(inverterID int64) (registerTarget, error) {
	var t registerTarget
	var modelKey string
	var confirmed int
	err := a.db.QueryRow(`SELECT id, COALESCE(NULLIF(TRIM(name), ''), 'Инвертор ' || id), ip, port, model_key, COALESCE(profile_write_confirmed, 0) FROM inverters WHERE id = ?`, inverterID).
		Scan(&t.InverterID, &t.Name, &t.IP, &t.Port, &modelKey, &confirmed)
	if err != nil {
		return t, fmt.Errorf("инвертор не найден")
	}
	t.ProfileWriteConfirmed = confirmed == 1
	t.Model, err = findInverterModel(modelKey)
	return t, err
}

// gridPeakRegisterBlocked reports whether a write to this address is blocked
// because the grid peak shaving feature is disabled.
func (a *App) gridPeakRegisterBlocked(params []DeviceParameterFixture, address uint16) (bool, string) {
	if a.gridPeakFeatureEnabled() {
		return false, ""
	}
	for _, p := range params {
		if p.Fields.ModbusAddress == address && strings.HasPrefix(strings.ToLower(p.Fields.Code), "grid_peak_shaving") {
			return true, fmt.Sprintf("Запись в регистр %d (%s) заблокирована: функция «Ограничение мощности» отключена в настройках (раздел «Функции»)", address, p.Fields.Code)
		}
	}
	if address == 178 || address == 191 {
		return true, fmt.Sprintf("Запись в регистр %d заблокирована: функция «Ограничение мощности» отключена в настройках (раздел «Функции»)", address)
	}
	return false, ""
}

type RegisterTestPageData struct {
	Title         string
	Inverters     []registerTestInverter
	Models        []InverterModelDefinition
	WritesEnabled bool
	GridPeak      bool
}

type registerTestInverter struct {
	ID       int64
	Name     string
	Endpoint string
	ModelKey string
}

func (a *App) registerTestPageHandler(w http.ResponseWriter, r *http.Request) {
	data := RegisterTestPageData{Title: "Тестирование регистров", WritesEnabled: a.kvBool(kvFeatureRegisterTestWrites), GridPeak: a.gridPeakFeatureEnabled()}
	data.Models, _ = availableInverterModels()
	rows, err := a.db.Query(`SELECT id, COALESCE(NULLIF(TRIM(name), ''), 'Инвертор ' || id), ip, port, model_key FROM inverters ORDER BY id`)
	if err == nil {
		for rows.Next() {
			var i registerTestInverter
			var ip string
			var port int
			if rows.Scan(&i.ID, &i.Name, &ip, &port, &i.ModelKey) == nil {
				i.Endpoint = fmt.Sprintf("%s:%d", ip, port)
				i.ModelKey = normalizeInverterModelKey(i.ModelKey)
				data.Inverters = append(data.Inverters, i)
			}
		}
		rows.Close()
	}
	if err := a.tmplRegisterTest.Execute(w, data); err != nil {
		a.appendAppLog("error", "register test template error", map[string]any{"error": err.Error()})
	}
}

func (a *App) apiRegistersListHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	modelKey := q.Get("model_key")
	if id, _ := strconv.ParseInt(q.Get("inverter_id"), 10, 64); id > 0 {
		if t, err := a.loadRegisterTarget(id); err == nil {
			modelKey = t.Model.Key
		}
	}
	model, params, err := loadDeviceParametersForModelStructural(modelKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	var addrFilter *int
	if search != "" {
		if n, err := parseRegisterNumber(search); err == nil {
			addrFilter = intPtr(int(n))
		}
	}
	items := []RegisterDescription{}
	for _, p := range params {
		f := p.Fields
		if search != "" {
			hay := strings.ToLower(f.Code + " " + f.Name + " " + f.Description)
			addrMatch := false
			if addrFilter != nil {
				for _, ad := range deviceParameterAddresses(f) {
					if int(ad) == *addrFilter {
						addrMatch = true
					}
				}
			}
			if !addrMatch && !strings.Contains(hay, search) {
				continue
			}
		}
		if q.Get("writable") == "1" && !f.IsWritable {
			continue
		}
		d := describeRegisterField(model, params, f)
		d.CompatibleModels = nil
		items = append(items, d)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Address < items[j].Address })
	if code := strings.TrimSpace(q.Get("code")); code != "" {
		for _, p := range params {
			if strings.EqualFold(p.Fields.Code, code) {
				d := describeRegisterField(model, params, p.Fields)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": d})
				return
			}
		}
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Регистр не найден в профиле"})
		return
	}
	writeError := ""
	if _, _, err := loadDeviceParametersForModel(model.Key); err != nil {
		writeError = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model_key": model.Key, "model_name": model.Name, "file": model.ParametersFile,
		"items": items, "total": len(items), "issues": validateProfileStructure(params), "write_check_error": writeError})
}

type registerReadRequest struct {
	InverterID   int64  `json:"inverter_id"`
	Address      *int   `json:"address"`
	Count        int    `json:"count"`
	RegisterType string `json:"register_type"`
	Code         string `json:"code"`
}

type registerReadValue struct {
	Address int                    `json:"address"`
	Raw     uint16                 `json:"raw"`
	Hex     string                 `json:"hex"`
	Binary  string                 `json:"binary"`
	Signed  int16                  `json:"signed"`
	Fields  []registerFieldDecoded `json:"fields"`
}

type registerFieldDecoded struct {
	Code    string               `json:"code"`
	Name    string               `json:"name"`
	Kind    string               `json:"kind"`
	Decoded DecodedRegisterValue `json:"decoded"`
}

func fieldsStartingAt(params []DeviceParameterFixture, address uint16, regType string) []DeviceParameterFields {
	out := []DeviceParameterFields{}
	for _, p := range params {
		if p.Fields.ModbusAddress == address && strings.EqualFold(p.Fields.RegisterType, regType) {
			out = append(out, p.Fields)
		}
	}
	return out
}

func (a *App) apiRegisterReadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	var req registerReadRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
		return
	}
	t, err := a.loadRegisterTarget(req.InverterID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	_, params, _ := loadDeviceParametersForModelStructural(t.Model.Key)
	regTypeName := "holding"
	if req.Code != "" {
		found := false
		for _, p := range params {
			if strings.EqualFold(p.Fields.Code, req.Code) {
				req.Address = intPtr(int(p.Fields.ModbusAddress))
				req.Count = max(1, p.Fields.RegisterCount)
				if len(p.Fields.ModbusAddresses) > 1 {
					req.Count = 1
				}
				req.RegisterType = p.Fields.RegisterType
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "code не найден в профиле"})
			return
		}
	}
	if req.Address == nil || *req.Address < 0 || *req.Address > 65535 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Укажите адрес 0..65535"})
		return
	}
	if req.Count == 0 {
		req.Count = 1
	}
	if req.Count < 1 || req.Count > modbusMaxReadRegisters || *req.Address+req.Count-1 > 65535 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: fmt.Sprintf("Количество регистров 1..%d в пределах адресного пространства", modbusMaxReadRegisters)})
		return
	}
	regType, regTypeName, err := registerTypeFromString(req.RegisterType)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if !a.tryLockModbus(5 * time.Second) {
		writeJSON(w, http.StatusServiceUnavailable, jsonResponse{OK: false, Message: errModbusBusy})
		return
	}
	started := time.Now()
	blocks := readModbusBlocks(t.IP, t.Port, 1, 5*time.Second, 2, 300*time.Millisecond, []modbusBlockRead{{Address: uint16(*req.Address), Count: uint16(req.Count), RegisterType: regType}})
	a.modbusMu.Unlock()
	readAt := time.Now().UTC()
	b := blocks[0]
	entry := HistoryEntry{OperationType: opRegisterRead, InverterID: t.InverterID, Initiator: requestInitiator(r), RegisterAddress: intPtr(*req.Address),
		DurationMS: time.Since(started).Milliseconds(), Attempt: b.Attempts, Details: map[string]any{"count": req.Count, "register_type": regTypeName}}
	if b.Err != nil {
		entry.Status, entry.Error, entry.Message = histError, classifyModbusError(b.Err), "Чтение регистров не выполнено"
		a.recordHistory(entry)
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "message": "Чтение не удалось: " + classifyModbusError(b.Err), "attempts": b.Attempts})
		return
	}
	values := []registerReadValue{}
	for i, v := range b.Values {
		addr := uint16(*req.Address + i)
		rv := registerReadValue{Address: int(addr), Raw: v, Hex: fmt.Sprintf("0x%04X", v), Binary: formatBinary16(v), Signed: int16(v), Fields: []registerFieldDecoded{}}
		for _, f := range fieldsStartingAt(params, addr, regTypeName) {
			words := []uint16{v}
			if f.RegisterCount > 1 && len(f.ModbusAddresses) <= 1 && i+f.RegisterCount <= len(b.Values) {
				words = b.Values[i : i+f.RegisterCount]
			} else if f.RegisterCount > 1 {
				continue
			}
			ff := f
			rv.Fields = append(rv.Fields, registerFieldDecoded{Code: f.Code, Name: f.Name, Kind: inferRegisterValueKind(f), Decoded: decodeRegisterValue(words, &ff)})
		}
		values = append(values, rv)
		a.insertObservation(t.InverterID, t.Model.Key, int(addr), firstCode(params, addr), int(v), "read", "", "")
	}
	entry.Status = histInfo
	entry.Message = fmt.Sprintf("Прочитано регистров: %d", len(values))
	if len(values) == 1 {
		entry.VerifiedValue = intPtr(int(values[0].Raw))
		entry.RegisterCode = firstCode(params, uint16(*req.Address))
	}
	a.recordHistory(entry)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "read_at_utc": readAt, "duration_ms": entry.DurationMS, "attempts": b.Attempts, "register_type": regTypeName, "values": values, "live": true})
}

func firstCode(params []DeviceParameterFixture, address uint16) string {
	for _, p := range params {
		if p.Fields.ModbusAddress == address {
			return p.Fields.Code
		}
	}
	return ""
}

func (a *App) insertObservation(inverterID int64, modelKey string, address int, code string, raw int, kind, opID, note string) {
	_, _ = a.db.Exec(`INSERT INTO register_observations (ts_utc, inverter_id, model_key, register_address, register_code, raw_value, kind, operation_id, note) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), inverterID, modelKey, address, code, raw, kind, opID, note)
}

// registerWriteRequest describes one manual register write. Mode:
//   - "logical": Value is an engineering value encoded through the profile;
//   - "raw":     Value is the raw uint16 (only for fields without a write mode
//     or for addresses absent from the profile);
//   - "bits":    Bits sets individual documented, writable bits; all other
//     bits are preserved by read-modify-write.
type registerWriteRequest struct {
	InverterID      int64          `json:"inverter_id"`
	Code            string         `json:"code"`
	Address         *int           `json:"address"`
	Mode            string         `json:"mode"`
	Value           *float64       `json:"value"`
	Bits            map[string]int `json:"bits"`
	AllowUnknown    bool           `json:"allow_unknown"`
	Confirm         bool           `json:"confirm"`
	ConfirmAddress  *int           `json:"confirm_address"`
	ExpectedCurrent *int           `json:"expected_current"`
	PreviewToken    string         `json:"preview_token"`
	Note            string         `json:"note"`
}

type registerWritePlan struct {
	Target        registerTarget
	Field         *DeviceParameterFields
	Address       uint16
	Value         uint16
	Mask          uint16
	Mode          string
	Logical       *float64
	Description   string
	Warnings      []string
	UnknownAddr   bool
	ChangedBits   []DecodedBit
	ProfileParams []DeviceParameterFixture
}

func (a *App) planRegisterWrite(req registerWriteRequest) (registerWritePlan, int, error) {
	plan := registerWritePlan{Mode: strings.ToLower(strings.TrimSpace(req.Mode))}
	if !a.kvBool(kvFeatureRegisterTestWrites) {
		return plan, http.StatusForbidden, fmt.Errorf("Запись со страницы тестирования отключена в настройках (раздел «Функции»)")
	}
	t, err := a.loadRegisterTarget(req.InverterID)
	if err != nil {
		return plan, http.StatusBadRequest, err
	}
	plan.Target = t
	if t.Model.WriteRequiresConfirm && !t.ProfileWriteConfirmed {
		return plan, http.StatusForbidden, fmt.Errorf("Запись заблокирована: профиль %s требует read-only проверки и подтверждения записи в настройках инвертора", t.Model.Name)
	}
	_, params, err := loadDeviceParametersForModel(t.Model.Key)
	if err != nil {
		return plan, http.StatusConflict, fmt.Errorf("Профиль не прошёл проверку безопасности записи: %v", err)
	}
	plan.ProfileParams = params
	if req.Code != "" {
		for _, p := range params {
			if strings.EqualFold(p.Fields.Code, req.Code) {
				f := p.Fields
				plan.Field = &f
				break
			}
		}
		if plan.Field == nil {
			return plan, http.StatusBadRequest, fmt.Errorf("code=%s не найден в профиле", req.Code)
		}
		plan.Address = plan.Field.ModbusAddress
	} else if req.Address != nil && *req.Address >= 0 && *req.Address <= 65535 {
		plan.Address = uint16(*req.Address)
		writable := []DeviceParameterFields{}
		known := false
		for _, p := range params {
			if p.Fields.ModbusAddress == plan.Address && strings.EqualFold(p.Fields.RegisterType, "holding") {
				known = true
				if p.Fields.IsWritable && p.Fields.IsActive {
					writable = append(writable, p.Fields)
				}
			}
		}
		switch {
		case len(writable) == 1:
			f := writable[0]
			plan.Field = &f
		case len(writable) > 1:
			return plan, http.StatusBadRequest, fmt.Errorf("адрес %d описан несколькими записываемыми полями; укажите code", plan.Address)
		case known:
			return plan, http.StatusForbidden, fmt.Errorf("адрес %d описан в профиле только как read-only; запись заблокирована", plan.Address)
		default:
			plan.UnknownAddr = true
		}
	} else {
		return plan, http.StatusBadRequest, fmt.Errorf("укажите code или address 0..65535")
	}
	if len(deviceParameterAddresses(fieldOrEmpty(plan.Field))) > 1 && plan.Field != nil {
		return plan, http.StatusBadRequest, fmt.Errorf("многорегистровые значения не записываются со страницы тестирования")
	}
	if blocked, msg := a.gridPeakRegisterBlocked(params, plan.Address); blocked {
		return plan, http.StatusForbidden, fmt.Errorf("%s", msg)
	}
	if plan.UnknownAddr {
		if !req.AllowUnknown {
			return plan, http.StatusBadRequest, fmt.Errorf("адрес %d отсутствует в профиле; для исследовательской записи включите «разрешить неописанный адрес»", plan.Address)
		}
		if plan.Mode != "raw" || req.Value == nil || *req.Value < 0 || *req.Value > 65535 || *req.Value != math.Trunc(*req.Value) {
			return plan, http.StatusBadRequest, fmt.Errorf("для неописанного адреса допускается только raw-значение 0..65535")
		}
		plan.Value = uint16(*req.Value)
		plan.Description = fmt.Sprintf("raw-запись %d в неописанный адрес %d", plan.Value, plan.Address)
		plan.Warnings = append(plan.Warnings, "Адрес не описан в профиле: назначение и допустимые значения неизвестны. Результат будет сохранён как наблюдение.")
		return plan, 0, nil
	}
	f := *plan.Field
	if !f.IsWritable || !strings.EqualFold(f.RegisterType, "holding") {
		return plan, http.StatusForbidden, fmt.Errorf("code=%s не является записываемым holding-регистром", f.Code)
	}
	wm := strings.ToLower(strings.TrimSpace(f.WriteMode))
	switch plan.Mode {
	case "bits":
		if len(f.Bits) == 0 || f.WriteBitmask == nil {
			return plan, http.StatusBadRequest, fmt.Errorf("для code=%s биты не описаны; редактор битов недоступен", f.Code)
		}
		if len(req.Bits) == 0 {
			return plan, http.StatusBadRequest, fmt.Errorf("не выбраны биты для изменения")
		}
		var value, mask uint16
		for key, v := range req.Bits {
			bit, err := strconv.Atoi(key)
			if err != nil {
				return plan, http.StatusBadRequest, fmt.Errorf("некорректный номер бита %q", key)
			}
			var def *RegisterBitDef
			for i := range f.Bits {
				if f.Bits[i].Bit == bit {
					def = &f.Bits[i]
				}
			}
			if def == nil {
				return plan, http.StatusBadRequest, fmt.Errorf("бит %d не описан в профиле; неизвестные биты не изменяются", bit)
			}
			if !def.Writable || def.mask()&^*f.WriteBitmask != 0 {
				return plan, http.StatusForbidden, fmt.Errorf("бит %d (%s) не разрешён для записи профилем", bit, def.Name)
			}
			if v < 0 || v >= 1<<def.width() {
				return plan, http.StatusBadRequest, fmt.Errorf("бит %d: значение %d вне диапазона", bit, v)
			}
			if len(def.Values) > 0 && def.width() > 1 {
				if _, ok := def.Values[strconv.Itoa(v)]; !ok {
					return plan, http.StatusBadRequest, fmt.Errorf("биты %d–%d: значение %d не описано в профиле", def.Bit, def.Bit+def.width()-1, v)
				}
			}
			mask |= def.mask()
			value |= uint16(v<<def.Bit) & def.mask()
		}
		plan.Value, plan.Mask = value, mask
		plan.Description = fmt.Sprintf("изменение битов 0x%04X регистра %d (остальные биты сохраняются)", mask, plan.Address)
	case "logical", "":
		if req.Value == nil {
			return plan, http.StatusBadRequest, fmt.Errorf("укажите значение")
		}
		raw, err := encodeDeviceParameterValue(*req.Value, f)
		if err != nil {
			return plan, http.StatusBadRequest, fmt.Errorf("code=%s: %v", f.Code, err)
		}
		if wm == "mapped_masked_bits" {
			mapped, ok := f.WriteValues[strconv.FormatInt(int64(*req.Value), 10)]
			if !ok || *req.Value != math.Trunc(*req.Value) {
				return plan, http.StatusBadRequest, fmt.Errorf("code=%s: для значения %v нет безопасного кода в write_values", f.Code, *req.Value)
			}
			raw = mapped
		}
		plan.Value = raw
		plan.Logical = req.Value
		if wm == "masked_bits" || wm == "mapped_masked_bits" {
			plan.Mask = *f.WriteBitmask
		}
		plan.Mode = "logical"
		plan.Description = fmt.Sprintf("запись %v → raw %d (0x%04X)", *req.Value, raw, raw)
	case "raw":
		if wm != "" {
			return plan, http.StatusBadRequest, fmt.Errorf("code=%s использует режим %s; используйте логическое значение или редактор битов, чтобы сохранить остальные биты", f.Code, wm)
		}
		if req.Value == nil || *req.Value < 0 || *req.Value > 65535 || *req.Value != math.Trunc(*req.Value) {
			return plan, http.StatusBadRequest, fmt.Errorf("raw-значение должно быть целым 0..65535")
		}
		decoded, err := decodeDeviceParameterRegisters([]uint16{uint16(*req.Value)}, f)
		if err == nil {
			if enc, err2 := encodeDeviceParameterValue(decoded, f); err2 != nil || enc != uint16(*req.Value) {
				if err2 == nil {
					err2 = fmt.Errorf("каноническое raw=%d", enc)
				}
				return plan, http.StatusBadRequest, fmt.Errorf("raw=%v выходит за ограничения профиля: %v", *req.Value, err2)
			}
		}
		plan.Value = uint16(*req.Value)
		plan.Description = fmt.Sprintf("raw-запись %d (0x%04X)", plan.Value, plan.Value)
	default:
		return plan, http.StatusBadRequest, fmt.Errorf("неизвестный режим записи %q", req.Mode)
	}
	if confidenceLevel(f.Confidence) != "verified" {
		plan.Warnings = append(plan.Warnings, "Описание регистра не подтверждено на этом оборудовании (confidence: "+f.Confidence+").")
	}
	return plan, 0, nil
}

func fieldOrEmpty(f *DeviceParameterFields) DeviceParameterFields {
	if f == nil {
		return DeviceParameterFields{}
	}
	return *f
}

func previewToken(p registerWritePlan, current uint16) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%d|%d|%d", p.Target.InverterID, p.Address, p.Value, p.Mask, current)))
	return hex.EncodeToString(sum[:8])
}

func readSingleHolding(ip string, port int, address uint16) (uint16, error) {
	blocks := readModbusBlocks(ip, port, 1, 5*time.Second, 2, 300*time.Millisecond, []modbusBlockRead{{Address: address, Count: 1, RegisterType: modbus.HOLDING_REGISTER}})
	if blocks[0].Err != nil {
		return 0, blocks[0].Err
	}
	return blocks[0].Values[0], nil
}

func (a *App) apiRegisterWritePreviewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	var req registerWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
		return
	}
	plan, status, err := a.planRegisterWrite(req)
	if err != nil {
		writeJSON(w, status, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if !a.tryLockModbus(5 * time.Second) {
		writeJSON(w, http.StatusServiceUnavailable, jsonResponse{OK: false, Message: errModbusBusy})
		return
	}
	current, readErr := readSingleHolding(plan.Target.IP, plan.Target.Port, plan.Address)
	a.modbusMu.Unlock()
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, jsonResponse{OK: false, Message: "Не удалось прочитать текущее значение для предпросмотра: " + classifyModbusError(readErr)})
		return
	}
	target := plan.Value
	if plan.Mask != 0 {
		target = mergeMaskedRegisterValue(current, plan.Value, plan.Mask)
	}
	resp := map[string]any{
		"ok": true, "dry_run": true, "address": plan.Address, "description": plan.Description, "warnings": plan.Warnings, "unknown_address": plan.UnknownAddr,
		"current": decodeRegisterValue([]uint16{current}, plan.Field), "target": decodeRegisterValue([]uint16{target}, plan.Field),
		"current_raw": current, "target_raw": target, "mask": plan.Mask, "changed_bits": fmt.Sprintf("0x%04X", current^target),
		"no_change": current == target, "expected_current": current, "preview_token": previewToken(plan, current),
		"read_at_utc": time.Now().UTC(), "inverter": plan.Target.Name, "model": plan.Target.Model.Name,
	}
	a.recordHistory(HistoryEntry{OperationType: opRegisterWrite, Status: histDryRun, InverterID: plan.Target.InverterID, Initiator: requestInitiator(r),
		RegisterAddress: intPtr(int(plan.Address)), RegisterCode: fieldOrEmpty(plan.Field).Code, PreviousValue: intPtr(int(current)), RequestedValue: intPtr(int(target)),
		Message: "Предпросмотр записи (без записи в инвертор): " + plan.Description})
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) apiRegisterWriteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	var req registerWriteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
		return
	}
	plan, status, err := a.planRegisterWrite(req)
	if err != nil {
		writeJSON(w, status, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	if !req.Confirm || req.ConfirmAddress == nil || *req.ConfirmAddress != int(plan.Address) || req.ExpectedCurrent == nil || req.PreviewToken == "" {
		writeJSON(w, http.StatusPreconditionRequired, jsonResponse{OK: false, Message: "Запись требует предпросмотра и явного подтверждения: повторите адрес регистра в поле подтверждения"})
		return
	}
	expected := uint16(*req.ExpectedCurrent)
	if previewToken(plan, expected) != req.PreviewToken {
		writeJSON(w, http.StatusConflict, jsonResponse{OK: false, Message: "Параметры записи отличаются от предпросмотра; выполните предпросмотр заново"})
		return
	}
	if !a.tryLockModbus(10 * time.Second) {
		writeJSON(w, http.StatusServiceUnavailable, jsonResponse{OK: false, Message: errModbusBusy})
		return
	}
	out := writeRegisterVerified(registerWriteSpec{
		IP: plan.Target.IP, Port: plan.Target.Port, UnitID: 1, Address: plan.Address, Value: plan.Value, Mask: plan.Mask,
		ExpectedCurrent: &expected, Timeout: 5 * time.Second, VerifyReads: 3, VerifyDelay: 500 * time.Millisecond,
	})
	a.modbusMu.Unlock()

	code := fieldOrEmpty(plan.Field).Code
	histStatus := histError
	switch {
	case out.Verified:
		histStatus = histVerified
	case out.WriteAccepted:
		histStatus = histUnverified
	case out.ConcurrentChange:
		histStatus = histBlocked
	}
	opID := a.recordHistory(HistoryEntry{OperationType: opRegisterWrite, Status: histStatus, InverterID: plan.Target.InverterID, Initiator: requestInitiator(r),
		RegisterAddress: intPtr(int(plan.Address)), RegisterCode: code, PreviousValue: out.PreviousValue, RequestedValue: intPtr(int(plan.Value)),
		WrittenValue: writtenPtr(out), VerifiedValue: out.VerifiedValue, DurationMS: out.DurationMS, Error: out.Error, Attempt: 1,
		Message: "Ручная запись со страницы тестирования: " + plan.Description, Details: map[string]any{"mask": plan.Mask, "mode": plan.Mode, "attempts": out.Attempts, "note": req.Note, "unknown_address": plan.UnknownAddr}})
	if out.WriteAccepted {
		a.recordHistory(HistoryEntry{OperationType: opRegisterReadback, ParentID: opID, Status: map[bool]string{true: histVerified, false: histUnverified}[out.Verified],
			InverterID: plan.Target.InverterID, RegisterAddress: intPtr(int(plan.Address)), RegisterCode: code, WrittenValue: intPtr(int(out.WrittenValue)),
			VerifiedValue: out.VerifiedValue, Message: "Контрольное чтение после записи", Error: map[bool]string{true: "", false: out.Error}[out.Verified]})
	}
	kind := "write_rejected"
	if out.Verified {
		kind = "write_verified"
	} else if out.WriteAccepted {
		kind = "write_unverified"
	}
	if !out.ConcurrentChange && (out.WriteAccepted || out.PreviousValue != nil) {
		a.insertObservation(plan.Target.InverterID, plan.Target.Model.Key, int(plan.Address), code, int(out.WrittenValue), kind, opID, req.Note)
	}
	if out.VerifiedValue != nil {
		a.insertObservation(plan.Target.InverterID, plan.Target.Model.Key, int(plan.Address), code, *out.VerifiedValue, "read", opID, "контрольное чтение")
	}
	if out.Verified && plan.Field != nil && plan.Logical != nil {
		switch code {
		case "grid_peak_shaving_power":
			p := int(*plan.Logical)
			_ = a.saveStoredGridPeakState(plan.Target.InverterID, &p, nil)
		case "grid_peak_shaving_enabled":
			e := *plan.Logical == 1
			_ = a.saveStoredGridPeakState(plan.Target.InverterID, nil, &e)
		}
	}
	httpStatus := http.StatusOK
	if !out.Verified {
		httpStatus = http.StatusBadGateway
		if out.ConcurrentChange {
			httpStatus = http.StatusConflict
		}
	}
	msg := "Записано и подтверждено контрольным чтением"
	if !out.Verified {
		msg = out.Error
	}
	resp := map[string]any{"ok": out.Verified, "message": msg, "outcome": out, "operation_id": opID, "status": histStatus, "status_label": historyStatusLabels[histStatus]}
	if out.VerifiedValue != nil {
		resp["verified"] = decodeRegisterValue([]uint16{uint16(*out.VerifiedValue)}, plan.Field)
	}
	writeJSON(w, httpStatus, resp)
}

func writtenPtr(o registerWriteOutcome) *int {
	if !o.WriteAccepted && o.VerifiedValue == nil {
		return nil
	}
	return intPtr(int(o.WrittenValue))
}

type registerObservation struct {
	ID          int64  `json:"id"`
	TS          string `json:"ts_utc"`
	InverterID  int64  `json:"inverter_id"`
	ModelKey    string `json:"model_key"`
	Address     int    `json:"address"`
	Code        string `json:"code"`
	Raw         int    `json:"raw"`
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	Note        string `json:"note"`
}

type observationAnalysis struct {
	Address          int      `json:"address"`
	Observations     int      `json:"observations"`
	ReadValues       []int    `json:"read_values"`
	AcceptedValues   []int    `json:"accepted_values"`
	RejectedValues   []int    `json:"rejected_values"`
	UnverifiedValues []int    `json:"unverified_values"`
	Contradictions   []string `json:"contradictions"`
	Confidence       string   `json:"confidence"`
	ConfidenceLabel  string   `json:"confidence_label"`
	ProfileRange     string   `json:"profile_range,omitempty"`
}

func uniqSorted(in []int) []int {
	set := map[int]bool{}
	for _, v := range in {
		set[v] = true
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func analyzeObservations(address int, obs []registerObservation, field *DeviceParameterFields) observationAnalysis {
	an := observationAnalysis{Address: address, Observations: len(obs), Contradictions: []string{}}
	for _, o := range obs {
		switch o.Kind {
		case "read":
			an.ReadValues = append(an.ReadValues, o.Raw)
		case "write_verified", "accepted":
			an.AcceptedValues = append(an.AcceptedValues, o.Raw)
		case "write_rejected", "rejected":
			an.RejectedValues = append(an.RejectedValues, o.Raw)
		case "write_unverified":
			an.UnverifiedValues = append(an.UnverifiedValues, o.Raw)
		}
	}
	an.ReadValues, an.AcceptedValues, an.RejectedValues, an.UnverifiedValues = uniqSorted(an.ReadValues), uniqSorted(an.AcceptedValues), uniqSorted(an.RejectedValues), uniqSorted(an.UnverifiedValues)
	acc := map[int]bool{}
	for _, v := range an.AcceptedValues {
		acc[v] = true
	}
	for _, v := range an.RejectedValues {
		if acc[v] {
			an.Contradictions = append(an.Contradictions, fmt.Sprintf("значение %d и принималось, и отклонялось инвертором", v))
		}
	}
	for _, v := range an.UnverifiedValues {
		an.Contradictions = append(an.Contradictions, fmt.Sprintf("значение %d принято записью, но контрольное чтение показало другое", v))
	}
	if field != nil {
		if field.RawMin != nil && field.RawMax != nil {
			an.ProfileRange = fmt.Sprintf("%v..%v", *field.RawMin, *field.RawMax)
			for _, v := range append(append([]int{}, an.ReadValues...), an.AcceptedValues...) {
				bounded, check := rawValueForBounds(*field, uint16(v))
				if check && (float64(bounded) < *field.RawMin || float64(bounded) > *field.RawMax) {
					if field.Signed && v > 32767 {
						continue
					}
					an.Contradictions = append(an.Contradictions, fmt.Sprintf("наблюдалось значение %d вне диапазона профиля %s", v, an.ProfileRange))
				}
			}
		}
		if len(field.AllowedValues) > 0 && !allowedValuesSupersededByBitLayout(*field) {
			allowed := map[int]bool{}
			for _, v := range field.AllowedValues {
				allowed[v] = true
			}
			for _, v := range an.ReadValues {
				if !allowed[v] && field.WriteMode == "" {
					an.Contradictions = append(an.Contradictions, fmt.Sprintf("прочитано значение %d, которого нет в allowed_values профиля", v))
				}
			}
		}
	}
	confirmed := len(an.AcceptedValues)
	switch {
	case len(an.Contradictions) > 0:
		an.Confidence, an.ConfidenceLabel = "conflict", "Противоречивые данные"
	case confirmed >= 3:
		an.Confidence, an.ConfidenceLabel = "high", "Высокая (≥3 подтверждённых значений)"
	case confirmed >= 1:
		an.Confidence, an.ConfidenceLabel = "medium", "Средняя (есть подтверждённые записи)"
	case len(an.ReadValues) > 0:
		an.Confidence, an.ConfidenceLabel = "low", "Низкая (только чтения)"
	default:
		an.Confidence, an.ConfidenceLabel = "none", "Нет наблюдений"
	}
	return an
}

func (a *App) apiRegisterObservationsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		inverterID, _ := strconv.ParseInt(q.Get("inverter_id"), 10, 64)
		addr, err := parseRegisterNumber(q.Get("address"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		rows, err := a.db.Query(`SELECT id, ts_utc, COALESCE(inverter_id, 0), model_key, register_address, register_code, raw_value, kind, operation_id, note
			FROM register_observations WHERE register_address = ? AND (? = 0 OR inverter_id = ?) ORDER BY id DESC LIMIT 500`, int(addr), inverterID, inverterID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: err.Error()})
			return
		}
		items := []registerObservation{}
		for rows.Next() {
			var o registerObservation
			if rows.Scan(&o.ID, &o.TS, &o.InverterID, &o.ModelKey, &o.Address, &o.Code, &o.Raw, &o.Kind, &o.OperationID, &o.Note) == nil {
				items = append(items, o)
			}
		}
		rows.Close()
		var field *DeviceParameterFields
		if inverterID > 0 {
			if t, err := a.loadRegisterTarget(inverterID); err == nil {
				if _, params, err := loadDeviceParametersForModelStructural(t.Model.Key); err == nil {
					for _, p := range params {
						if p.Fields.ModbusAddress == addr && p.Fields.IsWritable {
							f := p.Fields
							field = &f
						}
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "analysis": analyzeObservations(int(addr), items, field)})
	case http.MethodPost:
		var req struct {
			InverterID int64  `json:"inverter_id"`
			Address    int    `json:"address"`
			Raw        int    `json:"raw"`
			Kind       string `json:"kind"`
			Note       string `json:"note"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Некорректный JSON"})
			return
		}
		if req.Kind != "note" && req.Kind != "accepted" && req.Kind != "rejected" {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "kind: note, accepted или rejected"})
			return
		}
		if req.Address < 0 || req.Address > 65535 || req.Raw < 0 || req.Raw > 65535 {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "адрес и значение 0..65535"})
			return
		}
		modelKey := ""
		if t, err := a.loadRegisterTarget(req.InverterID); err == nil {
			modelKey = t.Model.Key
		}
		a.insertObservation(req.InverterID, modelKey, req.Address, "", req.Raw, req.Kind, "", req.Note)
		writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Наблюдение сохранено"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
	}
}
