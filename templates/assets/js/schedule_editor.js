(function(){
  const scheduleJSONEl = document.getElementById('scheduleJSON');
  if (!scheduleJSONEl) return;

  const listView = document.getElementById('listView');
  const gridView = document.getElementById('gridView');
  const viewModeEl = document.getElementById('viewMode');
  const listModeBtn = document.getElementById('listModeBtn');
  const gridModeBtn = document.getElementById('gridModeBtn');
  const modal = document.getElementById('dayModal');
  const modalEditor = document.getElementById('modalDayEditor');
  const modalDayTitle = document.getElementById('modalDayTitle');
  let openModalDayIndex = -1;
  let jsonDirty = true;

  const useTimerLabels = [
    { bit: 0, label: 'Вкл', hint: 'Time Of Use enabled' },
    { bit: 1, label: 'Пн', hint: 'Monday' },
    { bit: 2, label: 'Вт', hint: 'Tuesday' },
    { bit: 3, label: 'Ср', hint: 'Wednesday' },
    { bit: 4, label: 'Чт', hint: 'Thursday' },
    { bit: 5, label: 'Пт', hint: 'Friday' },
    { bit: 6, label: 'Сб', hint: 'Saturday' },
    { bit: 7, label: 'Вс', hint: 'Sunday' }
  ];
  const canonicalChargeModeOptions = [
    { value: 0, label: 'No Grid or Gen or Sell' },
    { value: 1, label: 'Allow Grid' },
    { value: 2, label: 'Allow Gen' },
    { value: 3, label: 'Allow Grid & Gen' },
    { value: 32, label: 'Sell' },
    { value: 33, label: 'Sell & Grid' },
    { value: 34, label: 'Sell & Gen' },
    { value: 35, label: 'Sell & Grid & Gen' }
  ];
  const canonicalChargeModeValues = new Set(canonicalChargeModeOptions.map(option => option.value));
  const configuredChargeModeOptions = Array.isArray(window.chargeModeOptions)
    ? window.chargeModeOptions
      .map(item => ({ value: Number(item.value), label: String(item.label || item.value).trim() }))
      .filter(item => canonicalChargeModeValues.has(item.value) && item.label)
    : [];
  const chargeModeOptions = configuredChargeModeOptions.length
    ? configuredChargeModeOptions
    : canonicalChargeModeOptions;
  const allowedChargeModeValues = new Set(chargeModeOptions.map(option => option.value));
  const chargeModeSupportsSell = chargeModeOptions.some(option => (option.value & 32) !== 0);
  const schedulePowerMaxW = Math.max(0, intValue(window.schedulePowerMaxW, 655350));
  const gridExportMaxW = Math.max(0, intValue(window.gridExportMaxW, schedulePowerMaxW || 655350));
  const schedulePowerMaxRaw = Math.floor(schedulePowerMaxW / 10);
  const gridExportMaxRaw = Math.floor(gridExportMaxW / 10);
  const scheduleCalendarDate = new Date();
  const scheduleCalendarYear = scheduleCalendarDate.getFullYear();
  const scheduleCalendarMonth = scheduleCalendarDate.getMonth();

  function pad(n){ return String(n).padStart(2, '0'); }
  function hourLabel(hour){ return `${pad(hour)}:00`; }
  function slotLabel(hour, minute){ return `${pad(hour)}:${pad(minute)}`; }
  function sellTimeForHour(hour){ return Number(hour) * 100; }
  function sellTimeForSlot(hour, minute){ return Number(hour) * 100 + Number(minute); }
  function pointForHour(hour){
    if (hour >= 0 && hour < 5) return 1;
    if (hour >= 5 && hour < 9) return 2;
    if (hour >= 9 && hour < 13) return 3;
    if (hour >= 13 && hour < 17) return 4;
    if (hour >= 17 && hour < 21) return 5;
    return 6;
  }
  function slotIndex(hour, minute){ return hour * 12 + Math.floor(minute / 5); }
  function normalizeUseTimerMask(mask){
    mask = Number(mask || 0);
    if (!Number.isFinite(mask) || mask < 0) return 0;
    if (mask > 255) return 255;
    return Math.trunc(mask);
  }
  function valueOrDefault(value, fallback){ return value === undefined || value === null ? fallback : value; }
  function normalizeText(value){ return String(valueOrDefault(value, '')).trim().toLowerCase().replace(/[_-]+/g, ' ').replace(/\s+/g, ' '); }
  function intValue(value, fallback = 0){
    if (typeof value === 'number' && Number.isFinite(value)) return Math.trunc(value);
    const s = String(valueOrDefault(value, '')).trim();
    if (!s) return fallback;
    const n = Number(s.includes(':') ? s.split(':')[0] : s);
    return Number.isFinite(n) ? Math.trunc(n) : fallback;
  }
  function boolValue(value, fallback = false){
    if (typeof value === 'boolean') return value;
    if (typeof value === 'number') return value !== 0;
    const s = normalizeText(value);
    if (!s) return fallback;
    return ['1','true','yes','y','on','вкл','да','истина','enabled','enable'].includes(s);
  }
  function priorityLoadValue(value, fallback = 0){
    if (typeof value === 'boolean') return value ? 1 : 0;
    if (typeof value === 'number' && Number.isFinite(value)) return value !== 0 ? 1 : 0;
    const s = normalizeText(value);
    if (!s) return fallback ? 1 : 0;
    if (['1','true','yes','y','on','вкл','да','истина','enabled','enable','battery first','batteryfirst','battery','battery priority'].includes(s)) return 1;
    if (['0','false','no','n','off','выкл','нет','ложь','disabled','disable','load first','loadfirst','load','load priority'].includes(s)) return 0;
    return boolValue(value, Boolean(fallback)) ? 1 : 0;
  }
  function chargeModeValue(value){
    const s = normalizeText(value);
    const byLabel = chargeModeOptions.find(option => normalizeText(option.label) === s);
    if (byLabel) return byLabel.value;
    const n = intValue(value, 0);
    return allowedChargeModeValues.has(n) ? n : 0;
  }
  function loadLimitModeValue(value){
    const s = normalizeText(value);
    if (s === '1' || s === 'zero export load' || s === 'zeroexportload' || s === 'essentials') return 1;
    if (s === '2' || s === 'zero export ct' || s === 'zeroexportct' || s === 'zero export' || s === 'zeroexport') return 2;
    if (s === 'selling first' || s === 'sellingfirst' || s === 'allow export' || s === 'allowexport') return 0;
    const n = intValue(value, 0);
    return Math.max(0, Math.min(2, n));
  }
  function clamp(v, min, max){ return Math.max(min, Math.min(max, v)); }
  function displaySellModeKw(value){ return Math.max(0, intValue(value, 0)) * 10; }
  function rawSellModeKw(value){ return Math.max(0, intValue(value, 0)); }
  function displayGridExportLimit(value){ return Math.max(0, intValue(value, 0)) * 10; }
  function rawGridExportLimit(value){ return Math.max(0, intValue(value, 0)); }
  function useTimerEnabled(){ return (normalizeUseTimerMask(scheduleData.use_timer_mask) & 1) !== 0; }
  function maskChecked(bit){ return (normalizeUseTimerMask(scheduleData.use_timer_mask) & (1 << bit)) !== 0; }
  function maskSummary(mask){
    const selected = useTimerLabels.filter(x => (mask & (1 << x.bit)) !== 0).map(x => x.label);
    return selected.length ? selected.join(', ') : 'не выбрано';
  }
  function weekdayBitFromJSWeekday(jsWeekday){ return jsWeekday === 0 ? 7 : jsWeekday; }
  function weekdayLabelFromBit(bit){
    const item = useTimerLabels.find(x => x.bit === bit);
    return item ? item.label : '';
  }
  function dayCalendarInfo(dayNumber){
    const d = new Date(scheduleCalendarYear, scheduleCalendarMonth, Number(dayNumber || 1));
    const valid = d.getFullYear() === scheduleCalendarYear && d.getMonth() === scheduleCalendarMonth && d.getDate() === Number(dayNumber || 0);
    const bit = valid ? weekdayBitFromJSWeekday(d.getDay()) : 0;
    return { valid, bit, label: weekdayLabelFromBit(bit), weekend: bit === 6 || bit === 7 };
  }
  function isDayActiveByMask(dayNumber, mask){
    const info = dayCalendarInfo(dayNumber);
    if ((mask & 1) === 0) return false;
    if (!info.valid) return true;
    return info.bit > 0 && (mask & (1 << info.bit)) !== 0;
  }
  function chargeModeFlags(value){
    const n = chargeModeValue(value);
    return { sell: (n & 32) !== 0, grid: (n & 1) !== 0, gen: (n & 2) !== 0 };
  }
  function chargeModeFromFlags(flags){
    let value = 0;
    if (flags.sell) value += 32;
    if (flags.grid) value += 1;
    if (flags.gen) value += 2;
    return allowedChargeModeValues.has(value) ? value : 0;
  }
  function chargeModeChecksHTML(scope, dayIndex, hour, minute, value){
    const flags = chargeModeFlags(value);
    const minuteAttrs = minute === null || minute === undefined ? '' : ` data-minute="${minute}"`;
    const sellControl = chargeModeSupportsSell
      ? `<label class="mode-check"><input type="checkbox" class="charge-mode-bool" data-mode="sell" ${flags.sell ? 'checked' : ''}> Sell</label>`
      : '';
    return `<div class="mode-checkboxes" data-scope="${scope}" data-day="${dayIndex}" data-hour="${hour}"${minuteAttrs}>
      ${sellControl}
      <label class="mode-check"><input type="checkbox" class="charge-mode-bool" data-mode="grid" ${flags.grid ? 'checked' : ''}> Grid</label>
      <label class="mode-check"><input type="checkbox" class="charge-mode-bool" data-mode="gen" ${flags.gen ? 'checked' : ''}> Gen</label>
    </div>`;
  }
  function prioritySelectHTML(scopeClass, dayIndex, hour, minute, value){
    const minuteAttrs = minute === null || minute === undefined ? '' : ` data-minute="${minute}"`;
    const n = priorityLoadValue(value, 0);
    return `<select class="${scopeClass}" data-day="${dayIndex}" data-hour="${hour}"${minuteAttrs} data-field="priority_load">
      <option value="0" ${n === 0 ? 'selected' : ''}>load first</option>
      <option value="1" ${n === 1 ? 'selected' : ''}>battery first</option>
    </select>`;
  }


  function minutesFromSellTime(sellTime){
    const hour = Math.max(0, Math.min(23, Math.floor(Number(sellTime || 0) / 100)));
    const minute = Math.max(0, Math.min(59, Math.floor(Number(sellTime || 0) % 100)));
    return hour * 60 + minute;
  }
  function pointFillOrder(currentPoint){
    const points = [];
    for (let point = 1; point <= 6; point++) if (point !== currentPoint) points.push(point);
    points.sort((a, b) => {
      const da = Math.abs(a - currentPoint);
      const db = Math.abs(b - currentPoint);
      if (da !== db) return da - db;
      return a - b;
    });
    return points;
  }
  function slotToSendCandidate(slot, fromCustom){
    const minute = Number(slot.minute || 0);
    const hour = Number(slot.hour || 0);
    const sellTime = sellTimeForSlot(hour, minute);
    return {
      ...slot,
      hour,
      minute,
      point: pointForHour(hour),
      sell_time: sellTime,
      label: slotLabel(hour, minute),
      _minutes: minutesFromSellTime(sellTime),
      _fromCustom: !!fromCustom
    };
  }
  function hourToSendCandidate(hour){
    const h = Number(hour.hour || 0);
    const sellTime = sellTimeForSlot(h, 0);
    return {
      ...hour,
      hour: h,
      minute: 0,
      point: pointForHour(h),
      sell_time: sellTime,
      label: slotLabel(h, 0),
      _minutes: minutesFromSellTime(sellTime),
      _fromCustom: false
    };
  }
  function collectSendCandidates(day){
    const byTime = new Map();
    (day.hours || []).forEach(hour => {
      if (!hour.enabled) return;
      const c = hourToSendCandidate(hour);
      byTime.set(c.sell_time, c);
    });
    (day.slots || []).forEach(slot => {
      const hour = day.hours && day.hours[Number(slot.hour || 0)];
      if (!slot.enabled || !hour || Number(slot.minute || 0) === 0 || isDefaultCustomSlot(slot, hour)) return;
      const c = slotToSendCandidate(slot, true);
      byTime.set(c.sell_time, c);
    });
    (day.customSlots || []).forEach(slot => {
      const hour = day.hours && day.hours[Number(slot.hour || 0)];
      if (!slot.enabled || !hour || Number(slot.minute || 0) === 0 || isDefaultCustomSlot(slot, hour)) return;
      const c = slotToSendCandidate(slot, true);
      byTime.set(c.sell_time, c);
    });
    return Array.from(byTime.values()).sort((a, b) => a._minutes - b._minutes);
  }
  function currentSlotForPreview(day, now){
    const h = now.getHours();
    const m = Math.floor(now.getMinutes() / 5) * 5;
    const hour = day.hours && day.hours[h] ? day.hours[h] : makeDefaultHour(h, {});
    const idx = slotIndex(h, m);
    const slot = day.slots && day.slots[idx] ? day.slots[idx] : null;
    if (m !== 0 && slot && slot.enabled && !isDefaultCustomSlot(slot, hour)) {
      return slotToSendCandidate(slot, true);
    }
    return hourToSendCandidate(hour);
  }
  function minuteSlotFromHourForPreview(hour, minute){
    return {
      ...hour,
      hour: Number(hour.hour || 0),
      minute,
      label: slotLabel(Number(hour.hour || 0), minute),
      point: pointForHour(Number(hour.hour || 0)),
      sell_time: sellTimeForSlot(Number(hour.hour || 0), minute)
    };
  }
  function buildPointPreview(day, current){
    const totalPoints = 6;
    const preferredBeforeCount = 2;
    const currentMinutes = minutesFromSellTime(current.sell_time);
    const before = [];
    const after = [];
    collectSendCandidates(day).forEach(c => {
      if (c.sell_time === current.sell_time) return;
      if (c._minutes < currentMinutes) before.push(c);
      if (c._minutes > currentMinutes) after.push(c);
    });
    before.sort((a, b) => {
      if (a._minutes !== b._minutes) return b._minutes - a._minutes;
      return b.sell_time - a.sell_time;
    });
    after.sort((a, b) => {
      if (a._minutes !== b._minutes) return a._minutes - b._minutes;
      return a.sell_time - b.sell_time;
    });

    let beforeCount = Math.min(preferredBeforeCount, before.length);
    let afterCount = Math.min(totalPoints - 1 - beforeCount, after.length);
    let remaining = totalPoints - 1 - beforeCount - afterCount;
    if (remaining > 0) {
      const extraBefore = Math.min(remaining, before.length - beforeCount);
      beforeCount += extraBefore;
      remaining -= extraBefore;
    }
    if (remaining > 0) {
      const extraAfter = Math.min(remaining, after.length - afterCount);
      afterCount += extraAfter;
      remaining -= extraAfter;
    }

    const selected = [];
    before.slice(0, beforeCount).reverse().forEach(c => selected.push({...c, _reason: 'ближайшая заполненная точка до текущего времени'}));
    selected.push({...current, _reason: current._fromCustom ? 'текущий заполненный 5-минутный слот' : 'текущая часовая точка'});
    after.slice(0, afterCount).forEach(c => selected.push({...c, _reason: 'ближайшая заполненная точка после текущего времени'}));
    while (selected.length < totalPoints) selected.push({...current, _reason: 'резерв: текущее значение'});
    return selected.slice(0, totalPoints).map((row, index) => ({...row, point: index + 1}));
  }
  function renderSendPreview(){
    const panel = document.getElementById('sendPreviewPanel');
    if (!panel) return;
    const now = new Date();
    const dayNumber = Math.max(1, Math.min(31, now.getDate()));
    const dayIndex = dayNumber - 1;
    const day = scheduleData.days[dayIndex] || scheduleData.days[0];
    if (!day) { panel.innerHTML = ''; return; }
    const current = currentSlotForPreview(day, now);
    const rows = buildPointPreview(day, current);
    panel.innerHTML = `<div class="send-preview-head">
      <div>
        <h3>Что будет отправлено сейчас</h3>
        <div class="muted">День ${day.day}, текущее время ${slotLabel(current.hour, current.minute)}, режим: ${current._fromCustom ? 'заполненный 5-минутный слот' : 'часовая точка'}. Точки берутся только в рамках текущих суток: до текущего времени — ближайшие предыдущие, затем текущая точка, после — ближайшие следующие.</div>
      </div>
      <div class="send-preview-badge">Use Timer: ${maskSummary(normalizeUseTimerMask(scheduleData.use_timer_mask))}</div>
    </div>
    <div class="send-preview-grid">
      ${rows.map(row => `<div class="send-point-card ${row.sell_time === current.sell_time ? 'active' : ''}">
        <div class="send-point-title">Point ${row.point}</div>
        <div class="send-point-time">${row.label}</div>
        <div class="send-point-metrics">
          <span>Power: <b>${displaySellModeKw(row.sell_mode_kw)} W</b></span>
          <span>SOC: <b>${row.sell_mode_batt_capacity}%</b></span>
          <span>Charge: <b>${row.charge_mode}</b></span>
        </div>
        <div class="muted send-point-reason">${row._reason}${row._fromCustom ? ' · кастомный слот' : ''}</div>
      </div>`).join('')}
    </div>`;
  }

  function makeDefaultHour(hour, source) {
    source = source || {};
    return {
      hour,
      label: hourLabel(hour),
      point: pointForHour(hour),
      sell_time: sellTimeForHour(hour),
      enabled: true,
      sell_mode_kw: Math.max(0, intValue(source.sell_mode_kw, 0)),
      sell_mode_batt_capacity: clamp(intValue(source.sell_mode_batt_capacity, 100), 0, 100),
      charge_mode: chargeModeValue(valueOrDefault(source.charge_mode, 0)),
      grid_export_limit: Math.max(0, intValue(source.grid_export_limit, 0)),
      grid_charge_enabled: boolValue(source.grid_charge_enabled, false),
      solar_export: boolValue(source.solar_export, false),
      load_limit_mode: loadLimitModeValue(valueOrDefault(source.load_limit_mode, 0)),
      use_timer: boolValue(source.use_timer, false),
      use_timer_mask: normalizeUseTimerMask(valueOrDefault(source.use_timer_mask, 0)),
      priority_load: priorityLoadValue(source.priority_load, 0)
    };
  }

  function makeDefaultSlot(hour, minute, source) {
    source = source || {};
    return {
      hour,
      minute,
      label: slotLabel(hour, minute),
      point: pointForHour(hour),
      sell_time: sellTimeForSlot(hour, minute),
      enabled: true,
      sell_mode_kw: Math.max(0, intValue(source.sell_mode_kw, 0)),
      sell_mode_batt_capacity: clamp(intValue(source.sell_mode_batt_capacity, 100), 0, 100),
      charge_mode: chargeModeValue(valueOrDefault(source.charge_mode, 0)),
      grid_export_limit: Math.max(0, intValue(source.grid_export_limit, 0)),
      grid_charge_enabled: boolValue(source.grid_charge_enabled, false),
      solar_export: boolValue(source.solar_export, false),
      load_limit_mode: loadLimitModeValue(valueOrDefault(source.load_limit_mode, 0)),
      use_timer: boolValue(source.use_timer, false),
      use_timer_mask: normalizeUseTimerMask(valueOrDefault(source.use_timer_mask, 0)),
      priority_load: priorityLoadValue(source.priority_load, 0),
      default_custom_slot: boolValue(valueOrDefault(source.default_custom_slot, source.is_default_custom_slot), false)
    };
  }

  function slotsEqual(a, b) {
    if (!a || !b) return false;
    return intValue(a.sell_mode_kw, 0) === intValue(b.sell_mode_kw, 0) &&
      intValue(a.sell_mode_batt_capacity, 100) === intValue(b.sell_mode_batt_capacity, 100) &&
      chargeModeValue(valueOrDefault(a.charge_mode, 0)) === chargeModeValue(valueOrDefault(b.charge_mode, 0)) &&
      intValue(a.grid_export_limit, 0) === intValue(b.grid_export_limit, 0) &&
      boolValue(a.grid_charge_enabled, false) === boolValue(b.grid_charge_enabled, false) &&
      boolValue(a.solar_export, false) === boolValue(b.solar_export, false) &&
      loadLimitModeValue(valueOrDefault(a.load_limit_mode, 0)) === loadLimitModeValue(valueOrDefault(b.load_limit_mode, 0)) &&
      priorityLoadValue(a.priority_load, 0) === priorityLoadValue(b.priority_load, 0);
  }

  function isDefaultCustomSlot(cs, hourData) {
    if (!cs) return false;
    if (cs.default_custom_slot === true || cs.is_default_custom_slot === true || cs.created_by_add === true) return true;
    return !!hourData && slotsEqual(cs, hourData);
  }

  function markCustomSlotEdited(cs) {
    if (!cs) return;
    cs.default_custom_slot = false;
    delete cs.is_default_custom_slot;
    delete cs.created_by_add;
  }

  function makeDefaultDay(day, source) {
    source = source || {};
    const hours = [];
    for (let h = 0; h < 24; h++) {
      let existingHour = null;
      if (Array.isArray(source.hours)) {
        existingHour = source.hours.find(x => Number(x.hour) === h) || source.hours[h] || null;
      }
      hours.push(makeDefaultHour(h, existingHour || {}));
    }
    const slots = [];
    for (let h = 0; h < 24; h++) {
      for (let m = 0; m < 60; m += 5) {
        let existingSlot = null;
        if (Array.isArray(source.slots)) {
          const idx = slotIndex(h, m);
          existingSlot = source.slots[idx] || null;
        }
        slots.push(makeDefaultSlot(h, m, existingSlot || hours[h] || {}));
      }
    }
    const explicitCustomSlots = Array.isArray(source.customSlots) ? source.customSlots : (Array.isArray(source.custom_slots) ? source.custom_slots : []);
    explicitCustomSlots.forEach(rawSlot => {
      const h = clamp(intValue(rawSlot && rawSlot.hour, 0), 0, 23);
      let m = clamp(intValue(rawSlot && rawSlot.minute, 0), 0, 55);
      m = Math.floor(m / 5) * 5;
      if (m === 0) return;
      const idx = slotIndex(h, m);
      if (idx >= 0 && idx < slots.length) {
        slots[idx] = makeDefaultSlot(h, m, rawSlot || hours[h] || {});
        slots[idx].default_custom_slot = boolValue(valueOrDefault(rawSlot && rawSlot.default_custom_slot, rawSlot && rawSlot.is_default_custom_slot), false);
      }
    });
    const customSlots = [];
    for (let h = 0; h < 24; h++) {
      for (let m = 0; m < 60; m += 5) {
        if (m === 0) continue;
        const idx = slotIndex(h, m);
        const slot = slots[idx];
        const hour = hours[h];
        if (slot && hour && (slot.default_custom_slot || !slotsEqual(slot, hour))) {
          customSlots.push({
            hour: h, minute: m,
            default_custom_slot: !!slot.default_custom_slot,
            sell_mode_kw: slot.sell_mode_kw,
            sell_mode_batt_capacity: slot.sell_mode_batt_capacity,
            charge_mode: slot.charge_mode,
            grid_export_limit: slot.grid_export_limit,
            grid_charge_enabled: slot.grid_charge_enabled,
            solar_export: slot.solar_export,
            load_limit_mode: slot.load_limit_mode,
            priority_load: priorityLoadValue(slot.priority_load, 0),
            enabled: slot.enabled,
            use_timer: slot.use_timer,
            use_timer_mask: slot.use_timer_mask
          });
        }
      }
    }
    return { day, enabled: true, hours, slots, customSlots };
  }

  function parseScheduleJSON(){
    try { return JSON.parse(scheduleJSONEl.value); } catch { return { days: [] }; }
  }

  function normalizeScheduleData(raw) {
    raw = raw || {};
    let mask = normalizeUseTimerMask(valueOrDefault(valueOrDefault(raw.use_timer_mask, raw.UseTimerMask), 0));
    if (mask === 0 && (boolValue(raw.use_timer, false) || boolValue(raw.is_active, false) || boolValue(raw.enabled, false))) mask = 1;
    const normalized = { use_timer_mask: mask, days: [] };
    const srcDays = raw && Array.isArray(raw.days) ? raw.days : [];
    for (let day = 1; day <= 31; day++) {
      const existingDay = srcDays.find(x => Number(x.day) === day) || {};
      normalized.days.push(makeDefaultDay(day, existingDay));
    }
    return normalized;
  }

  let scheduleData = normalizeScheduleData(parseScheduleJSON());

  function applyUseTimerToHours(){
    const mask = normalizeUseTimerMask(scheduleData.use_timer_mask);
    const timerEnabled = (mask & 1) !== 0;
    scheduleData.days.forEach(day => {
      const dayActive = isDayActiveByMask(day.day, mask);
      day.enabled = dayActive;
      day.hours.forEach(hour => {
        hour.enabled = dayActive;
        hour.use_timer = timerEnabled;
        hour.use_timer_mask = mask;
        hour.point = pointForHour(hour.hour);
        hour.sell_time = sellTimeForHour(hour.hour);
        hour.label = hourLabel(hour.hour);
      });
      day.slots.forEach(slot => {
        slot.enabled = dayActive;
        slot.use_timer = timerEnabled;
        slot.use_timer_mask = mask;
        slot.point = pointForHour(slot.hour);
        slot.sell_time = sellTimeForSlot(slot.hour, slot.minute);
        slot.label = slotLabel(slot.hour, slot.minute);
      });
      (day.customSlots || []).forEach(cs => {
        cs.enabled = dayActive;
        cs.use_timer = timerEnabled;
        cs.use_timer_mask = mask;
      });
    });
  }

  function compactScheduleForExport(){
    return {
      use_timer_mask: normalizeUseTimerMask(scheduleData.use_timer_mask),
      days: scheduleData.days.map(day => ({
        day: day.day,
        enabled: !!day.enabled,
        hours: day.hours.map(hour => ({
          hour: hour.hour,
          label: hour.label,
          point: hour.point,
          sell_time: hour.sell_time,
          enabled: !!hour.enabled,
          sell_mode_kw: hour.sell_mode_kw,
          sell_mode_batt_capacity: hour.sell_mode_batt_capacity,
          charge_mode: hour.charge_mode,
          grid_export_limit: hour.grid_export_limit,
          grid_charge_enabled: hour.grid_charge_enabled,
          solar_export: hour.solar_export,
          load_limit_mode: hour.load_limit_mode,
          use_timer: hour.use_timer,
          use_timer_mask: hour.use_timer_mask,
          priority_load: priorityLoadValue(hour.priority_load, 0)
        })),
        customSlots: (day.customSlots || []).map(cs => ({
          hour: cs.hour,
          minute: cs.minute,
          default_custom_slot: !!cs.default_custom_slot,
          label: slotLabel(cs.hour, cs.minute),
          point: pointForHour(cs.hour),
          sell_time: sellTimeForSlot(cs.hour, cs.minute),
          enabled: !!cs.enabled,
          sell_mode_kw: cs.sell_mode_kw,
          sell_mode_batt_capacity: cs.sell_mode_batt_capacity,
          charge_mode: cs.charge_mode,
          grid_export_limit: cs.grid_export_limit,
          grid_charge_enabled: cs.grid_charge_enabled,
          solar_export: cs.solar_export,
          load_limit_mode: cs.load_limit_mode,
          use_timer: cs.use_timer,
          use_timer_mask: cs.use_timer_mask,
          priority_load: priorityLoadValue(cs.priority_load, 0)
        }))
      }))
    };
  }

  function markJSONDirty(){
    applyUseTimerToHours();
    jsonDirty = true;
    document.getElementById('registerPreviewPanel')?.classList.add('hidden');
    renderSendPreview();
  }

  function syncJSON(){
    applyUseTimerToHours();
    scheduleJSONEl.value = JSON.stringify(compactScheduleForExport());
    jsonDirty = false;
    return scheduleJSONEl.value;
  }

  function renderUseTimerMaskPanel(){
    const panel = document.getElementById('useTimerMaskPanel');
    if (!panel) return;
    const mask = normalizeUseTimerMask(scheduleData.use_timer_mask);
    panel.innerHTML = `<details class="use-timer-dropdown" open>
      <summary>
        <span><strong>Use Timer</strong><span class="muted"> · общий параметр расписания; адрес берётся из профиля модели</span></span>
        <span class="use-timer-summary">${maskSummary(mask)} · bitmask=${mask}</span>
      </summary>
      <div class="use-timer-panel">
        <p class="muted">Это единственный интерфейс настройки активных дней. В инвертор отправляется сумма выбранных битов: Bit0 — Вкл, Bit1..Bit7 — Пн..Вс. Даты с невыбранным днём недели автоматически становятся неактивными в календаре.</p>
        <div class="calendar-legend"><span class="legend-dot weekend"></span> выходные <span class="legend-dot inactive"></span> неактивные даты</div>
        <div class="chips">
          ${useTimerLabels.map(({label, bit, hint}) => `<label class="chip use-timer-chip" title="${hint}"><input type="checkbox" class="use-timer-mask-check" data-bit="${bit}" ${maskChecked(bit) ? 'checked' : ''}> ${label} <span class="muted">${1 << bit}</span></label>`).join('')}
        </div>
      </div>
    </details>`;
    panel.querySelectorAll('.use-timer-mask-check').forEach(el => {
      el.addEventListener('change', function(){
        let mask = normalizeUseTimerMask(scheduleData.use_timer_mask);
        const bit = Number(this.dataset.bit);
        if (this.checked) mask |= (1 << bit); else mask &= ~(1 << bit);
        scheduleData.use_timer_mask = normalizeUseTimerMask(mask);
        markJSONDirty();
        if (openModalDayIndex >= 0) {
          const openDay = scheduleData.days[openModalDayIndex];
          if (openDay && !openDay.enabled && modal) {
            openModalDayIndex = -1;
            modal.classList.remove('show');
          }
        }
        renderCurrentView();
      });
    });
  }

  function openUseTimerPanel(){
    const details = document.querySelector('#useTimerMaskPanel details');
    if (details) details.open = true;
    const panel = document.getElementById('useTimerMaskPanel');
    if (panel) panel.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  function buildCopyDayHTML(dayIndex){
    const options = scheduleData.days.map((d, idx) => `<option value="${idx}" ${idx === dayIndex ? 'selected' : ''}>День ${d.day}</option>`).join('');
    return `<div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap;">
      <select class="hour-select copy-day-select" data-copy-target="${dayIndex}">${options}</select>
      <button type="button" class="btn btn-secondary copy-day-btn" data-copy-target="${dayIndex}">Использовать расписание выбранного дня</button>
    </div>`;
  }

  function buildHourRow(dayIndex, hourIndex, hour){
    const day = scheduleData.days[dayIndex];
    const usedMinutes = (day.customSlots || []).filter(cs => cs.hour === hourIndex).map(cs => cs.minute);
    const availMinutes = [];
    for (let m = 5; m < 60; m += 5) {
      if (!usedMinutes.includes(m)) availMinutes.push(m);
    }
    const addBtnHTML = availMinutes.length > 0
      ? `<button type="button" class="btn btn-secondary btn-xs add-slot-btn" data-day="${dayIndex}" data-hour="${hourIndex}" data-minutes="${availMinutes.join(',')}" title="Добавить 5-минутный интервал">+</button>`
      : `<span class="muted btn-xs" title="Все интервалы добавлены">✓</span>`;
    return `<tr class="hour-row">
      <td class="hour-label-cell">${hour.label} ${addBtnHTML}</td>
      <td><input type="number" class="hour-input" data-day="${dayIndex}" data-hour="${hourIndex}" data-field="sell_mode_kw" h="1" value="${displaySellModeKw(hour.sell_mode_kw)}" min="0" step="10"></td>
      <td><input type="number" class="hour-input" data-day="${dayIndex}" data-hour="${hourIndex}" data-field="sell_mode_batt_capacity" value="${hour.sell_mode_batt_capacity}" min="0" max="100"></td>
      <td>${chargeModeChecksHTML('hour', dayIndex, hourIndex, null, hour.charge_mode)}</td>
      <td><input type="number" class="hour-input" data-day="${dayIndex}" data-hour="${hourIndex}" data-field="grid_export_limit" value="${displayGridExportLimit(hour.grid_export_limit)}" min="0" step="10"></td>
      <td><input type="checkbox" class="hour-bool" data-day="${dayIndex}" data-hour="${hourIndex}" data-field="grid_charge_enabled" ${hour.grid_charge_enabled ? 'checked' : ''}></td>
      <td><select class="hour-select" data-day="${dayIndex}" data-hour="${hourIndex}" data-field="load_limit_mode">
        <option value="0" ${hour.load_limit_mode === 0 ? 'selected' : ''}>Selling first</option>
        <option value="1" ${hour.load_limit_mode === 1 ? 'selected' : ''}>Zero export load</option>
        <option value="2" ${hour.load_limit_mode === 2 ? 'selected' : ''}>Zero export CT</option>
      </select></td>
      <td>${prioritySelectHTML('hour-select', dayIndex, hourIndex, null, hour.priority_load)}</td>
    </tr>`;
  }

  function buildCustomSlotRow(dayIndex, hour, minute, cs) {
    return `<tr class="custom-slot-row" data-day="${dayIndex}" data-hour="${hour}" data-minute="${minute}">
      <td class="custom-slot-time">
        <span class="slot-time-prefix">${pad(hour)}:</span><input type="number" class="slot-time-input" data-day="${dayIndex}" data-hour="${hour}" data-old-minute="${minute}" value="${minute}" min="5" max="55" step="5">
        <button type="button" class="btn btn-secondary btn-xs remove-slot-btn" data-day="${dayIndex}" data-hour="${hour}" data-minute="${minute}" title="Удалить интервал">×</button>
      </td>
      <td><input type="number" class="slot-input" data-day="${dayIndex}" data-hour="${hour}" data-minute="${minute}" data-field="sell_mode_kw" value="${displaySellModeKw(cs.sell_mode_kw)}" min="0" step="10"></td>
      <td><input type="number" class="slot-input" data-day="${dayIndex}" data-hour="${hour}" data-minute="${minute}" data-field="sell_mode_batt_capacity" value="${cs.sell_mode_batt_capacity}" min="0" max="100"></td>
      <td>${chargeModeChecksHTML('slot', dayIndex, hour, minute, cs.charge_mode)}</td>
      <td><input type="number" class="slot-input" data-day="${dayIndex}" data-hour="${hour}" data-minute="${minute}" data-field="grid_export_limit" value="${displayGridExportLimit(cs.grid_export_limit)}" min="0" step="10"></td>
      <td><input type="checkbox" class="slot-bool" data-day="${dayIndex}" data-hour="${hour}" data-minute="${minute}" data-field="grid_charge_enabled" ${cs.grid_charge_enabled ? 'checked' : ''}></td>
      <td><select class="slot-select" data-day="${dayIndex}" data-hour="${hour}" data-minute="${minute}" data-field="load_limit_mode">
        <option value="0" ${cs.load_limit_mode === 0 ? 'selected' : ''}>Selling first</option>
        <option value="1" ${cs.load_limit_mode === 1 ? 'selected' : ''}>Zero export load</option>
        <option value="2" ${cs.load_limit_mode === 2 ? 'selected' : ''}>Zero export CT</option>
      </select></td>
      <td>${prioritySelectHTML('slot-select', dayIndex, hour, minute, cs.priority_load)}</td>
    </tr>`;
  }

  function buildDayEditorHTML(dayIndex){
    const day = scheduleData.days[dayIndex];
    let rows = '';
    day.hours.forEach((hour, hourIndex) => {
      rows += buildHourRow(dayIndex, hourIndex, hour);
      const customForHour = (day.customSlots || []).filter(cs => cs.hour === hourIndex).sort((a, b) => a.minute - b.minute);
      customForHour.forEach(cs => {
        rows += buildCustomSlotRow(dayIndex, cs.hour, cs.minute, cs);
      });
    });
    const totalCustom = (day.customSlots || []).length;
    const customInfo = totalCustom > 0 ? ` · <strong>${totalCustom}</strong> кастомных интервалов` : '';
    return `<div class="day-summary">
      <div class="muted">24 часа × 12 слотов = 288 пятиминутных интервалов. Нажми <strong>+</strong> рядом с часом чтобы добавить отдельный 5-минутный интервал.${customInfo}</div>
      ${buildCopyDayHTML(dayIndex)}
    </div>
    <div class="table-wrap"><table><thead><tr>
      <th>Время</th><th>Power</th><th>Capacity</th><th>Sell / Grid / Gen</th><th>Grid Export Limit</th><th>Grid Charge</th><th>Load Limit</th><th>Priority Load</th>
    </tr></thead><tbody>${rows}</tbody></table></div>`;
  }

  function copyDaySchedule(sourceDayIndex, targetDayIndex){
    if (sourceDayIndex === targetDayIndex) return;
    const source = scheduleData.days[sourceDayIndex];
    const target = scheduleData.days[targetDayIndex];
    if (!source || !target) return;
    const targetActive = isDayActiveByMask(target.day, normalizeUseTimerMask(scheduleData.use_timer_mask));
    target.hours = source.hours.map((hour, idx) => ({
      ...hour,
      hour: idx,
      label: hourLabel(idx),
      point: pointForHour(idx),
      sell_time: sellTimeForHour(idx),
      enabled: targetActive,
      use_timer: useTimerEnabled(),
      use_timer_mask: normalizeUseTimerMask(scheduleData.use_timer_mask)
    }));
    target.slots = source.slots.map((slot, idx) => ({
      ...slot,
      enabled: targetActive,
      use_timer: useTimerEnabled(),
      use_timer_mask: normalizeUseTimerMask(scheduleData.use_timer_mask)
    }));
    target.customSlots = (source.customSlots || []).map(cs => ({
      ...cs,
      enabled: targetActive,
      use_timer: useTimerEnabled(),
      use_timer_mask: normalizeUseTimerMask(scheduleData.use_timer_mask)
    }));
    target.enabled = targetActive;
    markJSONDirty();
    renderCurrentView();
    rerenderModalIfOpen();
    if (window.showPopup) showPopup(`Расписание дня ${source.day} применено к дню ${target.day}`);
  }

  function applyHourValuesToSlot(target, hour, minute) {
    if (!target || !hour) return;
    target.sell_mode_kw = hour.sell_mode_kw;
    target.sell_mode_batt_capacity = hour.sell_mode_batt_capacity;
    target.charge_mode = hour.charge_mode;
    target.grid_export_limit = hour.grid_export_limit;
    target.grid_charge_enabled = hour.grid_charge_enabled;
    target.solar_export = hour.solar_export;
    target.load_limit_mode = hour.load_limit_mode;
    target.priority_load = hour.priority_load;
    target.enabled = hour.enabled;
    target.use_timer = hour.use_timer;
    target.use_timer_mask = hour.use_timer_mask;
    target.point = pointForHour(hour.hour);
    target.sell_time = sellTimeForSlot(hour.hour, minute);
    target.label = slotLabel(hour.hour, minute);
  }

  function syncHourToSlots(dayIndex, hourIndex) {
    const hour = scheduleData.days[dayIndex].hours[hourIndex];
    const baseIdx = hourIndex * 12;
    const day = scheduleData.days[dayIndex];
    for (let m = 0; m < 12; m++) {
      const slotIdx = baseIdx + m;
      if (slotIdx >= day.slots.length) break;
      const minute = m * 5;
      const customSlot = day.customSlots && day.customSlots.find(cs => cs.hour === hourIndex && cs.minute === minute);
      if (customSlot && !isDefaultCustomSlot(customSlot, hour)) continue;
      const slot = day.slots[slotIdx];
      applyHourValuesToSlot(slot, hour, minute);
      if (customSlot) {
        applyHourValuesToSlot(customSlot, hour, minute);
        customSlot.default_custom_slot = true;
        slot.default_custom_slot = true;
      }
    }
  }

  function syncCustomSlotToSlots(dayIndex, hour, minute) {
    const day = scheduleData.days[dayIndex];
    const idx = slotIndex(hour, minute);
    if (idx < 0 || idx >= day.slots.length) return;
    const cs = day.customSlots.find(x => x.hour === hour && x.minute === minute);
    if (!cs) return;
    const slot = day.slots[idx];
    slot.sell_mode_kw = cs.sell_mode_kw;
    slot.sell_mode_batt_capacity = cs.sell_mode_batt_capacity;
    slot.charge_mode = cs.charge_mode;
    slot.grid_export_limit = cs.grid_export_limit;
    slot.grid_charge_enabled = cs.grid_charge_enabled;
    slot.solar_export = cs.solar_export;
    slot.load_limit_mode = cs.load_limit_mode;
    slot.priority_load = cs.priority_load;
    slot.default_custom_slot = !!cs.default_custom_slot;
    slot.enabled = cs.enabled;
    slot.use_timer = cs.use_timer;
    slot.use_timer_mask = cs.use_timer_mask;
  }

  function addCustomSlot(dayIndex, hour, minute) {
    const day = scheduleData.days[dayIndex];
    const existing = day.customSlots.find(x => x.hour === hour && x.minute === minute);
    if (existing) return;
    const idx = slotIndex(hour, minute);
    const slot = day.slots[idx];
    const hourData = day.hours[hour];
    const source = slot || hourData || {};
    day.customSlots.push({
      hour, minute,
      default_custom_slot: true,
      sell_mode_kw: source.sell_mode_kw,
      sell_mode_batt_capacity: source.sell_mode_batt_capacity,
      charge_mode: source.charge_mode,
      grid_export_limit: source.grid_export_limit,
      grid_charge_enabled: source.grid_charge_enabled,
      solar_export: source.solar_export,
      load_limit_mode: source.load_limit_mode,
      priority_load: priorityLoadValue(source.priority_load, 0),
      enabled: source.enabled !== false,
      use_timer: source.use_timer,
      use_timer_mask: source.use_timer_mask
    });
  }

  function removeCustomSlot(dayIndex, hour, minute) {
    const day = scheduleData.days[dayIndex];
    day.customSlots = day.customSlots.filter(x => !(x.hour === hour && x.minute === minute));
    const idx = slotIndex(hour, minute);
    if (idx >= 0 && idx < day.slots.length) {
      const hourData = day.hours[hour];
      const slot = day.slots[idx];
      slot.sell_mode_kw = hourData.sell_mode_kw;
      slot.sell_mode_batt_capacity = hourData.sell_mode_batt_capacity;
      slot.charge_mode = hourData.charge_mode;
      slot.grid_export_limit = hourData.grid_export_limit;
      slot.grid_charge_enabled = hourData.grid_charge_enabled;
      slot.solar_export = hourData.solar_export;
      slot.load_limit_mode = hourData.load_limit_mode;
      slot.priority_load = hourData.priority_load;
      slot.default_custom_slot = false;
      slot.enabled = hourData.enabled;
      slot.use_timer = hourData.use_timer;
      slot.use_timer_mask = hourData.use_timer_mask;
    }
  }

  function rerenderModalIfOpen() {
    if (openModalDayIndex < 0 || !modal || !modalEditor || !modalDayTitle) return;
    const day = scheduleData.days[openModalDayIndex];
    if (!day) return;
    modalDayTitle.textContent = `День ${day.day}`;
    modalEditor.innerHTML = buildDayEditorHTML(openModalDayIndex);
    attachDayEditorEvents(modalEditor);
  }

  function attachDayEditorEvents(container){
    container.querySelectorAll('.copy-day-btn').forEach(btn => {
      btn.addEventListener('click', function(e){
        e.preventDefault();
        e.stopPropagation();
        const targetDayIndex = Number(this.dataset.copyTarget);
        const select = container.querySelector(`.copy-day-select[data-copy-target="${targetDayIndex}"]`);
        const sourceDayIndex = Number((select && select.value) || targetDayIndex);
        copyDaySchedule(sourceDayIndex, targetDayIndex);
      });
    });

    container.querySelectorAll('.charge-mode-bool').forEach(el => {
      el.addEventListener('change', function(e){
        e.stopPropagation();
        const box = this.closest('.mode-checkboxes');
        if (!box) return;
        const dayIndex = Number(box.dataset.day);
        const hourIndex = Number(box.dataset.hour);
        const minute = Number(box.dataset.minute || 0);
        const flags = {
          sell: !!box.querySelector('[data-mode="sell"]')?.checked,
          grid: !!box.querySelector('[data-mode="grid"]')?.checked,
          gen: !!box.querySelector('[data-mode="gen"]')?.checked
        };
        const value = chargeModeFromFlags(flags);
        if (box.dataset.scope === 'slot') {
          const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hourIndex && x.minute === minute);
          if (cs) {
            markCustomSlotEdited(cs);
            cs.charge_mode = value;
            syncCustomSlotToSlots(dayIndex, hourIndex, minute);
          }
        } else {
          scheduleData.days[dayIndex].hours[hourIndex].charge_mode = value;
          syncHourToSlots(dayIndex, hourIndex);
        }
        markJSONDirty();
      });
      el.addEventListener('click', e => e.stopPropagation());
    });

    container.querySelectorAll('.hour-bool').forEach(el => {
      el.addEventListener('change', function(e){
        e.stopPropagation();
        const dayIndex = Number(this.dataset.day);
        const hourIndex = Number(this.dataset.hour);
        const field = this.dataset.field;
        scheduleData.days[dayIndex].hours[hourIndex][field] = this.checked;
        syncHourToSlots(dayIndex, hourIndex);
        markJSONDirty();
      });
      el.addEventListener('click', e => e.stopPropagation());
    });

    container.querySelectorAll('.hour-input').forEach(el => {
      if (el.dataset.field === 'sell_mode_kw') {
        el.addEventListener('focus', function(){
          const dayIndex = Number(this.dataset.day);
          const hourIndex = Number(this.dataset.hour);
          this.removeAttribute('max');
          this.step = '1';
          this.value = rawSellModeKw(scheduleData.days[dayIndex].hours[hourIndex].sell_mode_kw);
          this.select();
        });
        el.addEventListener('blur', function(){
          const dayIndex = Number(this.dataset.day);
          const hourIndex = Number(this.dataset.hour);
          this.removeAttribute('max');
          this.step = '10';
          this.value = displaySellModeKw(scheduleData.days[dayIndex].hours[hourIndex].sell_mode_kw);
        });
      }
      if (el.dataset.field === 'grid_export_limit') {
        el.addEventListener('focus', function(){
          const dayIndex = Number(this.dataset.day);
          const hourIndex = Number(this.dataset.hour);
          this.removeAttribute('max');
          this.step = '1';
          this.value = rawGridExportLimit(scheduleData.days[dayIndex].hours[hourIndex].grid_export_limit);
          this.select();
        });
        el.addEventListener('blur', function(){
          const dayIndex = Number(this.dataset.day);
          const hourIndex = Number(this.dataset.hour);
          this.removeAttribute('max');
          this.step = '10';
          this.value = displayGridExportLimit(scheduleData.days[dayIndex].hours[hourIndex].grid_export_limit);
        });
      }
      el.addEventListener('input', function(){
        const dayIndex = Number(this.dataset.day);
        const hourIndex = Number(this.dataset.hour);
        const field = this.dataset.field;
        let value = parseInt(this.value || '0', 10);
        if (field === 'sell_mode_batt_capacity') {
          value = clamp(Number.isNaN(value) ? 100 : value, 0, 100);
        } else if (field === 'sell_mode_kw') {
          value = clamp(Number.isNaN(value) ? 0 : value, 0, schedulePowerMaxRaw);
        } else if (field === 'grid_export_limit') {
          value = clamp(Number.isNaN(value) ? 0 : value, 0, gridExportMaxRaw);
        } else if (value < 0 || Number.isNaN(value)) {
          value = 0;
        }
        this.value = value;
        scheduleData.days[dayIndex].hours[hourIndex][field] = value;
        syncHourToSlots(dayIndex, hourIndex);
        markJSONDirty();
      });
    });

    container.querySelectorAll('.hour-select').forEach(el => {
      if (el.classList.contains('copy-day-select')) {
        el.addEventListener('click', e => e.stopPropagation());
        return;
      }
      el.addEventListener('change', function(){
        const dayIndex = Number(this.dataset.day);
        const hourIndex = Number(this.dataset.hour);
        const field = this.dataset.field;
        scheduleData.days[dayIndex].hours[hourIndex][field] = field === 'priority_load' ? priorityLoadValue(this.value, 0) : parseInt(this.value, 10);
        syncHourToSlots(dayIndex, hourIndex);
        markJSONDirty();
      });
    });

    container.querySelectorAll('.slot-time-input').forEach(el => {
      el.addEventListener('change', function(){
        const dayIndex = Number(this.dataset.day);
        const hour = Number(this.dataset.hour);
        const oldMinute = Number(this.dataset.oldMinute);
        let newMinute = Math.round(Number(this.value) / 5) * 5;
        if (newMinute < 5) newMinute = 5;
        if (newMinute > 55) newMinute = 55;
        this.value = newMinute;
        if (newMinute === oldMinute) return;
        const day = scheduleData.days[dayIndex];
        const duplicate = day.customSlots.find(cs => cs.hour === hour && cs.minute === newMinute && cs.minute !== oldMinute);
        if (duplicate) {
          if (window.showPopup) showPopup(`Интервал ${pad(hour)}:${pad(newMinute)} уже существует`, 'error');
          this.value = oldMinute;
          return;
        }
        const cs = day.customSlots.find(x => x.hour === hour && x.minute === oldMinute);
        if (!cs) return;
        const oldIdx = slotIndex(hour, oldMinute);
        const newIdx = slotIndex(hour, newMinute);
        if (oldIdx >= 0 && oldIdx < day.slots.length) {
          const oldSlot = day.slots[oldIdx];
          const hourData = day.hours[hour];
          oldSlot.sell_mode_kw = hourData.sell_mode_kw;
          oldSlot.sell_mode_batt_capacity = hourData.sell_mode_batt_capacity;
          oldSlot.charge_mode = hourData.charge_mode;
          oldSlot.grid_export_limit = hourData.grid_export_limit;
          oldSlot.grid_charge_enabled = hourData.grid_charge_enabled;
          oldSlot.solar_export = hourData.solar_export;
          oldSlot.load_limit_mode = hourData.load_limit_mode;
          oldSlot.priority_load = hourData.priority_load;
          oldSlot.default_custom_slot = false;
          oldSlot.enabled = hourData.enabled;
          oldSlot.use_timer = hourData.use_timer;
          oldSlot.use_timer_mask = hourData.use_timer_mask;
        }
        markCustomSlotEdited(cs);
        cs.minute = newMinute;
        cs.sell_time = sellTimeForSlot(hour, newMinute);
        cs.label = slotLabel(hour, newMinute);
        if (newIdx >= 0 && newIdx < day.slots.length) {
          const newSlot = day.slots[newIdx];
          newSlot.sell_mode_kw = cs.sell_mode_kw;
          newSlot.sell_mode_batt_capacity = cs.sell_mode_batt_capacity;
          newSlot.charge_mode = cs.charge_mode;
          newSlot.grid_export_limit = cs.grid_export_limit;
          newSlot.grid_charge_enabled = cs.grid_charge_enabled;
          newSlot.solar_export = cs.solar_export;
          newSlot.load_limit_mode = cs.load_limit_mode;
          newSlot.priority_load = cs.priority_load;
          newSlot.default_custom_slot = !!cs.default_custom_slot;
          newSlot.enabled = cs.enabled;
          newSlot.use_timer = cs.use_timer;
          newSlot.use_timer_mask = cs.use_timer_mask;
        }
        markJSONDirty();
        renderCurrentView();
        rerenderModalIfOpen();
      });
      el.addEventListener('keydown', function(e){
        if (e.key === 'Enter') { e.preventDefault(); this.blur(); }
      });
    });

    container.querySelectorAll('.add-slot-btn').forEach(el => {
      el.addEventListener('click', function(e){
        e.preventDefault();
        e.stopPropagation();
        const dayIndex = Number(this.dataset.day);
        const hourIndex = Number(this.dataset.hour);
        const minutes = (this.dataset.minutes || '').split(',').map(Number).filter(n => n > 0);
        const minute = minutes.length > 0 ? minutes[0] : 5;
        addCustomSlot(dayIndex, hourIndex, minute);
        markJSONDirty();
        renderCurrentView();
        rerenderModalIfOpen();
      });
    });

    container.querySelectorAll('.remove-slot-btn').forEach(el => {
      el.addEventListener('click', function(e){
        e.preventDefault();
        e.stopPropagation();
        const dayIndex = Number(this.dataset.day);
        const hour = Number(this.dataset.hour);
        const minute = Number(this.dataset.minute);
        removeCustomSlot(dayIndex, hour, minute);
        markJSONDirty();
        renderCurrentView();
        rerenderModalIfOpen();
      });
    });

    container.querySelectorAll('.slot-input').forEach(el => {
      if (el.dataset.field === 'sell_mode_kw') {
        el.addEventListener('focus', function(){
          const dayIndex = Number(this.dataset.day);
          const hour = Number(this.dataset.hour);
          const minute = Number(this.dataset.minute);
          const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hour && x.minute === minute);
          this.removeAttribute('max');
          this.step = '1';
          if (cs) this.value = rawSellModeKw(cs.sell_mode_kw);
          this.select();
        });
        el.addEventListener('blur', function(){
          const dayIndex = Number(this.dataset.day);
          const hour = Number(this.dataset.hour);
          const minute = Number(this.dataset.minute);
          const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hour && x.minute === minute);
          this.removeAttribute('max');
          this.step = '10';
          if (cs) this.value = displaySellModeKw(cs.sell_mode_kw);
        });
      }
      if (el.dataset.field === 'grid_export_limit') {
        el.addEventListener('focus', function(){
          const dayIndex = Number(this.dataset.day);
          const hour = Number(this.dataset.hour);
          const minute = Number(this.dataset.minute);
          const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hour && x.minute === minute);
          this.removeAttribute('max');
          this.step = '1';
          if (cs) this.value = rawGridExportLimit(cs.grid_export_limit);
          this.select();
        });
        el.addEventListener('blur', function(){
          const dayIndex = Number(this.dataset.day);
          const hour = Number(this.dataset.hour);
          const minute = Number(this.dataset.minute);
          const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hour && x.minute === minute);
          this.removeAttribute('max');
          this.step = '10';
          if (cs) this.value = displayGridExportLimit(cs.grid_export_limit);
        });
      }
      el.addEventListener('input', function(){
        const dayIndex = Number(this.dataset.day);
        const hour = Number(this.dataset.hour);
        const minute = Number(this.dataset.minute);
        const field = this.dataset.field;
        let value = parseInt(this.value || '0', 10);
        if (field === 'sell_mode_batt_capacity') {
          value = clamp(Number.isNaN(value) ? 100 : value, 0, 100);
        } else if (field === 'sell_mode_kw') {
          value = clamp(Number.isNaN(value) ? 0 : value, 0, schedulePowerMaxRaw);
        } else if (field === 'grid_export_limit') {
          value = clamp(Number.isNaN(value) ? 0 : value, 0, gridExportMaxRaw);
        } else if (value < 0 || Number.isNaN(value)) {
          value = 0;
        }
        this.value = value;
        const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hour && x.minute === minute);
        if (cs) {
          markCustomSlotEdited(cs);
          cs[field] = value;
          syncCustomSlotToSlots(dayIndex, hour, minute);
          markJSONDirty();
        }
      });
    });

    container.querySelectorAll('.slot-select').forEach(el => {
      el.addEventListener('change', function(){
        const dayIndex = Number(this.dataset.day);
        const hour = Number(this.dataset.hour);
        const minute = Number(this.dataset.minute);
        const field = this.dataset.field;
        const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hour && x.minute === minute);
        if (cs) {
          markCustomSlotEdited(cs);
          cs[field] = field === 'priority_load' ? priorityLoadValue(this.value, 0) : parseInt(this.value, 10);
          syncCustomSlotToSlots(dayIndex, hour, minute);
          markJSONDirty();
        }
      });
    });

    container.querySelectorAll('.slot-bool').forEach(el => {
      el.addEventListener('change', function(e){
        e.stopPropagation();
        const dayIndex = Number(this.dataset.day);
        const hour = Number(this.dataset.hour);
        const minute = Number(this.dataset.minute);
        const field = this.dataset.field;
        const cs = scheduleData.days[dayIndex].customSlots.find(x => x.hour === hour && x.minute === minute);
        if (cs) {
          markCustomSlotEdited(cs);
          cs[field] = this.checked;
          syncCustomSlotToSlots(dayIndex, hour, minute);
          markJSONDirty();
        }
      });
      el.addEventListener('click', e => e.stopPropagation());
    });
  }

  function renderListView(){
    if (!listView) return;
    listView.innerHTML = '';
    scheduleData.days.forEach((day, dayIndex) => {
      const details = document.createElement('details');
      details.innerHTML = `<summary><div class="day-summary" style="display:inline-flex; width:100%; justify-content:space-between;"><span>День ${day.day}</span><span class="muted">288 слотов (5 мин)</span></div></summary>${buildDayEditorHTML(dayIndex)}`;
      listView.appendChild(details);
      attachDayEditorEvents(details);
    });
  }

  function renderGridView(){
    if (!gridView) return;
    gridView.innerHTML = '';
    scheduleData.days.forEach((day, dayIndex) => {
      const card = document.createElement('div');
      const info = dayCalendarInfo(day.day);
      const isActive = !!day.enabled;
      card.className = 'day-card' + (info.weekend ? ' weekend-day' : '') + (!isActive ? ' inactive-day' : '');
      card.dataset.dayIndex = dayIndex;
      const customCount = (day.customSlots || []).length;
      const customInfo = customCount > 0 ? `<div style="margin-top:4px;color:var(--accent);font-size:13px;">+${customCount} кастомных</div>` : '';
      const stateInfo = isActive ? 'активен' : 'неактивен';
      const weekdayInfo = info.valid ? `${info.label} · ${stateInfo}` : stateInfo;
      card.innerHTML = `<div style="font-size:20px;font-weight:700;">День ${day.day}</div><div class="day-weekday">${weekdayInfo}</div><div style="margin-top:8px;" class="muted">24 часа / 288 слотов</div>${customInfo}`;
      card.addEventListener('click', function(){
        if (!modal || !modalEditor || !modalDayTitle) return;
        if (!day.enabled) {
          if (window.showPopup) showPopup(`День ${day.day} отключён по настройкам Use Timer`, 'error');
          return;
        }
        openModalDayIndex = dayIndex;
        modalDayTitle.textContent = `День ${day.day}`;
        modalEditor.innerHTML = buildDayEditorHTML(dayIndex);
        attachDayEditorEvents(modalEditor);
        modal.classList.add('show');
      });
      gridView.appendChild(card);
    });
  }

  function applyViewMode(){
    if (!listView) viewModeEl.value = 'grid';
    const mode = viewModeEl.value === 'grid' ? 'grid' : 'list';
    if (mode === 'grid') {
      if (gridView) gridView.classList.remove('hidden');
      if (listView) listView.classList.add('hidden');
      if (gridModeBtn) gridModeBtn.className = 'btn btn-primary';
      if (listModeBtn) listModeBtn.className = 'btn btn-secondary';
    } else {
      if (gridView) gridView.classList.add('hidden');
      if (listView) listView.classList.remove('hidden');
      if (listModeBtn) listModeBtn.className = 'btn btn-primary';
      if (gridModeBtn) gridModeBtn.className = 'btn btn-secondary';
    }
  }

  function renderCurrentView(){
    renderUseTimerMaskPanel();
    renderSendPreview();
    renderListView();
    renderGridView();
    applyViewMode();
  }

  if (listModeBtn) listModeBtn.addEventListener('click', () => { viewModeEl.value = 'list'; applyViewMode(); });
  if (gridModeBtn) gridModeBtn.addEventListener('click', () => { viewModeEl.value = 'grid'; applyViewMode(); });
  const modalCloseBtn = document.getElementById('modalCloseBtn');
  if (modalCloseBtn) modalCloseBtn.addEventListener('click', () => {
    openModalDayIndex = -1;
    if (modal) modal.classList.remove('show');
  });

  function resetSlotFromHour(day, hour, minute){
    if (!day || !day.hours || !day.slots) return;
    const hourData = day.hours[hour];
    const idx = slotIndex(hour, minute);
    if (!hourData || idx < 0 || idx >= day.slots.length) return;
    const slot = day.slots[idx];
    slot.sell_mode_kw = hourData.sell_mode_kw;
    slot.sell_mode_batt_capacity = hourData.sell_mode_batt_capacity;
    slot.charge_mode = hourData.charge_mode;
    slot.grid_export_limit = hourData.grid_export_limit;
    slot.grid_charge_enabled = hourData.grid_charge_enabled;
    slot.solar_export = hourData.solar_export;
    slot.load_limit_mode = hourData.load_limit_mode;
    slot.priority_load = hourData.priority_load;
    slot.default_custom_slot = false;
    slot.enabled = hourData.enabled;
    slot.use_timer = hourData.use_timer;
    slot.use_timer_mask = hourData.use_timer_mask;
    slot.point = pointForHour(hour);
    slot.sell_time = sellTimeForSlot(hour, minute);
    slot.label = slotLabel(hour, minute);
  }



  function clearAllFiveMinuteSlots(){
    let removed = 0;
    applyUseTimerToHours();
    scheduleData.days.forEach(day => {
      if (!Array.isArray(day.customSlots) || day.customSlots.length === 0) return;
      day.customSlots.forEach(cs => {
        const hour = Number(cs.hour);
        const minute = Number(cs.minute);
        if (minute !== 0) {
          removed++;
          resetSlotFromHour(day, hour, minute);
        }
      });
      day.customSlots = [];
    });
    if (removed > 0) {
      markJSONDirty();
      renderCurrentView();
      rerenderModalIfOpen();
    }
    return removed;
  }

  function clearEmptyCustomSlots(){
    let removed = 0;
    applyUseTimerToHours();
    scheduleData.days.forEach(day => {
      if (!Array.isArray(day.customSlots) || day.customSlots.length === 0) return;
      const kept = [];
      day.customSlots.forEach(cs => {
        const hour = Number(cs.hour);
        const minute = Number(cs.minute);
        const hourData = day.hours && day.hours[hour];
        const empty = !hourData || minute === 0 || isDefaultCustomSlot(cs, hourData);
        if (empty) {
          removed++;
          resetSlotFromHour(day, hour, minute);
        } else {
          kept.push(cs);
        }
      });
      day.customSlots = kept;
    });
    if (removed > 0) {
      markJSONDirty();
      renderCurrentView();
      rerenderModalIfOpen();
    }
    return removed;
  }

  window.scheduleEditor = {
    getScheduleData: () => scheduleData,
    setScheduleData: (data) => {
      scheduleData = normalizeScheduleData(data);
      openModalDayIndex = -1;
      if (modal) modal.classList.remove('show');
      if (modalEditor) modalEditor.innerHTML = '';
      applyUseTimerToHours();
      scheduleJSONEl.value = JSON.stringify(compactScheduleForExport());
      jsonDirty = false;
      document.getElementById('registerPreviewPanel')?.classList.add('hidden');
      renderCurrentView();
    },
    renderSchedule: renderCurrentView,
    applyViewMode,
    syncJSON,
    clearEmptyCustomSlots,
    clearAllFiveMinuteSlots
  };

  markJSONDirty();
  renderCurrentView();
})();
