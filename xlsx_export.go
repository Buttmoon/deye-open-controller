package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type scheduleExportSheet struct {
	Name string
	JSON string
}

var scheduleExportHeaders = []string{
	"Day", "Time", "SellModeKW", "SellModeBattCapacity", "ChargeMode", "GridExportLimit", "GridChargeEnabled", "LoadLimitMode",
	"UseTimerMask", "UseTimerEnabled", "UseTimerMonday", "UseTimerTuesday", "UseTimerWednesday", "UseTimerThursday", "UseTimerFriday", "UseTimerSaturday", "UseTimerSunday", "PriorityLoad",
}

func (a *App) exportScheduleXLSXHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	ids := parseIDsCSV(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		if idStr := strings.TrimSpace(r.URL.Query().Get("id")); idStr != "" {
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil && id > 0 {
				ids = []int64{id}
			}
		}
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Не выбран инвертор для экспорта"})
		return
	}
	inverters, err := a.getInvertersByIDs(ids)
	if err != nil || len(inverters) == 0 {
		writeJSON(w, http.StatusNotFound, jsonResponse{OK: false, Message: "Инверторы не найдены"})
		return
	}
	sheets := make([]scheduleExportSheet, 0, len(inverters))
	for _, inv := range inverters {
		raw := defaultScheduleJSON()
		if s, err := a.getScheduleByInverterID(inv.ID); err == nil && strings.TrimSpace(s.ScheduleJSON) != "" {
			raw = s.ScheduleJSON
		}
		sheetName := strings.TrimSpace(inv.Name)
		if sheetName == "" {
			sheetName = fmt.Sprintf("Inverter %d", inv.ID)
		}
		sheets = append(sheets, scheduleExportSheet{Name: sheetName, JSON: raw})
	}
	data, err := buildSchedulesXLSX(sheets)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка генерации XLSX"})
		return
	}
	fileName := "schedule-" + time.Now().Format("20060102-150405") + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	_, _ = w.Write(data)
}

func buildSchedulesXLSX(sheets []scheduleExportSheet) ([]byte, error) {
	if len(sheets) == 0 {
		sheets = []scheduleExportSheet{{Name: "Schedule", JSON: defaultScheduleJSON()}}
	}
	buf := bytes.NewBuffer(nil)
	zw := zip.NewWriter(buf)
	writeZip := func(name, data string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write([]byte(data))
		return err
	}
	if err := writeZip("[Content_Types].xml", xlsxContentTypes(len(sheets))); err != nil {
		return nil, err
	}
	if err := writeZip("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`); err != nil {
		return nil, err
	}
	if err := writeZip("xl/_rels/workbook.xml.rels", xlsxWorkbookRels(len(sheets))); err != nil {
		return nil, err
	}
	if err := writeZip("xl/workbook.xml", xlsxWorkbookXML(sheets)); err != nil {
		return nil, err
	}
	if err := writeZip("docProps/app.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>inverter-schedule</Application></Properties>`); err != nil {
		return nil, err
	}
	if err := writeZip("docProps/core.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:creator>inverter-schedule</dc:creator></cp:coreProperties>`); err != nil {
		return nil, err
	}
	for i, sheet := range sheets {
		if err := writeZip(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), xlsxWorksheetXML(sheet.JSON)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func xlsxContentTypes(sheetCount int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	for i := 1; i <= sheetCount; i++ {
		b.WriteString(fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i))
	}
	b.WriteString(`</Types>`)
	return b.String()
}

func xlsxWorkbookRels(sheetCount int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= sheetCount; i++ {
		b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i, i))
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

func xlsxWorkbookXML(sheets []scheduleExportSheet) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	seen := map[string]int{}
	for i, sheet := range sheets {
		name := sanitizeSheetName(sheet.Name)
		seen[name]++
		if seen[name] > 1 {
			name = sanitizeSheetName(fmt.Sprintf("%s %d", name, seen[name]))
		}
		b.WriteString(fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscape(name), i+1, i+1))
	}
	b.WriteString(`</sheets></workbook>`)
	return b.String()
}

