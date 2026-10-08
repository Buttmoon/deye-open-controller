(function () {
  const esc = App.esc;
  const $ = id => document.getElementById(id);
  const embedded = id => {
    let v = JSON.parse($(id).textContent || "null");
    if (typeof v === "string") v = JSON.parse(v);
    return v;
  };
  const models = embedded("ssModels") || [];
  const inverterID = Number($("ssInverterID")?.value || 0);
  const modelKey = $("ssModelKey")?.value || "";
  let intervals = [];
  let baseline = [];
  let view = "timeline";
  let lastResult = null;
  let previewController = null;
  let dirty = false;
  let preservedJSON = "";
  let advancedReasons = [];

  const llOpts = [
    {value: 0, label: "Selling first"},
    {value: 1, label: "Zero export load"},
    {value: 2, label: "Zero export CT"}
  ];
  const prOpts = [
    {value: 0, label: "Приоритет нагрузки"},
    {value: 1, label: "Приоритет батареи"}
  ];

  function showPopup(msg, type) {
    if (typeof window.showPopup === "function") window.showPopup(msg, type);
    else App.toast(msg, type === "error" ? "error" : type === "success" ? "success" : "info");
  }

  function pad2(n) { return String(n).padStart(2, "0"); }
  function normalizeTimeValue(t, allow24) {
    if (!t) return "00:00";
    if (t === "24:00" && allow24) return "24:00";
    const [hs, ms] = String(t).split(":");
    let h = Number(hs) || 0, m = Number(ms) || 0;
    m = Math.round(m / 5) * 5;
    if (m === 60) { h += 1; m = 0; }
    if (allow24 && h === 24 && m === 0) return "24:00";
    if (h > 23) h = 23;
    if (m > 55) m = 55;
    return `${pad2(h)}:${pad2(m)}`;
  }
  function timeOptions(sel, allow24End) {
    sel = normalizeTimeValue(sel, !!allow24End);
    const parts = [];
    for (let h = 0; h < 24; h++) {
      for (let m = 0; m < 60; m += 5) {
        const v = `${pad2(h)}:${pad2(m)}`;
        parts.push(`<option value="${v}" ${v === sel ? "selected" : ""}>${v}</option>`);
      }
    }
    if (allow24End) {
      parts.push(`<option value="24:00" ${sel === "24:00" ? "selected" : ""}>24:00</option>`);
    }
    return parts.join("");
  }

  function defaultsFromForm() {
    return {
      grid_charge_enabled: $("ssDefGrid").value === "1",
      load_limit_mode: Number($("ssDefLL").value) || 0,
      priority_load: Number($("ssDefPL").value) || 0,
      power_w: Number($("ssDefPower").value) || 0,
      battery_soc: Number($("ssDefSOC").value) || 0,
      grid_export_limit_w: Number($("ssDefExport")?.value) || 0
    };
  }

  function periodWeekdays() {
    return [...document.querySelectorAll("#ssWeekdays input:checked")].map(x => Number(x.dataset.wd));
  }

  function newInterval(o) {
    const d = defaultsFromForm();
    const base = Object.assign({
      name: "", start_time: "00:00", end_time: "06:00",
      grid_charge_enabled: d.grid_charge_enabled,
      load_limit_mode: d.load_limit_mode,
      priority_load: d.priority_load,
      power_w: d.power_w,
      battery_soc: d.battery_soc,
      grid_export_limit_w: d.grid_export_limit_w,
      mode: "custom",
      weekdays: periodWeekdays(),
      date_from: $("ssDateFrom").value || "",
      date_to: $("ssDateTo").value || ""
    }, o || {});
    base.start_time = normalizeTimeValue(base.start_time, false);
    base.end_time = normalizeTimeValue(base.end_time, true);
    return base;
  }

  function markDirty(v = true) {
    dirty = v;
    $("ssDirty")?.classList.toggle("hidden", !dirty);
  }

  function crossesMidnight(r) {
    return r.end_time !== "24:00" && r.end_time <= r.start_time;
  }

  function rowHTML(r, i) {
    const wrap = crossesMidnight(r);
    const partial = !String(r.start_time || "").endsWith(":00") || (!String(r.end_time || "").endsWith(":00") && r.end_time !== "24:00");
    return `<tr data-i="${i}" class="interval-row${partial ? " custom-slot-row" : ""}">
      <td class="hour-label-cell mono">#${i + 1}${partial ? '<div class="badge badge-info" style="margin-top:4px">5 мин</div>' : ""}</td>
      <td><input class="hour-input" type="text" data-f="name" value="${esc(r.name)}" placeholder="подпись"></td>
      <td><select class="hour-select" data-f="start_time">${timeOptions(r.start_time, false)}</select></td>
      <td><select class="hour-select" data-f="end_time">${timeOptions(r.end_time, true)}</select>${wrap ? '<div class="badge badge-info" style="margin-top:4px">через полночь</div>' : ""}</td>
      <td><label class="mode-check"><input type="checkbox" data-f="grid_charge_enabled" ${r.grid_charge_enabled ? "checked" : ""}> Grid</label></td>
      <td><select class="hour-select" data-f="load_limit_mode" data-num="1">${llOpts.map(o => `<option value="${o.value}" ${Number(r.load_limit_mode) === o.value ? "selected" : ""}>${esc(o.label)}</option>`).join("")}</select></td>
      <td><select class="hour-select" data-f="priority_load" data-num="1">${prOpts.map(o => `<option value="${o.value}" ${Number(r.priority_load) === o.value ? "selected" : ""}>${esc(o.label)}</option>`).join("")}</select></td>
      <td><input class="hour-input" type="number" min="0" step="10" data-f="power_w" data-num="1" value="${esc(r.power_w)}"></td>
      <td><input class="hour-input" type="number" min="0" max="100" data-f="battery_soc" data-num="1" value="${esc(r.battery_soc)}"></td>
      <td><input class="hour-input" type="number" min="0" step="10" data-f="grid_export_limit_w" data-num="1" value="${esc(r.grid_export_limit_w || 0)}"></td>
      <td class="nowrap">
        <button type="button" class="btn btn-secondary btn-xs" data-act="up" ${i === 0 ? "disabled" : ""} title="Выше">↑</button>
        <button type="button" class="btn btn-secondary btn-xs" data-act="down" ${i >= intervals.length - 1 ? "disabled" : ""} title="Ниже">↓</button>
        <button type="button" class="btn btn-secondary btn-xs" data-act="dup" title="Дублировать">⧉</button>
        <button type="button" class="btn btn-danger btn-xs" data-act="del" title="Удалить">×</button>
      </td>
    </tr>`;
  }

  function renderIntervals() {
    const body = $("ssIntervals");
    const empty = $("ssIntervalEmpty");
    if (!intervals.length) {
      body.innerHTML = "";
      empty.style.display = "";
      $("ssIntervalTable").style.display = "none";
    } else {
      empty.style.display = "none";
      $("ssIntervalTable").style.display = "table";
      const y = window.scrollY;
      body.innerHTML = intervals.map(rowHTML).join("");
      window.scrollTo(0, y);
    }
    renderLocalTimeline();
    updateAdvancedWarn();
  }

  function renderLocalTimeline() {
    const el = $("ssTimelineLocal");
    if (!el) return;
    if (!intervals.length) { el.innerHTML = ""; return; }
    const blocks = intervals.map((r, i) => {
      const toMin = t => t === "24:00" ? 1440 : (() => { const [h, m] = (t || "0:0").split(":").map(Number); return h * 60 + (m || 0); })();
      let s = toMin(r.start_time), e = toMin(r.end_time);
      const mk = (a, b) => `<button type="button" class="tl-block gc-${r.grid_charge_enabled ? "on" : "off"}" data-i="${i}" style="left:${(a / 1440) * 100}%;width:${Math.max(((b - a) / 1440) * 100, 0.8)}%">
        <span>${esc(r.start_time)}–${esc(r.end_time)}</span>
        <small>сеть ${r.grid_charge_enabled ? "вкл" : "выкл"} · LL ${r.load_limit_mode} · P ${r.priority_load}</small>
      </button>`;
      if (e > s) return mk(s, e);
      return mk(s, 1440) + (e > 0 ? mk(0, e) : "");
    }).join("");
    el.innerHTML = `<div class="tl-track">${blocks}</div><div class="tl-axis"><span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></div>`;
    el.querySelectorAll(".tl-block").forEach(b => b.addEventListener("click", () => {
      const row = document.querySelector(`#ssIntervals tr[data-i="${b.dataset.i}"]`);
      row?.classList.add("highlight");
      setTimeout(() => row?.classList.remove("highlight"), 1200);
    }));
  }

  function updateAdvancedWarn() {
    const box = $("ssAdvancedWarn");
    if (!box) return;
    if (advancedReasons.length || preservedJSON) {
      box.classList.remove("hidden");
      box.innerHTML = `<b>Сохранены параметры расширенного режима.</b> ${esc((advancedReasons || []).join("; ") || "Исходный schedule JSON удерживается для совместимости при экспорте/дополнении.")}
        Упрощённый редактор их не показывает, но не удаляет при режиме «Дополнить».`;
    } else box.classList.add("hidden");
  }

  $("ssIntervals").addEventListener("input", onEdit);
  $("ssIntervals").addEventListener("change", onEdit);
  function onEdit(ev) {
    const tr = ev.target.closest("tr[data-i]");
    if (!tr) return;
    const r = intervals[Number(tr.dataset.i)];
    const el = ev.target;
    if (!el.dataset.f) return;
    if (el.type === "checkbox") {
      r[el.dataset.f] = el.checked;
    } else if (el.dataset.f === "start_time") {
      r.start_time = normalizeTimeValue(el.value, false);
    } else if (el.dataset.f === "end_time") {
      r.end_time = normalizeTimeValue(el.value, true);
    } else r[el.dataset.f] = el.dataset.num ? Number(el.value) : el.value;
    markDirty();
    if (el.dataset.f === "start_time" || el.dataset.f === "end_time") renderIntervals();
    else renderLocalTimeline();
    schedulePreview();
  }

  $("ssIntervals").addEventListener("click", ev => {
    const b = ev.target.closest("button[data-act]");
    if (!b) return;
    const i = Number(b.closest("tr").dataset.i);
    if (b.dataset.act === "del") intervals.splice(i, 1);
    if (b.dataset.act === "dup") intervals.splice(i + 1, 0, JSON.parse(JSON.stringify(intervals[i])));
    if (b.dataset.act === "up" && i > 0) [intervals[i - 1], intervals[i]] = [intervals[i], intervals[i - 1]];
    if (b.dataset.act === "down" && i < intervals.length - 1) [intervals[i + 1], intervals[i]] = [intervals[i], intervals[i + 1]];
    markDirty();
    renderIntervals();
    schedulePreview();
  });

  $("ssAddInterval").addEventListener("click", () => {
    const last = intervals[intervals.length - 1];
    const next = newInterval();
    if (last) {
      next.start_time = last.end_time === "24:00" ? "00:00" : last.end_time;
      const [hs, ms] = next.start_time.split(":").map(Number);
      let end = hs * 60 + (ms || 0) + 6 * 60;
      next.end_time = end >= 24 * 60 ? "24:00" : `${pad2(Math.floor(end / 60))}:${pad2(end % 60)}`;
    }
    intervals.push(next);
    markDirty();
    renderIntervals();
    schedulePreview();
  });

  $("ssMergeAdj").addEventListener("click", () => {
    if (intervals.length < 2) return;
    const out = [];
    for (const r of intervals) {
      const prev = out[out.length - 1];
      const same = prev && prev.end_time === r.start_time && prev.end_time !== "00:00" &&
        !!prev.grid_charge_enabled === !!r.grid_charge_enabled &&
        Number(prev.load_limit_mode) === Number(r.load_limit_mode) &&
        Number(prev.priority_load) === Number(r.priority_load) &&
        Number(prev.power_w) === Number(r.power_w) &&
        Number(prev.battery_soc) === Number(r.battery_soc);
      if (same) prev.end_time = r.end_time;
      else out.push(JSON.parse(JSON.stringify(r)));
    }
    intervals = out;
    markDirty();
    renderIntervals();
    schedulePreview();
    showPopup("Соседние одинаковые интервалы объединены", "success");
  });

  function updateLimits() {
    const m = models.find(x => x.key === modelKey);
    if (!m) { $("ssLimits").textContent = ""; return; }
    $("ssLimits").textContent = `${m.name}: мощность до ${m.power_max_w} Вт, экспорт до ${m.export_max_w} Вт`;
  }

  async function showExisting() {
    const out = $("ssExisting");
    if (!inverterID) { out.innerHTML = ""; return; }
    const data = await App.api("/api/simple-schedule/analyze?inverter_id=" + inverterID);
    if (!data.ok || !data.exists) {
      out.innerHTML = '<div class="notice notice-info small">У инвертора ещё нет сохранённого расписания.</div>';
      return;
    }
    const an = data.analysis || {};
    out.innerHTML = `<div class="notice ${an.representable ? "notice-info" : "notice-warn"} small">
      Текущее расписание «${esc(data.name)}» (${data.enabled ? "включено" : "выключено"}).
      ${an.representable ? "Можно загрузить в редактор." : "Есть особенности расширенного режима: " + esc((an.reasons || []).join("; "))}</div>`;
  }

  function cleanRules() {
    const wd = periodWeekdays();
    const df = $("ssDateFrom").value || "";
    const dt = $("ssDateTo").value || "";
    return intervals.map(r => {
      const o = {
        name: r.name || "", mode: "custom",
        start_time: r.start_time, end_time: r.end_time,
        start_hour: Number((r.start_time || "0:0").split(":")[0]),
        end_hour: r.end_time === "24:00" ? 24 : Number((r.end_time || "0:0").split(":")[0]),
        grid_charge_enabled: !!r.grid_charge_enabled,
        load_limit_mode: Number(r.load_limit_mode) || 0,
        priority_load: Number(r.priority_load) || 0,
        power_w: Number(r.power_w) || 0,
        battery_soc: Number(r.battery_soc) || 0,
        grid_export_limit_w: Number(r.grid_export_limit_w) || 0,
        solar_export: false
      };
      const w = (r.weekdays && r.weekdays.length) ? r.weekdays : wd;
      if (w.length) o.weekdays = w;
      if (df || r.date_from) o.date_from = r.date_from || df;
      if (dt || r.date_to) o.date_to = r.date_to || dt;
      return o;
    });
  }

  function buildRequest() {
    return {
      inverter_ids: inverterID ? [inverterID] : [],
      month: $("ssMonth").value,
      rules: cleanRules(),
      base_mode: document.querySelector("input[name=ssBase]:checked")?.value || "replace",
      use_timer_mask: 255,
      name: $("ssName").value.trim() || "Упрощённое расписание",
      auto_renew: $("ssAutoRenew").checked
    };
  }

  function renderViz() {
    if (!lastResult) return;
    const modeCategory = iv => {
      const cm = iv.charge_mode || 0;
      const sell = (cm & 32) !== 0, grid = (cm & 1) !== 0;
      return sell && grid ? "sell_grid" : sell ? "sell" : grid ? "grid_charge" : "self";
    };
    const data = ScheduleViz.fromPreview(lastResult, modeCategory);
    if (view === "calendar") ScheduleViz.calendar($("ssViz"), data);
    else ScheduleViz.timeline($("ssViz"), data);
  }
  document.querySelectorAll("[data-view]").forEach(b => b.addEventListener("click", () => { view = b.dataset.view; renderViz(); }));

  const schedulePreview = App.debounce(preview, 350);
  async function preview() {
    const msgs = $("ssMessages");
    if (!intervals.length) {
      $("ssViz").innerHTML = '<div class="empty">Добавьте интервалы — предпросмотр обновится автоматически.</div>';
      msgs.innerHTML = ""; $("ssDescriptions").innerHTML = ""; lastResult = null;
      return;
    }
    previewController?.abort();
    previewController = new AbortController();
    const data = await App.api("/api/simple-schedule/preview", {method: "POST", json: buildRequest(), signal: previewController.signal});
    if (data.aborted) return;
    if (!data.result) { lastResult = null; msgs.innerHTML = `<div class="notice notice-err small">${esc(data.message)}</div>`; return; }
    const res = data.result;
    lastResult = res;
    $("ssDescriptions").innerHTML = `<ol>${(res.descriptions || []).map(d => `<li>${esc(d)}</li>`).join("")}</ol>
      <div class="muted">Часов с покрытием: ${res.enabled_hours} · блоков/день: ${res.logical_day_slots || "—"} · аппаратных слотов TOU: ${res.hardware_slots || 6}${(res.interval_spans || []).some(s => s.start_min % 60 || s.end_min % 60) ? " · есть интервалы с шагом 5 мин" : ""}</div>`;
    msgs.innerHTML =
      (res.exceeds_hardware_slots ? `<div class="notice notice-warn small"><b>Больше 6 аппаратных программ TOU.</b> Интервалы не обрезаются; планировщик применяет почасово.</div>` : "") +
      (res.validation_errors || []).map(e => `<div class="notice notice-err small">${esc(e)}</div>`).join("") +
      (res.destructive || []).map(e => `<div class="notice notice-warn small">⚠ ${esc(e)}</div>`).join("") +
      (res.conflicts?.length ? `<details class="plain small"><summary>Пересечения: ${res.conflicts.length}</summary><ul>${res.conflicts.slice(0, 40).map(c => `<li>${esc(c.message)}</li>`).join("")}</ul></details>` : "") +
      (res.warnings || []).filter(w => !String(w).includes("аппаратных")).map(e => `<div class="notice notice-info small">${esc(e)}</div>`).join("");
    renderViz();
  }

  async function saveSchedule(opts) {
    if (!inverterID) { showPopup("Инвертор не настроен", "error"); return null; }
    const req = buildRequest();
    if (!req.rules.length) { showPopup("Добавьте интервалы", "error"); return null; }
    let res = await App.api("/api/simple-schedule/apply", {method: "POST", json: req});
    if (res.needs_confirmation) {
      if (!await App.confirmDialog({title: "Подтвердите изменение", html: `<ul>${(res.destructive || []).map(d => `<li>${esc(d)}</li>`).join("")}</ul>`, confirmText: "Сохранить", danger: true})) return null;
      req.confirm_destructive = true;
      res = await App.api("/api/simple-schedule/apply", {method: "POST", json: req});
    }
    if (res.ok) {
      markDirty(false);
      baseline = JSON.parse(JSON.stringify(intervals));
      preservedJSON = "";
      advancedReasons = [];
      updateAdvancedWarn();
    }
    showPopup(res.message || (res.ok ? "Сохранено" : "Ошибка"), res.ok ? "success" : "error");
    if (res.ok && opts?.runNow) {
      const run = await App.api("/api/tasks/run-now", {method: "POST"});
      showPopup(run.message || (run.ok ? "Планировщик запущен" : "Не удалось запустить"), run.ok ? "success" : "error");
    }
    if (res.ok) showExisting();
    return res;
  }

  $("ssApply").addEventListener("click", ev => App.busy(ev.currentTarget, () => saveSchedule({runNow: false})));
  $("ssApplyNow").addEventListener("click", async ev => {
    if (!await App.confirmDialog({
      title: "Сохранить и отправить сейчас?",
      html: "<p>Расписание будет сохранено, затем планировщик запишет регистры TOU с контрольным чтением.</p>",
      confirmText: "Сохранить и отправить", danger: true
    })) return;
    await App.busy(ev.currentTarget, () => saveSchedule({runNow: true}));
  });

  $("ssReset").addEventListener("click", async () => {
    if (dirty && !await App.confirmDialog({title: "Сбросить изменения?", html: "<p>Несохранённые правки будут потеряны.</p>", confirmText: "Сбросить", danger: true})) return;
    intervals = JSON.parse(JSON.stringify(baseline));
    markDirty(false);
    renderIntervals();
    schedulePreview();
  });

  async function loadRulesFromScheduleJSON(scheduleJSON, meta) {
    const data = await App.api("/api/simple-schedule/extract-rules", {method: "POST", json: {schedule_json: scheduleJSON}});
    if (!data.ok) { showPopup(data.message || "Не удалось разобрать расписание", "error"); return false; }
    const rules = data.rules || [];
    intervals = rules.map(r => {
      const n = newInterval(r);
      if (r.start_time) n.start_time = normalizeTimeValue(r.start_time, false);
      else if (r.start_hour !== undefined) n.start_time = `${pad2(r.start_hour)}:00`;
      if (r.end_time) n.end_time = normalizeTimeValue(r.end_time, true);
      else if (r.end_hour !== undefined) n.end_time = r.end_hour === 24 ? "24:00" : `${pad2(r.end_hour)}:00`;
      n.grid_charge_enabled = !!r.grid_charge_enabled;
      if (r.grid_export_limit_w != null) n.grid_export_limit_w = Number(r.grid_export_limit_w) || 0;
      return n;
    });
    const an = data.analysis || {};
    advancedReasons = an.representable ? [] : (an.reasons || []);
    preservedJSON = advancedReasons.length ? scheduleJSON : "";
    $("ssPreservedJSON").value = preservedJSON;
    if (meta?.name) $("ssName").value = meta.name;
    markDirty(true);
    renderIntervals();
    schedulePreview();
    updateAdvancedWarn();
    return true;
  }

  $("ssReloadCurrent").addEventListener("click", async () => {
    if (!inverterID) return;
    if (dirty && !await App.confirmDialog({title: "Заменить содержимое редактора?", html: "<p>Текущие несохранённые интервалы будут заменены сохранённым расписанием.</p>", confirmText: "Загрузить"})) return;
    const an = await App.api("/api/simple-schedule/analyze?inverter_id=" + inverterID);
    if (!an.exists) { showPopup("Нет сохранённого расписания", "error"); return; }
    const tl = await App.api(`/api/schedules/timeline?inverter_id=${inverterID}&month=${$("ssMonth").value}`);
    if (!tl.schedule_json) { showPopup("Не удалось получить JSON расписания", "error"); return; }
    await loadRulesFromScheduleJSON(tl.schedule_json, {name: an.name || tl.name});
    baseline = JSON.parse(JSON.stringify(intervals));
    markDirty(false);
    showPopup("Текущее расписание загружено", "success");
  });

  async function refreshTemplateSelect(preferredNames = []) {
    const select = $("ssTemplateSelect");
    if (!select) return "";
    const response = await fetch("/api/templates");
    const data = await response.json();
    if (!response.ok || !data.ok || !Array.isArray(data.items)) return select.value || "";
    const current = select.value;
    select.innerHTML = '<option value="">-- выбрать шаблон --</option>';
    data.items.forEach(item => {
      const opt = document.createElement("option");
      opt.value = item.id;
      opt.textContent = item.name;
      select.appendChild(opt);
    });
    const preferred = preferredNames.find(name => Array.from(select.options).some(opt => opt.textContent === name));
    if (preferred) {
      const opt = Array.from(select.options).find(opt => opt.textContent === preferred);
      if (opt) select.value = opt.value;
    } else if (current && Array.from(select.options).some(opt => opt.value === current)) {
      select.value = current;
    }
    return select.value || "";
  }

  async function loadTemplateIntoEditor(templateID, confirmReplace) {
    if (!templateID) return false;
    if (confirmReplace && dirty && intervals.length && !await App.confirmDialog({
      title: "Применить шаблон?",
      html: "<p>Текущие интервалы редактора будут заменены. В инвертор ничего не пишется.</p>",
      confirmText: "Применить"
    })) return false;
    const response = await fetch("/templates/apply?id=" + encodeURIComponent(templateID));
    const data = await response.json();
    if (!response.ok || !data.ok) { showPopup(data.message || "Ошибка загрузки шаблона", "error"); return false; }
    const ok = await loadRulesFromScheduleJSON(data.schedule_json || "{}", {name: data.name});
    if (ok) {
      $("ssTemplateMsg").innerHTML = `<div class="notice notice-info small">Шаблон «${esc(data.name)}» загружен в редактор. Сохраните расписание отдельно, если нужно применить к устройству.</div>`;
      showPopup("Шаблон загружен в редактор", "success");
    }
    return ok;
  }

  $("ssApplyTemplateBtn").addEventListener("click", () => loadTemplateIntoEditor($("ssTemplateSelect").value, true));
  $("ssTemplateSelect").addEventListener("change", function () {
    if (this.value) loadTemplateIntoEditor(this.value, true);
  });

  $("ssUploadTemplateBtn").addEventListener("click", () => $("ssUploadTemplateFile").click());
  $("ssUploadTemplateFile").addEventListener("change", async function () {
    const file = this.files && this.files[0];
    if (!file) return;
    const btn = $("ssUploadTemplateBtn");
    const original = btn.textContent;
    btn.disabled = true; btn.textContent = "Загружаю...";
    try {
      const fdPreview = new FormData();
      fdPreview.append("file", file);
      const prevR = await fetch("/api/templates/import-preview", {method: "POST", body: fdPreview});
      const prev = await prevR.json().catch(() => ({ok: false}));
      if (prev.ok && Array.isArray(prev.templates)) {
        const list = prev.templates.map(t => `<li>${esc(t.name)}${t.error ? " — <span style=color:var(--danger)>" + esc(t.error) + "</span>" : (t.exists ? " (уже есть)" : "")}</li>`).join("");
        $("ssTemplateMsg").innerHTML = `<div class="notice notice-info small"><b>Предпросмотр XLSX «${esc(prev.file || file.name)}»</b><ul>${list}</ul></div>`;
      }
      const formData = new FormData();
      formData.append("xlsx_file", file);
      const response = await fetch("/templates/import/upload?format=json", {
        method: "POST", headers: {Accept: "application/json"}, body: formData
      });
      const data = await response.json();
      if (!response.ok || !data.ok) {
        showPopup(data.message || "Ошибка загрузки шаблона", "error");
        return;
      }
      const imported = Array.isArray(data.imported) ? data.imported : [];
      const created = imported.filter(item => item.created);
      const selected = await refreshTemplateSelect(created.map(item => item.name));
      if (created.length > 0 && selected) await loadTemplateIntoEditor(selected, false);
      const failed = imported.length - created.length;
      showPopup(failed > 0 ? `Загружено: ${created.length}, ошибок: ${failed}` : (data.message || `Загружено шаблонов: ${created.length}`), failed ? "error" : "success");
    } catch (e) {
      showPopup(e.message || "Ошибка соединения", "error");
    } finally {
      this.value = "";
      btn.disabled = false; btn.textContent = original;
    }
  });

  $("ssSaveTemplateBtn").addEventListener("click", async () => {
    if (!intervals.length) { showPopup("Нет интервалов для шаблона", "error"); return; }
    const name = prompt("Введите имя шаблона");
    if (!name) return;
    const description = prompt("Описание шаблона", "") || "";
    const buildReq = Object.assign(buildRequest(), {include_json: true, base_mode: "replace"});
    const pdata = await App.api("/api/simple-schedule/preview", {method: "POST", json: buildReq});
    if (!pdata.result || (pdata.result.validation_errors || []).length) {
      showPopup(pdata.message || (pdata.result?.validation_errors || []).join("; ") || "Исправьте ошибки предпросмотра", "error");
      return;
    }
    let scheduleJSON = pdata.result.schedule_json || "";
    if (preservedJSON && advancedReasons.length && !dirty) {
      scheduleJSON = preservedJSON;
    } else if (preservedJSON && advancedReasons.length && dirty) {
      showPopup("Исходные расширенные параметры будут заменены пересобранным расписанием", "info");
    }
    if (!scheduleJSON) { showPopup("Не удалось получить JSON расписания для шаблона", "error"); return; }
    const params = new URLSearchParams();
    params.append("name", name);
    params.append("description", description);
    params.append("view_mode", "grid");
    params.append("schedule_json", scheduleJSON);
    const response = await fetch("/templates", {method: "POST", headers: {"Content-Type": "application/x-www-form-urlencoded", Accept: "application/json"}, body: params});
    const data = await response.json().catch(() => ({ok: false}));
    showPopup(data.message || (data.ok ? "Шаблон сохранён (XLSX-совместимый)" : "Ошибка"), data.ok ? "success" : "error");
    if (data.ok) refreshTemplateSelect([name]);
  });

  $("ssDownloadXlsxBtn").addEventListener("click", async () => {
    if (!intervals.length && !(preservedJSON && !dirty)) { showPopup("Нет интервалов", "error"); return; }
    const btn = $("ssDownloadXlsxBtn");
    await App.busy(btn, async () => {
      if (preservedJSON && advancedReasons.length && !dirty && inverterID) {
        window.location.href = "/schedules/export?ids=" + inverterID;
        showPopup("Скачивание сохранённого XLSX (с расширенными параметрами)", "success");
        return;
      }
      const response = await fetch("/api/simple-schedule/export-xlsx", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(buildRequest())
      });
      if (!response.ok) {
        const err = await response.json().catch(() => ({}));
        showPopup(err.message || "Ошибка экспорта XLSX", "error");
        return;
      }
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "simple-schedule.xlsx";
      a.click();
      URL.revokeObjectURL(url);
      showPopup("XLSX скачан", "success");
    });
  });

  ["ssMonth", "ssDateFrom", "ssDateTo"].forEach(id => $(id)?.addEventListener("change", () => { markDirty(); schedulePreview(); }));
  document.querySelectorAll("input[name=ssBase]").forEach(x => x.addEventListener("change", schedulePreview));
  $("ssWeekdays")?.addEventListener("change", () => { markDirty(); schedulePreview(); });

  window.addEventListener("beforeunload", e => { if (dirty) { e.preventDefault(); e.returnValue = ""; } });

  updateLimits();
  showExisting();
  renderIntervals();
  if (inverterID) {
    App.api(`/api/schedules/timeline?inverter_id=${inverterID}&month=${$("ssMonth").value}`).then(async tl => {
      if (!tl.ok || !tl.schedule_json) return;
      await loadRulesFromScheduleJSON(tl.schedule_json, {name: tl.name || tl.title});
      baseline = JSON.parse(JSON.stringify(intervals));
      markDirty(false);
      updateAdvancedWarn();
    }).catch(() => {});
  }
})();
