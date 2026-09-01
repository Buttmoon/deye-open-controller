package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var logExportPreferredHeaders = []string{
	"timestamp", "inverter_timestamp", "inverter_time_status", "inverter_time_register_62", "inverter_time_register_63", "inverter_time_register_64",
	"Enabled", "SellModeKW", "SellModeBattCapacity", "ChargeMode", "GridExportLimit", "GridChargeEnabled", "SolarExport", "LoadLimitMode", "UseTimer", "PriorityLoad",
}

type inverterLogLine struct {
	Record map[string]any
	Raw    string
	Time   time.Time
}

func (a *App) apiInverterLogExportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "xlsx"
	}
	from, _ := parseLogTimeFilter(r.URL.Query().Get("from"))
	to, _ := parseLogTimeFilter(r.URL.Query().Get("to"))
	fields := requestedLogExportFields(r.URL.Query())
	selectedFile := strings.TrimSpace(r.URL.Query().Get("file"))
	items, headers, err := collectInverterLogRecords(from, to, fields, selectedFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка чтения логов: " + err.Error()})
		return
	}
	stamp := time.Now().Format("20060102-150405")
	switch format {
	case "txt", "log", "jsonl":
		name := "inverter-log-" + stamp + ".txt"
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		for _, item := range items {
			_, _ = w.Write([]byte(item.Raw + "\n"))
		}
	default:
		xlsxHeaders, rows := buildLogExportXLSXRows(items, headers)
		data, err := buildGenericXLSX("InverterLog", xlsxHeaders, rows)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка генерации XLSX"})
			return
		}
		name := "inverter-log-" + stamp + ".xlsx"
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		_, _ = w.Write(data)
	}
}

func (a *App) apiInverterLogRecordsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "message": "Метод не поддерживается"})
		return
	}

	from, hasFrom := parseLogTimeFilter(r.URL.Query().Get("from"))
	to, hasTo := parseLogTimeFilter(r.URL.Query().Get("to"))
	file := strings.TrimSpace(r.URL.Query().Get("file"))
	limitStr := strings.TrimSpace(r.URL.Query().Get("limit"))
	limit := 500
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}

	items, _, err := collectInverterLogRecords(from, to, nil, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "message": "Ошибка чтения логов: " + err.Error()})
		return
	}

	if len(items) > limit {
		items = items[len(items)-limit:]
	}

	records := make([]map[string]any, 0, len(items))
	for _, item := range items {
		records = append(records, item.Record)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"count":    len(records),
		"has_from": hasFrom,
		"has_to":   hasTo,
		"records":  records,
	})
}

func buildLogExportXLSXRows(items []inverterLogLine, headers []string) ([]string, [][]any) {
	columnInfo := logExportColumnInfoByCode()
	xlsxHeaders := []string{"Номер строки", "Дата", "Время"}
	for _, h := range headers {
		if h == "timestamp" {
			continue
		}
		xlsxHeaders = append(xlsxHeaders, logExportHeaderTitle(h, columnInfo))
	}
	rows := make([][]any, 0, len(items))
	for idx, item := range items {
		row := make([]any, 0, len(xlsxHeaders))
		datePart, timePart := splitLogDateTime(item.Record["timestamp"], item.Time)
		row = append(row, idx+1, datePart, timePart)
		for _, h := range headers {
			if h == "timestamp" {
				continue
			}
			row = append(row, formatLogCell(item.Record[h]))
		}
		rows = append(rows, row)
	}
	return xlsxHeaders, rows
}

