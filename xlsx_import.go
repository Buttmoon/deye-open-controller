package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

type ImportedTemplate struct{ Name, ScheduleJSON string }

type workbookXML struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
}
type relsXML struct {
	Relationships []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}
type sstXML struct {
	SI []struct {
		T string `xml:"t"`
	} `xml:"si"`
}
type worksheetXML struct {
	Rows []struct {
		R    int `xml:"r,attr"`
		Cell []struct {
			Ref      string `xml:"r,attr"`
			Type     string `xml:"t,attr"`
			Value    string `xml:"v"`
			InlineIS struct {
				T string `xml:"t"`
			} `xml:"is"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
}

func ParseTemplatesFromXLSX(r io.Reader) ([]ImportedTemplate, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	wbData, err := read("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	var wb workbookXML
	if err := xml.Unmarshal(wbData, &wb); err != nil {
		return nil, err
	}
	relsData, err := read("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	var rels relsXML
	if err := xml.Unmarshal(relsData, &rels); err != nil {
		return nil, err
	}
	relMap := map[string]string{}
	for _, r := range rels.Relationships {
		relMap[r.ID] = path.Clean("xl/" + r.Target)
	}
	sharedStrings := []string{}
	if f, ok := files["xl/sharedStrings.xml"]; ok {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		var sst sstXML
		if xml.Unmarshal(data, &sst) == nil {
			for _, si := range sst.SI {
				sharedStrings = append(sharedStrings, si.T)
			}
		}
	}
	var results []ImportedTemplate
	for _, sheet := range wb.Sheets {
		if strings.EqualFold(strings.TrimSpace(sheet.Name), "README") {
			continue
		}
		target := relMap[sheet.ID]
		if target == "" {
			continue
		}
		wsData, err := read(target)
		if err != nil {
			continue
		}
		var ws worksheetXML
		if err := xml.Unmarshal(wsData, &ws); err != nil {
			continue
		}
		tplPayload := defaultSchedulePayload()
		header := map[int]string{}
		for _, row := range ws.Rows {
			cells := map[int]string{}
			for _, c := range row.Cell {
				col := columnIndex(c.Ref)
				val := c.Value
				switch c.Type {
				case "s":
					if idx, err := strconv.Atoi(strings.TrimSpace(c.Value)); err == nil && idx >= 0 && idx < len(sharedStrings) {
						val = sharedStrings[idx]
					}
				case "inlineStr":
					val = c.InlineIS.T
				}
				cells[col] = strings.TrimSpace(val)
			}
			if row.R == 1 {
				for idx, v := range cells {
					header[idx] = normalizeHeader(v)
				}
				continue
			}
			if len(cells) == 0 {
				continue
			}
			day, _ := atoiLoose(firstByHeader(cells, header, "day"))
			timeStr := firstByHeader(cells, header, "time")
			if timeStr == "" {
				timeStr = firstByHeader(cells, header, "hour")
			}
			hour, minute, err := parseTimeValue(timeStr)
			if err != nil || day < 1 || day > len(tplPayload.Days) || hour < 0 || hour > 23 {
				continue
			}
			idx := slotIndex(hour, minute)
			if idx < 0 || idx >= len(tplPayload.Days[day-1].Slots) {
				continue
			}

			applyXLSXUseTimerMask(&tplPayload, cells, header)
			dayItem := &tplPayload.Days[day-1]
			dayItem.Enabled = true

			if minute == 0 {
				hourCfg := &dayItem.Hours[hour]
				applyXLSXRowToHourConfig(hourCfg, cells, header, true, tplPayload.UseTimerMask)
				for m := 0; m < 60; m += 5 {
					slotIdx := slotIndex(hour, m)
					if slotIdx >= 0 && slotIdx < len(dayItem.Slots) {
						dayItem.Slots[slotIdx] = minuteSlotFromHourConfig(*hourCfg, m)
					}
				}
				continue
			}

			slotCfg := minuteSlotFromHourConfig(dayItem.Hours[hour], minute)
			applyXLSXRowToMinuteSlot(&slotCfg, cells, header, true, tplPayload.UseTimerMask)
			dayItem.Slots[idx] = slotCfg
		}
		anyEnabled := false
		for di := range tplPayload.Days {
			has := false
			for _, h := range tplPayload.Days[di].Hours {
				if h.Enabled {
					has = true
					anyEnabled = true
					break
				}
			}
			if !has {
				for _, s := range tplPayload.Days[di].Slots {
					if s.Enabled {
						has = true
						anyEnabled = true
						break
					}
				}
			}
			tplPayload.Days[di].Enabled = has
		}
		if tplPayload.UseTimerMask == 0 && anyEnabled {
			// Legacy import: if rows are enabled but UseTimer columns are empty, enable timer for all weekdays.
			tplPayload.UseTimerMask = 255
		}
		normalizeSchedulePayloadSellTimes(&tplPayload)
		b, _ := json.Marshal(tplPayload)
		results = append(results, ImportedTemplate{Name: sheet.Name, ScheduleJSON: string(b)})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results, nil
}

func applyXLSXUseTimerMask(payload *SchedulePayload, cells map[int]string, header map[int]string) {
	if payload == nil {
		return
	}
	if v, ok := atoiOpt(firstByHeader(cells, header, "usetimermask")); ok && v > 0 {
		payload.UseTimerMask = normalizeUseTimerMask(payload.UseTimerMask | v)
	}
	if firstByHeader(cells, header, "usetimerenabled") != "" || firstByHeader(cells, header, "usetimermonday") != "" {
		rowMask := useTimerMaskFromFlags(
			parseBoolCell(firstByHeader(cells, header, "usetimerenabled")),
			parseBoolCell(firstByHeader(cells, header, "usetimermonday")),
			parseBoolCell(firstByHeader(cells, header, "usetimertuesday")),
			parseBoolCell(firstByHeader(cells, header, "usetimerwednesday")),
			parseBoolCell(firstByHeader(cells, header, "usetimerthursday")),
			parseBoolCell(firstByHeader(cells, header, "usetimerfriday")),
			parseBoolCell(firstByHeader(cells, header, "usetimersaturday")),
			parseBoolCell(firstByHeader(cells, header, "usetimersunday")),
		)
		if rowMask > 0 {
			payload.UseTimerMask = normalizeUseTimerMask(payload.UseTimerMask | rowMask)
		}
	}
}

func applyXLSXRowToHourConfig(h *HourConfig, cells map[int]string, header map[int]string, defaultEnabled bool, useTimerMask int) {
	if h == nil {
		return
	}
	h.Enabled = defaultEnabled
	if enabledRaw := firstByHeader(cells, header, "enabled"); enabledRaw != "" {
		h.Enabled = parseBoolCell(enabledRaw)
	}
	if v, ok := atoiOpt(firstByHeader(cells, header, "sellmodekw")); ok {
		h.SellModeKW = v
	}
	if v, ok := atoiOpt(firstByHeader(cells, header, "sellmodebattcapacity")); ok {
		h.SellModeBattCapacity = v
	}
	if v, ok := parseChargeModeCell(firstByHeader(cells, header, "chargemode")); ok {
		h.ChargeMode = v
	}
	if v, ok := atoiOpt(firstByHeader(cells, header, "gridexportlimit")); ok {
		h.GridExportLimit = v
	}
	if v := firstByHeader(cells, header, "gridchargeenabled"); v != "" {
		h.GridChargeEnabled = parseBoolCell(v)
	}
	if v := firstByHeader(cells, header, "solarexport"); v != "" {
		h.SolarExport = parseBoolCell(v)
	}
	if v, ok := parseLoadLimitModeCell(firstByHeader(cells, header, "loadlimitmode")); ok {
		h.LoadLimitMode = v
	}
	h.UseTimerMask = normalizeUseTimerMask(useTimerMask)
	h.UseTimer = h.UseTimerMask&1 != 0 || parseBoolCell(firstByHeader(cells, header, "usetimer"))
	h.PriorityLoad = priorityLoadValueFromAny(firstByHeader(cells, header, "priorityload"))
}

func applyXLSXRowToMinuteSlot(s *MinuteSlot, cells map[int]string, header map[int]string, defaultEnabled bool, useTimerMask int) {
	if s == nil {
		return
	}
	s.Enabled = defaultEnabled
	if enabledRaw := firstByHeader(cells, header, "enabled"); enabledRaw != "" {
		s.Enabled = parseBoolCell(enabledRaw)
	}
	if v, ok := atoiOpt(firstByHeader(cells, header, "sellmodekw")); ok {
		s.SellModeKW = v
	}
	if v, ok := atoiOpt(firstByHeader(cells, header, "sellmodebattcapacity")); ok {
		s.SellModeBattCapacity = v
	}
	if v, ok := parseChargeModeCell(firstByHeader(cells, header, "chargemode")); ok {
		s.ChargeMode = v
	}
	if v, ok := atoiOpt(firstByHeader(cells, header, "gridexportlimit")); ok {
		s.GridExportLimit = v
	}
	if v := firstByHeader(cells, header, "gridchargeenabled"); v != "" {
		s.GridChargeEnabled = parseBoolCell(v)
	}
	if v := firstByHeader(cells, header, "solarexport"); v != "" {
		s.SolarExport = parseBoolCell(v)
	}
	if v, ok := parseLoadLimitModeCell(firstByHeader(cells, header, "loadlimitmode")); ok {
		s.LoadLimitMode = v
	}
	s.UseTimerMask = normalizeUseTimerMask(useTimerMask)
	s.UseTimer = s.UseTimerMask&1 != 0 || parseBoolCell(firstByHeader(cells, header, "usetimer"))
	s.PriorityLoad = priorityLoadValueFromAny(firstByHeader(cells, header, "priorityload"))
}

func parseChargeModeCell(v string) (int, bool) {
	if n, ok := atoiOpt(v); ok {
		return normalizeChargeModeValue(n), true
	}
	if value, ok := chargeModeValueFromLabel(v); ok {
		return value, true
	}
	s := strings.ToLower(strings.TrimSpace(v))
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Join(strings.Fields(s), " ")
	switch s {
	case "no grid or gen or sell", "no grid or gen", "no grid", "none", "нет":
		return 0, true
	case "allow grid", "grid":
		return 1, true
	case "allow gen", "gen":
		return 2, true
	case "allow gen and grid", "allow grid and gen", "allow gen & grid", "allow grid & gen", "gen and grid", "grid and gen", "gen & grid", "grid & gen":
		return 3, true
	case "sell":
		return 32, true
	case "sell and grid", "sell & grid", "sell grid":
		return 33, true
	case "sell and gen", "sell & gen", "sell gen":
		return 34, true
	case "sell and grid and gen", "sell & grid & gen", "sell and gen and grid", "sell & gen & grid", "sell grid gen":
		return 35, true
	default:
		return 0, false
	}
}

func parseLoadLimitModeCell(v string) (int, bool) {
	if n, ok := atoiOpt(v); ok {
		return n, true
	}
	s := strings.ToLower(strings.TrimSpace(v))
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Join(strings.Fields(s), " ")
	switch s {
	case "selling first", "allow export", "sellingfirst":
		return 0, true
	case "zero export load", "essentials":
		return 1, true
	case "zero export ct", "zero export", "zeroexportct":
		return 2, true
	default:
		return 0, false
	}
}

func columnIndex(cellRef string) int {
	cellRef = strings.ToUpper(cellRef)
	idx := 0
	for _, r := range cellRef {
		if r < 'A' || r > 'Z' {
			break
		}
		idx = idx*26 + int(r-'A'+1)
	}
	return idx
}
func normalizeHeader(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, " ", "")
	v = strings.ReplaceAll(v, "_", "")
	return v
}
func firstByHeader(cells map[int]string, header map[int]string, target string) string {
	target = normalizeHeader(target)
	for idx, name := range header {
		if name == target {
			return cells[idx]
		}
	}
	return ""
}
func parseTimeValue(v string) (int, int, error) {
	v = strings.TrimSpace(v)
	if strings.Contains(v, ":") {
		parts := strings.Split(v, ":")
		hour, err := atoiLoose(parts[0])
		if err != nil {
			return 0, 0, err
		}
		minute := 0
		if len(parts) > 1 {
			m, err := atoiLoose(parts[1])
			if err != nil {
				return 0, 0, err
			}
			minute = m
		}
		minute = (minute / 5) * 5
		return hour, minute, nil
	}
	hour, err := atoiLoose(v)
	return hour, 0, err
}

func parseHourValue(v string) (int, error) {
	hour, _, err := parseTimeValue(v)
	return hour, err
}
func atoiLoose(v string) (int, error) {
	v = strings.TrimSpace(v)
	if strings.Contains(v, ".") {
		f, err := strconv.ParseFloat(v, 64)
		return int(f), err
	}
	return strconv.Atoi(v)
}
func atoiOpt(v string) (int, bool) {
	if strings.TrimSpace(v) == "" {
		return 0, false
	}
	n, err := atoiLoose(v)
	return n, err == nil
}