func xlsxWorksheetXML(scheduleJSON string) string {
	payload, err := parseSchedulePayloadLoose(scheduleJSON)
	if err != nil || len(payload.Days) == 0 {
		payload = defaultSchedulePayload()
	}
	normalizeSchedulePayloadSellTimes(&payload)
	var b strings.Builder
	b.Grow(1024 * 1024)
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	rowNum := 1
	b.WriteString(`<row r="1">`)
	for i, header := range scheduleExportHeaders {
		b.WriteString(xlsxInlineCell(i+1, rowNum, header))
	}
	b.WriteString(`</row>`)
	mask := normalizeUseTimerMask(payload.UseTimerMask)
	maskStr := strconv.Itoa(mask)
	boolMask := make([]string, 8)
	for i := 0; i < 8; i++ {
		if mask&(1<<i) != 0 {
			boolMask[i] = "TRUE"
		} else {
			boolMask[i] = "FALSE"
		}
	}
	colNames := make([]string, 19)
	for i := 1; i <= 18; i++ {
		colNames[i] = columnName(i)
	}
	chargeLabels := make(map[int]string)
	loadLabels := make(map[int]string)
	for _, day := range payload.Days {
		dayStr := strconv.Itoa(day.Day)
		for hi, hour := range day.Hours {
			rowNum++
			rowStr := strconv.Itoa(rowNum)
			cl := getOrCacheChargeLabel(chargeLabels, hour.ChargeMode)
			ll := getOrCacheLoadLabel(loadLabels, hour.LoadLimitMode)
			sellModeKW := strconv.Itoa(hour.SellModeKW)
			sellModeBatt := strconv.Itoa(hour.SellModeBattCapacity)
			gridExport := strconv.Itoa(hour.GridExportLimit)
			b.WriteString(`<row r="`)
			b.WriteString(rowStr)
			b.WriteString(`">`)
			writeCellInt(&b, colNames[1], rowStr, dayStr)
			writeCellStr(&b, colNames[2], rowStr, hourLabel(hi))
			writeCellInt(&b, colNames[3], rowStr, sellModeKW)
			writeCellInt(&b, colNames[4], rowStr, sellModeBatt)
			writeCellStr(&b, colNames[5], rowStr, cl)
			writeCellInt(&b, colNames[6], rowStr, gridExport)
			writeCellBool(&b, colNames[7], rowStr, hour.GridChargeEnabled)
			writeCellStr(&b, colNames[8], rowStr, ll)
			writeCellStr(&b, colNames[9], rowStr, maskStr)
			writeCellStr(&b, colNames[10], rowStr, boolMask[0])
			writeCellStr(&b, colNames[11], rowStr, boolMask[1])
			writeCellStr(&b, colNames[12], rowStr, boolMask[2])
			writeCellStr(&b, colNames[13], rowStr, boolMask[3])
			writeCellStr(&b, colNames[14], rowStr, boolMask[4])
			writeCellStr(&b, colNames[15], rowStr, boolMask[5])
			writeCellStr(&b, colNames[16], rowStr, boolMask[6])
			writeCellStr(&b, colNames[17], rowStr, boolMask[7])
			writeCellInt(&b, colNames[18], rowStr, strconv.Itoa(normalizePriorityLoadValue(hour.PriorityLoad)))
			b.WriteString(`</row>`)
			for si := hi * 12; si < (hi+1)*12 && si < len(day.Slots); si++ {
				slot := day.Slots[si]
				if slot.Minute == 0 {
					continue
				}
				if slot.SellModeKW == hour.SellModeKW &&
					slot.SellModeBattCapacity == hour.SellModeBattCapacity &&
					slot.ChargeMode == hour.ChargeMode &&
					slot.GridExportLimit == hour.GridExportLimit &&
					slot.GridChargeEnabled == hour.GridChargeEnabled &&
					slot.LoadLimitMode == hour.LoadLimitMode &&
					slot.PriorityLoad == hour.PriorityLoad {
					continue
				}
				rowNum++
				rowStr = strconv.Itoa(rowNum)
				scl := getOrCacheChargeLabel(chargeLabels, slot.ChargeMode)
				sll := getOrCacheLoadLabel(loadLabels, slot.LoadLimitMode)
				b.WriteString(`<row r="`)
				b.WriteString(rowStr)
				b.WriteString(`">`)
				writeCellInt(&b, colNames[1], rowStr, dayStr)
				writeCellStr(&b, colNames[2], rowStr, minuteSlotLabel(slot.Hour, slot.Minute))
				writeCellInt(&b, colNames[3], rowStr, strconv.Itoa(slot.SellModeKW))
				writeCellInt(&b, colNames[4], rowStr, strconv.Itoa(slot.SellModeBattCapacity))
				writeCellStr(&b, colNames[5], rowStr, scl)
				writeCellInt(&b, colNames[6], rowStr, strconv.Itoa(slot.GridExportLimit))
				writeCellBool(&b, colNames[7], rowStr, slot.GridChargeEnabled)
				writeCellStr(&b, colNames[8], rowStr, sll)
				writeCellStr(&b, colNames[9], rowStr, maskStr)
				writeCellStr(&b, colNames[10], rowStr, boolMask[0])
				writeCellStr(&b, colNames[11], rowStr, boolMask[1])
				writeCellStr(&b, colNames[12], rowStr, boolMask[2])
				writeCellStr(&b, colNames[13], rowStr, boolMask[3])
				writeCellStr(&b, colNames[14], rowStr, boolMask[4])
				writeCellStr(&b, colNames[15], rowStr, boolMask[5])
				writeCellStr(&b, colNames[16], rowStr, boolMask[6])
				writeCellStr(&b, colNames[17], rowStr, boolMask[7])
				writeCellInt(&b, colNames[18], rowStr, strconv.Itoa(normalizePriorityLoadValue(slot.PriorityLoad)))
				b.WriteString(`</row>`)
			}
		}
	}
	b.WriteString(`</sheetData>`)
	b.WriteString(xlsxScheduleDataValidations(rowNum))
	b.WriteString(`</worksheet>`)
	return b.String()
}