func requestedLogExportFields(values map[string][]string) []string {
	rawValues := values["fields"]
	if len(rawValues) == 0 {
		return nil
	}
	out := make([]string, 0, len(rawValues))
	seen := map[string]bool{}
	for _, raw := range rawValues {
		for _, field := range splitCSVTrim(raw) {
			if seen[field] {
				continue
			}
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

func splitLogDateTime(raw any, parsed time.Time) (string, string) {
	if !parsed.IsZero() {
		return parsed.Format("2006-01-02"), parsed.Format("15:04:05")
	}
	text := strings.TrimSpace(fmt.Sprint(formatLogCell(raw)))
	if text == "" || text == "<nil>" {
		return "", ""
	}
	if t, ok := parseLogTimeFilter(text); ok {
		return t.Format("2006-01-02"), t.Format("15:04:05")
	}
	parts := strings.Fields(text)
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return text, ""
}

func splitCSVTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func parseLogTimeFilter(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	layouts := []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02", "02.01.06 15:04:05", "02.01.2006 15:04:05"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseLogRecordTime(record map[string]any) time.Time {
	for _, key := range []string{"timestamp", "inverter_timestamp", "generated_at_local", "generated_at_utc"} {
		if raw, ok := record[key].(string); ok {
			if t, ok := parseLogTimeFilter(raw); ok {
				return t
			}
		}
	}
	return time.Time{}
}

func collectInverterLogRecords(from time.Time, to time.Time, requested []string, selectedFile string) ([]inverterLogLine, []string, error) {
	files, err := listInverterLogFiles()
	if err != nil {
		return nil, nil, err
	}
	hasPeriod := !from.IsZero() || !to.IsZero()
	if selectedFile != "" {
		safeName, ok := safeInverterLogName(selectedFile)
		if !ok {
			return nil, nil, fmt.Errorf("некорректное имя файла")
		}
		found := false
		for _, f := range files {
			if f.Name == safeName {
				files = []InverterLogFileInfo{f}
				found = true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("файл %s не найден", safeName)
		}
	} else if !hasPeriod {
		if len(files) == 0 {
			return nil, nil, nil
		}
		files = files[:1]
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModifiedUTC < files[j].ModifiedUTC })
	items := []inverterLogLine{}
	union := map[string]bool{}
	for _, f := range files {
		path := filepath.Join(inverterLogDir, f.Name)
		fh, err := os.Open(path)
		if err != nil {
			continue
		}
		s := bufio.NewScanner(fh)
		buf := make([]byte, 1024, 1024*1024)
		s.Buffer(buf, 20*1024*1024)
		for s.Scan() {
			raw := strings.TrimSpace(s.Text())
			if raw == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(raw), &rec); err != nil {
				continue
			}
			t := parseLogRecordTime(rec)
			if !from.IsZero() && !t.IsZero() && t.Before(from) {
				continue
			}
			if !to.IsZero() && !t.IsZero() && t.After(to) {
				continue
			}
			for k := range rec {
				union[k] = true
			}
			items = append(items, inverterLogLine{Record: rec, Raw: raw, Time: t})
		}
		_ = fh.Close()
	}
	headers := requested
	if len(headers) == 0 {
		headers = defaultLogExportHeaders(union)
	}
	return items, headers, nil
}

func defaultLogExportHeaders(union map[string]bool) []string {
	headers := make([]string, 0, len(union))
	for _, h := range logExportPreferredHeaders {
		if union[h] {
			headers = append(headers, h)
			delete(union, h)
		}
	}
	for _, col := range buildInverterLogExportColumns() {
		if union[col.Code] {
			headers = append(headers, col.Code)
			delete(union, col.Code)
		}
	}
	rest := make([]string, 0, len(union))
	for h := range union {
		rest = append(rest, h)
	}
	sort.Strings(rest)
	headers = append(headers, rest...)
	return headers
}

func buildInverterLogExportColumns() []InverterLogExportColumn {
	modelsCatalog, err := availableInverterModels()
	if err != nil {
		return nil
	}
	columns := make([]InverterLogExportColumn, 0)
	seen := map[string]bool{}
	for _, model := range modelsCatalog {
		_, params, loadErr := loadDeviceParametersForModel(model.Key)
		if loadErr != nil {
			continue
		}
		for _, param := range params {
			code := strings.TrimSpace(param.Fields.Code)
			if code == "" || seen[code] {
				continue
			}
			seen[code] = true
			columns = append(columns, InverterLogExportColumn{
				Number: len(columns) + 1, Code: code, Name: strings.TrimSpace(param.Fields.Name),
				Description: strings.TrimSpace(param.Fields.Description), Checked: param.Fields.IsActive,
			})
		}
	}
	return columns
}

func activeInverterLogExportColumns() []InverterLogExportColumn {
	columns := buildInverterLogExportColumns()
	active := make([]InverterLogExportColumn, 0, len(columns))
	for _, col := range columns {
		if col.Checked {
			active = append(active, col)
		}
	}
	return active
}

func logExportColumnInfoByCode() map[string]InverterLogExportColumn {
	info := map[string]InverterLogExportColumn{}
	for _, col := range buildInverterLogExportColumns() {
		info[col.Code] = col
	}
	return info
}

func logExportHeaderTitle(code string, info map[string]InverterLogExportColumn) string {
	if col, ok := info[code]; ok {
		label := col.Name
		if label == "" {
			label = col.Code
		}
		return fmt.Sprintf("%d. %s", col.Number, label)
	}
	return code
}

func formatLogCell(v any) any {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return x
	case bool:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func buildGenericXLSX(sheetName string, headers []string, rows [][]any) ([]byte, error) {
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
	if err := writeZip("[Content_Types].xml", genericXLSXContentTypes()); err != nil {
		return nil, err
	}
	if err := writeZip("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`); err != nil {
		return nil, err
	}
	if err := writeZip("xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`); err != nil {
		return nil, err
	}
	if err := writeZip("xl/workbook.xml", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="%s" sheetId="1" r:id="rId1"/></sheets></workbook>`, xmlEscape(sanitizeSheetName(sheetName)))); err != nil {
		return nil, err
	}
	if err := writeZip("docProps/app.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><Application>inverter-schedule</Application></Properties>`); err != nil {
		return nil, err
	}
	if err := writeZip("docProps/core.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:creator>inverter-schedule</dc:creator></cp:coreProperties>`); err != nil {
		return nil, err
	}
	if err := writeZip("xl/worksheets/sheet1.xml", genericXLSXWorksheet(headers, rows)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func genericXLSXContentTypes() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`
}

func genericXLSXWorksheet(headers []string, rows [][]any) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	b.WriteString(`<row r="1">`)
	for i, h := range headers {
		b.WriteString(xlsxInlineCell(i+1, 1, h))
	}
	b.WriteString(`</row>`)
	for ri, row := range rows {
		rowNum := ri + 2
		b.WriteString(fmt.Sprintf(`<row r="%d">`, rowNum))
		for ci, v := range row {
			b.WriteString(xlsxValueCell(ci+1, rowNum, v))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}