func getOrCacheChargeLabel(cache map[int]string, v int) string {
	if l, ok := cache[v]; ok {
		return l
	}
	l := chargeModeLabel(v)
	cache[v] = l
	return l
}

func getOrCacheLoadLabel(cache map[int]string, v int) string {
	if l, ok := cache[v]; ok {
		return l
	}
	l := loadLimitModeLabel(v)
	cache[v] = l
	return l
}

func writeCellInt(b *strings.Builder, col, rowStr, val string) {
	b.WriteString(`<c r="`)
	b.WriteString(col)
	b.WriteString(rowStr)
	b.WriteString(`"><v>`)
	b.WriteString(val)
	b.WriteString(`</v></c>`)
}

func writeCellStr(b *strings.Builder, col, rowStr, val string) {
	b.WriteString(`<c r="`)
	b.WriteString(col)
	b.WriteString(rowStr)
	b.WriteString(`" t="inlineStr"><is><t>`)
	b.WriteString(xmlEscape(val))
	b.WriteString(`</t></is></c>`)
}

func writeCellBool(b *strings.Builder, col, rowStr string, val bool) {
	b.WriteString(`<c r="`)
	b.WriteString(col)
	b.WriteString(rowStr)
	b.WriteString(`"><v>`)
	if val {
		b.WriteString(`1`)
	} else {
		b.WriteString(`0`)
	}
	b.WriteString(`</v></c>`)
}

func boolLabel(v bool) string {
	if v {
		return "TRUE"
	}
	return "FALSE"
}

func chargeModeLabel(v int) string {
	return chargeModeLabelFromValue(v)
}

func loadLimitModeLabel(v int) string {
	switch v {
	case 1:
		return "Zero export load"
	case 2:
		return "Zero export CT"
	default:
		return "Selling first"
	}
}

func xlsxScheduleDataValidations(lastRow int) string {
	if lastRow < 2 {
		lastRow = 745
	}
	boolList := `&quot;TRUE,FALSE&quot;`
	chargeList := `&quot;` + xmlEscape(strings.Join(chargeModeValidationLabels(), ",")) + `&quot;`
	loadLimitList := `&quot;Selling first,Zero export load,Zero export CT&quot;`
	validations := []struct{ sqref, formula string }{
		{fmt.Sprintf("E2:E%d", lastRow), chargeList},
		{fmt.Sprintf("G2:G%d", lastRow), boolList},
		{fmt.Sprintf("H2:H%d", lastRow), loadLimitList},
		{fmt.Sprintf("J2:Q%d", lastRow), boolList},
		{fmt.Sprintf("R2:R%d", lastRow), `&quot;0,1&quot;`},
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<dataValidations count="%d">`, len(validations)))
	for _, v := range validations {
		b.WriteString(fmt.Sprintf(`<dataValidation type="list" allowBlank="1" showErrorMessage="1" sqref="%s"><formula1>%s</formula1></dataValidation>`, v.sqref, v.formula))
	}
	b.WriteString(`</dataValidations>`)
	return b.String()
}

func xlsxValueCell(col, row int, value any) string {
	switch v := value.(type) {
	case int:
		return fmt.Sprintf(`<c r="%s%d"><v>%d</v></c>`, columnName(col), row, v)
	case bool:
		if v {
			return fmt.Sprintf(`<c r="%s%d"><v>1</v></c>`, columnName(col), row)
		}
		return fmt.Sprintf(`<c r="%s%d"><v>0</v></c>`, columnName(col), row)
	case string:
		return xlsxInlineCell(col, row, v)
	default:
		return xlsxInlineCell(col, row, fmt.Sprint(v))
	}
}

func xlsxInlineCell(col, row int, value string) string {
	return fmt.Sprintf(`<c r="%s%d" t="inlineStr"><is><t>%s</t></is></c>`, columnName(col), row, xmlEscape(value))
}

func columnName(idx int) string {
	name := ""
	for idx > 0 {
		idx--
		name = string(rune('A'+(idx%26))) + name
		idx /= 26
	}
	return name
}

func sanitizeSheetName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Schedule"
	}
	for _, bad := range []string{"/", "\\", "?", "*", "[", "]", ":"} {
		name = strings.ReplaceAll(name, bad, "-")
	}
	if len([]rune(name)) > 31 {
		runes := []rune(name)
		name = string(runes[:31])
	}
	return name
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	out := b.String()
	out = strings.ReplaceAll(out, "\"", "&quot;")
	out = strings.ReplaceAll(out, "'", "&apos;")
	return out
}
