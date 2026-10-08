(function () {
  const cfg = JSON.parse(document.getElementById("rtConfig").textContent || "{}");
  const esc = App.esc;
  const $ = id => document.getElementById(id);
  const invSel = $("rtInverter"), modelSel = $("rtModel");
  const listEl = $("rtList"), detailEl = $("rtDetail");
  let items = [];
  let selected = null;
  let lastRead = null; // {code, raw:[...], at}
  let listController = null;

  const confLabel = {verified: ["badge-ok", "подтверждено"], documented: ["badge-info", "по документации"], unverified: ["badge-warn", "не проверено"], unknown: ["badge-muted", "неизвестно"]};

  document.querySelectorAll(".tabbar button").forEach(b => b.addEventListener("click", () => {
    document.querySelectorAll(".tabbar button").forEach(x => x.classList.toggle("active", x === b));
    document.querySelectorAll(".tabpanel").forEach(p => p.classList.toggle("active", p.id === b.dataset.tab));
    if (b.dataset.tab === "tabProfiles") loadProfiles();
  }));

  const inverterId = () => Number(invSel.value) || 0;
  const modelKey = () => modelSel.value;

  invSel.addEventListener("change", () => {
    const opt = invSel.selectedOptions[0];
    if (opt && opt.dataset.model) modelSel.value = opt.dataset.model;
    modelSel.disabled = !!inverterId();
    selected = null; lastRead = null;
    detailEl.innerHTML = '<div class="empty">Выберите регистр в списке слева.</div>';
    loadList();
  });
  modelSel.addEventListener("change", loadList);

  async function loadList() {
    listController?.abort();
    listController = new AbortController();
    const p = new URLSearchParams({model_key: modelKey(), q: $("rtSearch").value.trim()});
    if (inverterId()) p.set("inverter_id", inverterId());
    if ($("rtWritableOnly").checked) p.set("writable", "1");
    listEl.innerHTML = '<tr><td colspan="5"><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-line"></div></td></tr>';
    const data = await App.api("/api/registers?" + p, {signal: listController.signal});
    if (data.aborted) return;
    if (!data.ok) { listEl.innerHTML = `<tr><td colspan="5" class="empty">${esc(data.message)}</td></tr>`; return; }
    items = data.items;
    $("rtCount").textContent = `${data.model_name}: найдено ${data.total} · файл ${data.file}`;
    const issues = data.issues || [];
    const errs = issues.filter(i => i.severity === "error").length, warns = issues.filter(i => i.severity === "warning").length;
    $("rtProfileIssues").innerHTML = (data.write_check_error ? `<div class="notice notice-warn small" style="margin-top:10px">Проверка безопасности записи не пройдена — запись для этой модели заблокирована, чтение доступно. ${esc(data.write_check_error)}</div>` : "")
      + (errs || warns ? `<details class="plain small" style="margin-top:8px"><summary>Замечания структурной проверки: ошибок ${errs}, предупреждений ${warns}</summary><ul>${issues.map(i => `<li>[${esc(i.severity)}] ${i.address ? "рег. " + i.address + ": " : ""}${esc(i.message)}</li>`).join("")}</ul></details>` : "");
    listEl.innerHTML = items.map((d, i) => {
      const [cls, lbl] = confLabel[d.confidence_level] || confLabel.unknown;
      return `<tr class="expandable${selected && selected.code === d.code ? " active" : ""}" data-i="${i}">
        <td class="mono">${d.address}${d.addresses && d.addresses.length > 1 ? "…" : ""}</td>
        <td><b class="mono small">${esc(d.code)}</b><div class="small">${esc(d.name)}</div></td>
        <td class="small">${esc(d.kind_label)}</td>
        <td>${d.writable ? '<span class="badge plain badge-warn">запись</span>' : '<span class="badge plain badge-muted">чтение</span>'}</td>
        <td><span class="badge plain ${cls}">${lbl}</span></td></tr>`;
    }).join("") || '<tr><td colspan="5" class="empty">Ничего не найдено</td></tr>';
  }

  $("rtSearch").addEventListener("input", App.debounce(loadList, 300));
  $("rtWritableOnly").addEventListener("change", loadList);
  listEl.addEventListener("click", ev => {
    const tr = ev.target.closest("tr[data-i]");
    if (!tr) return;
    selected = items[Number(tr.dataset.i)];
    lastRead = null;
    listEl.querySelectorAll("tr").forEach(r => r.classList.toggle("active", r === tr));
    renderDetail();
  });

  const fmtNum = v => (v === undefined || v === null) ? "—" : String(v);

  function renderDetail() {
    const d = selected;
    if (!d) return;
    const [cls, lbl] = confLabel[d.confidence_level] || confLabel.unknown;
    const enumRows = d.enum_labels ? Object.entries(d.enum_labels).map(([k, v]) => `${esc(k)} — ${esc(v)}`).join("<br>") : "—";
    const bits = (d.bits || []).map(b => `<tr><td class="mono">${b.bit}${(b.width || 1) > 1 ? "–" + (b.bit + b.width - 1) : ""}</td><td>${esc(b.name)}${b.description ? `<div class="small muted">${esc(b.description)}</div>` : ""}</td>
      <td class="small">${b.values ? Object.entries(b.values).map(([k, v]) => `${esc(k)}=${esc(v)}`).join(", ") : "—"}</td><td>${b.writable ? "да" : "нет"}</td></tr>`).join("");
    const canRead = inverterId() > 0;
    const canWrite = canRead && cfg.writesEnabled && d.writable && (d.addresses || []).length <= 1;
    detailEl.innerHTML = `
      <div class="row between"><div><h2 class="mb-0 mono">${esc(d.code)}</h2><div>${esc(d.name)}</div></div>
        <div class="row"><span class="badge ${cls}">${lbl}</span><span class="badge badge-info">${esc(d.kind_label)}</span></div></div>
      ${d.description ? `<p class="small">${esc(d.description)}</p>` : ""}
      ${(d.warnings || []).map(w => `<div class="notice notice-warn small">${esc(w)}</div>`).join("")}
      <dl class="kv small" style="margin-top:10px">
        <dt>Адрес</dt><dd class="mono">${(d.addresses || [d.address]).join(", ")} (0x${Number(d.address).toString(16).toUpperCase().padStart(4, "0")})</dd>
        <dt>Тип регистра</dt><dd>${esc(d.register_type)} · ${esc(d.data_type || "uint16")} · ${d.width} бит${d.signed ? " · со знаком" : ""}</dd>
        <dt>Raw-диапазон</dt><dd>${fmtNum(d.raw_min)} … ${fmtNum(d.raw_max)}</dd>
        <dt>Логический диапазон</dt><dd>${fmtNum(d.min)} … ${fmtNum(d.max)} ${esc(d.unit || "")}${d.step ? ", шаг " + d.step : ""}</dd>
        <dt>Масштаб</dt><dd>чтение ×${d.read_scale}, запись ×${d.write_scale}${d.offset ? ", смещение " + d.offset : ""}</dd>
        <dt>Допустимые значения</dt><dd>${d.allowed_values ? esc(d.allowed_values.join(", ")) + (d.allowed_values_ignored ? ' <span class="badge plain badge-warn">не применяется: регистр описан битами</span>' : "") : "—"}</dd>
        <dt>Значения перечисления</dt><dd>${enumRows}</dd>
        <dt>Режим записи</dt><dd>${esc(d.write_mode || "прямая запись")}${d.write_bitmask !== undefined ? ` · маска записи 0x${Number(d.write_bitmask).toString(16).toUpperCase()} (${d.write_bitmask})` : ""}</dd>
        <dt>Источник</dt><dd>${esc(d.source || "—")}</dd>
        <dt>Достоверность</dt><dd>${esc(d.confidence || "—")}</dd>
        <dt>Версия протокола</dt><dd>${esc(d.protocol_version || "—")}</dd>
        <dt>Модель</dt><dd>${esc(d.model_name)}${d.compatible_models && d.compatible_models.length ? `<div class="muted">тот же адрес и описание в: ${esc(d.compatible_models.join(", "))}</div>` : ""}</dd>
        ${d.shared_address_codes && d.shared_address_codes.length ? `<dt>Другие поля адреса</dt><dd class="mono">${esc(d.shared_address_codes.join(", "))}</dd>` : ""}
        ${d.incomplete && d.incomplete.length ? `<dt>Не описано</dt><dd class="muted">${esc(d.incomplete.join("; "))}</dd>` : ""}
        ${d.notes ? `<dt>Примечания</dt><dd>${esc(d.notes)}</dd>` : ""}
      </dl>
      ${bits ? `<h3>Биты</h3><div class="table-scroll"><table class="data-table"><thead><tr><th>Бит</th><th>Назначение</th><th>Значения</th><th>Запись</th></tr></thead><tbody>${bits}</tbody></table></div>` : ""}
      <div class="button-row" style="margin-top:12px">
        <button type="button" class="btn btn-primary" id="dRead" ${canRead ? "" : "disabled title='Выберите инвертор'"}>Прочитать с инвертора</button>
        <a class="btn btn-secondary" href="/history?register=${d.address}">История регистра</a>
        <button type="button" class="btn btn-secondary" id="dObs">Наблюдения</button>
      </div>
      ${canRead ? "" : '<p class="small muted">Для чтения и записи выберите инвертор вверху страницы.</p>'}
      <div id="dReadResult"></div>
      ${canWrite ? writeFormHTML(d) : (d.writable && canRead && !cfg.writesEnabled ? '<div class="notice notice-info small" style="margin-top:12px">Запись со страницы тестирования отключена в настройках (раздел «Функции»).</div>' : "")}
      ${metaFormHTML(d)}`;
    $("dRead")?.addEventListener("click", ev => readSelected(ev.currentTarget));
    $("dObs")?.addEventListener("click", () => { $("obsAddr").value = d.address; document.querySelector('[data-tab="tabObs"]').click(); loadObservations(); });
    bindWriteForm(d);
    bindMetaForm(d);
  }

  function decodedHTML(dec) {
    if (!dec) return "";
    const bits = (dec.bits || []).map(b => `<tr><td class="mono">${b.bit}</td><td>${esc(b.name)}</td><td class="mono">${b.value}</td><td>${esc(b.meaning)}</td></tr>`).join("");
    return `<dl class="kv small"><dt>Raw</dt><dd class="mono">${(dec.raw || []).join(", ")} · ${(dec.hex || []).join(", ")}</dd>
      <dt>Двоичное</dt><dd class="mono">${(dec.binary || []).join(" ")}</dd>
      <dt>Со знаком</dt><dd class="mono">${(dec.signed || []).join(", ")}</dd>
      <dt>Значение</dt><dd><b>${esc(dec.decoded_text)}</b> ${esc(dec.unit || "")}${dec.enum_label ? " — " + esc(dec.enum_label) : ""}</dd>
      ${(dec.violations || []).length ? `<dt>Нарушения профиля</dt><dd style="color:var(--danger)">${esc(dec.violations.join("; "))}</dd>` : ""}</dl>
      ${bits ? `<table class="data-table small"><thead><tr><th>Бит</th><th>Назначение</th><th>Знач.</th><th>Смысл</th></tr></thead><tbody>${bits}</tbody></table>` : ""}`;
  }

  async function readSelected(btn) {
    const out = $("dReadResult");
    await App.busy(btn, async () => {
      const data = await App.api("/api/registers/read", {method: "POST", json: {inverter_id: inverterId(), code: selected.code}});
      if (!data.ok) { out.innerHTML = `<div class="notice notice-err small">${esc(data.message)}</div>`; return; }
      const v = data.values[0];
      const f = (v.fields || []).find(x => x.code === selected.code) || (v.fields || [])[0];
      lastRead = {code: selected.code, raw: v.raw, at: data.read_at_utc, decoded: f && f.decoded};
      out.innerHTML = `<div class="notice notice-ok small" style="margin-top:10px">Прочитано с инвертора ${App.fmtDate(data.read_at_utc)} · ${data.duration_ms} мс · попыток: ${data.attempts}</div>${decodedHTML(f ? f.decoded : null)}`;
      syncBitEditor();
    });
  }

  function writeFormHTML(d) {
    const hasBits = (d.bits || []).some(b => b.writable) && d.write_bitmask !== undefined;
    const rawAllowed = !d.write_mode;
    const bitControls = hasBits ? (d.bits || []).filter(b => b.writable).map(b => {
      const w = b.width || 1;
      const opts = b.values && Object.keys(b.values).length ? Object.entries(b.values) : Array.from({length: 1 << w}, (_, i) => [String(i), String(i)]);
      return `<div class="field"><label>Бит ${b.bit}${w > 1 ? "–" + (b.bit + w - 1) : ""}: ${esc(b.name)}</label>
        <select data-bit="${b.bit}"><option value="">не менять</option>${opts.map(([k, v]) => `<option value="${esc(k)}">${esc(k)} — ${esc(v)}</option>`).join("")}</select></div>`;
    }).join("") : "";
    return `<div class="card" style="margin-top:14px;border-color:rgba(245,158,11,.45)">
      <h3 class="mt-0">Запись значения</h3>
      <p class="small muted">Порядок: предпросмотр (читает текущее значение, ничего не пишет) → подтверждение с вводом адреса → запись → контрольное чтение. Автоматических повторов записи нет.${d.write_bitmask !== undefined ? " Биты вне маски записи сохраняются (чтение-изменение-запись)." : ""}</p>
      <div class="tabbar" id="wModes">
        <button type="button" class="active" data-mode="logical">Логическое значение</button>
        ${hasBits ? '<button type="button" data-mode="bits">Редактор битов</button>' : ""}
        ${rawAllowed ? '<button type="button" data-mode="raw">Raw-значение</button>' : ""}
      </div>
      <div data-panel="logical"><div class="field"><label for="wValue">Значение${d.unit ? ", " + esc(d.unit) : ""} ${d.min !== undefined ? `(${d.min} … ${d.max})` : ""}</label>
        <input type="number" id="wValue" step="any"></div></div>
      ${hasBits ? `<div data-panel="bits" class="hidden"><p class="small muted">Показаны только описанные в профиле записываемые биты. Прочитайте регистр, чтобы увидеть текущие значения.</p><div class="grid-2" id="wBits">${bitControls}</div><div id="wBitsGrid" style="margin-top:8px"></div></div>` : ""}
      ${rawAllowed ? `<div data-panel="raw" class="hidden"><div class="field"><label for="wRaw">Raw (0–65535)</label><input type="number" id="wRaw" min="0" max="65535"></div></div>` : ""}
      <div class="field"><label for="wNote">Комментарий к операции</label><input type="text" id="wNote" placeholder="зачем выполняется запись"></div>
      <button type="button" class="btn btn-warning" id="wPreview">Предпросмотр записи</button>
    </div>`;
  }

  let writeMode = "logical";
  function bindWriteForm(d) {
    writeMode = "logical";
    const modes = $("wModes");
    if (!modes) return;
    modes.addEventListener("click", ev => {
      const b = ev.target.closest("button[data-mode]");
      if (!b) return;
      writeMode = b.dataset.mode;
      modes.querySelectorAll("button").forEach(x => x.classList.toggle("active", x === b));
      detailEl.querySelectorAll("[data-panel]").forEach(p => p.classList.toggle("hidden", p.dataset.panel !== writeMode));
    });
    $("wPreview").addEventListener("click", ev => previewWrite(d, ev.currentTarget));
    syncBitEditor();
  }

  function syncBitEditor() {
    const gridEl = $("wBitsGrid");
    if (!gridEl || !selected) return;
    const raw = lastRead && lastRead.code === selected.code ? lastRead.raw : null;
    const known = new Set();
    (selected.bits || []).forEach(b => { for (let i = 0; i < (b.width || 1); i++) known.add(b.bit + i); });
    gridEl.innerHTML = raw === null ? '<span class="small muted">Текущее значение не прочитано.</span>' :
      '<div class="bits-grid">' + Array.from({length: 16}, (_, i) => 15 - i).map(bit =>
        `<div class="bit-cell${(raw >> bit) & 1 ? " on" : ""}${known.has(bit) ? " known" : ""}" title="${known.has(bit) ? "описан в профиле" : "не описан — не изменяется"}"><span class="bit-no">${bit}</span>${(raw >> bit) & 1}</div>`).join("") + "</div>";
    if (raw !== null) {
      document.querySelectorAll("#wBits select[data-bit]").forEach(sel => {
        const def = (selected.bits || []).find(b => b.bit === Number(sel.dataset.bit));
        const w = def ? (def.width || 1) : 1;
        const cur = (raw >> Number(sel.dataset.bit)) & ((1 << w) - 1);
        const first = sel.options[0];
        first.textContent = `не менять (сейчас ${cur})`;
      });
    }
  }

  function buildWriteRequest(d) {
    const req = {inverter_id: inverterId(), code: d.code, mode: writeMode, note: $("wNote").value.trim()};
    if (writeMode === "logical") {
      const v = $("wValue").value;
      if (v === "") throw new Error("Введите значение");
      req.value = Number(v);
    } else if (writeMode === "raw") {
      const v = $("wRaw").value;
      if (v === "") throw new Error("Введите raw-значение");
      req.value = Number(v);
    } else {
      req.bits = {};
      document.querySelectorAll("#wBits select[data-bit]").forEach(sel => { if (sel.value !== "") req.bits[sel.dataset.bit] = Number(sel.value); });
      if (!Object.keys(req.bits).length) throw new Error("Выберите хотя бы один бит для изменения");
    }
    return req;
  }

  async function previewWrite(d, btn) {
    let req;
    try { req = buildWriteRequest(d); } catch (e) { App.toast(e.message, "warning"); return; }
    const prev = await App.busy(btn, () => App.api("/api/registers/write-preview", {method: "POST", json: req}));
    if (!prev.ok) { App.toast(prev.message || "Предпросмотр не выполнен", "error"); return; }
    if (prev.no_change) { App.toast("Текущее значение уже совпадает с целевым — запись не нужна", "info"); return; }
    const html = `<p>${esc(prev.inverter)} · ${esc(prev.model)} · регистр <b class="mono">${prev.address}</b></p>
      <p class="small">${esc(prev.description)}</p>
      ${(prev.warnings || []).map(w => `<div class="notice notice-warn small">${esc(w)}</div>`).join("")}
      <div class="grid-2"><div><h4>Сейчас (прочитано ${App.fmtDate(prev.read_at_utc)})</h4>${decodedHTML(prev.current)}</div>
      <div><h4>Будет записано</h4>${decodedHTML(prev.target)}</div></div>
      <p class="small muted">Изменяемые биты: <span class="mono">${esc(prev.changed_bits)}</span>. Если значение регистра изменится до записи, операция будет остановлена.</p>`;
    const ok = await App.confirmDialog({title: "Подтверждение записи в инвертор", html, confirmText: "Записать", danger: true, requireText: String(prev.address)});
    if (!ok) { App.toast("Запись отменена", "info"); return; }
    const res = await App.api("/api/registers/write", {method: "POST", timeoutMs: 60000, json: Object.assign({}, req, {
      confirm: true, confirm_address: prev.address, expected_current: prev.expected_current, preview_token: prev.preview_token})});
    const out = $("dReadResult");
    if (res.ok) {
      App.toast("Записано и подтверждено контрольным чтением", "success");
      out.innerHTML = `<div class="notice notice-ok small" style="margin-top:10px">${esc(res.status_label)} · операция <span class="mono">${esc(res.operation_id)}</span></div>${decodedHTML(res.verified)}`;
      lastRead = {code: d.code, raw: res.outcome && res.outcome.verified_value !== undefined ? res.outcome.verified_value : null};
      if (lastRead.raw === null) lastRead = null;
      syncBitEditor();
    } else {
      App.toast(res.message || "Запись не подтверждена", "error");
      out.innerHTML = `<div class="notice notice-err small" style="margin-top:10px">${esc(res.status_label || "Ошибка")}: ${esc(res.message)}${res.operation_id ? ` · операция <span class="mono">${esc(res.operation_id)}</span>` : ""}</div>${res.verified ? decodedHTML(res.verified) : ""}`;
    }
  }

  // --- Metadata correction -------------------------------------------------
  const metaKeys = [
    ["name", "Название", "text"], ["description", "Описание", "text"], ["notes", "Примечания", "text"], ["suffix", "Единица", "text"],
    ["min", "Мин. (логич.)", "number"], ["max", "Макс. (логич.)", "number"], ["step", "Шаг", "number"],
    ["raw_min", "Мин. raw", "number"], ["raw_max", "Макс. raw", "number"],
    ["allowed_values", "Допустимые значения (через запятую, пусто — убрать)", "list"],
    ["value_kind", "Вид значения", "kind"], ["confidence", "Достоверность (confidence)", "text"], ["source", "Источник", "text"]
  ];

  function metaFormHTML(d) {
    const cur = {name: d.name, description: d.description, notes: d.notes, suffix: d.unit, min: d.min, max: d.max, step: d.step, raw_min: d.raw_min, raw_max: d.raw_max,
      allowed_values: (d.allowed_values || []).join(", "), value_kind: d.kind, confidence: d.confidence, source: d.source};
    const fields = metaKeys.map(([k, label, type]) => {
      const v = cur[k] === undefined || cur[k] === null ? "" : cur[k];
      if (type === "kind") {
        const kinds = {uint: "целое без знака", int: "целое со знаком", scaled: "масштабированное число", boolean: "логическое (0/1)", enum: "перечисление", bitmask: "битовая маска", multi: "многорегистровое", time: "время HHMM"};
        return `<div class="field"><label>${label}</label><select data-meta="${k}">${Object.entries(kinds).map(([kk, vv]) => `<option value="${kk}" ${kk === v ? "selected" : ""}>${vv}</option>`).join("")}</select></div>`;
      }
      return `<div class="field"><label>${label}</label><input type="${type === "number" ? "number" : "text"}" step="any" data-meta="${k}" data-type="${type}" value="${esc(v)}" data-orig="${esc(v)}"></div>`;
    }).join("");
    return `<details class="plain" style="margin-top:14px"><summary>Исправить метаданные регистра в профиле</summary>
      <p class="small muted">Изменяются только описательные поля. Адрес, тип регистра, режим и маска записи, признак записи здесь не редактируются. Перед сохранением создаётся резервная копия файла профиля; откат — на вкладке «Профили».</p>
      <div class="grid-2">${fields}</div>
      <div class="field"><label>Биты (JSON-массив, пусто — не менять)</label><textarea data-meta="bits" rows="4" class="mono" placeholder='[{"bit":0,"name":"…","values":{"0":"выкл","1":"вкл"},"writable":true}]'></textarea></div>
      <div class="field"><label>Комментарий к изменению</label><input type="text" id="metaNote" placeholder="источник исправления"></div>
      <button type="button" class="btn btn-secondary" id="metaCheck">Проверить изменения</button></details>`;
  }

  function collectMetaChanges(d) {
    const changes = {};
    detailEl.querySelectorAll("[data-meta]").forEach(el => {
      const k = el.dataset.meta, type = el.dataset.type;
      if (k === "bits") {
        if (el.value.trim()) changes.bits = JSON.parse(el.value);
        return;
      }
      if (k === "value_kind") { if (el.value !== d.kind) changes.value_kind = el.value; return; }
      if (el.value === el.dataset.orig) return;
      if (type === "number") changes[k] = el.value === "" ? null : Number(el.value);
      else if (type === "list") changes[k] = el.value.trim() === "" ? null : el.value.split(/[,;\s]+/).filter(Boolean).map(Number);
      else changes[k] = el.value;
    });
    return changes;
  }

  function bindMetaForm(d) {
    $("metaCheck")?.addEventListener("click", async ev => {
      let changes;
      try { changes = collectMetaChanges(d); } catch (e) { App.toast("Биты: некорректный JSON — " + e.message, "error"); return; }
      if (!Object.keys(changes).length) { App.toast("Изменений нет", "info"); return; }
      const body = {model_key: modelKey(), code: d.code, changes, note: $("metaNote").value.trim(), dry_run: true};
      const check = await App.busy(ev.currentTarget, () => App.api("/api/registers/profile/patch", {method: "POST", json: body}));
      if (!check.ok) {
        App.toast(check.message, "error");
        return;
      }
      const html = `<div class="grid-2"><div><h4>Было</h4><pre class="small" style="white-space:pre-wrap;max-height:300px;overflow:auto">${esc(JSON.stringify(check.before, null, 2))}</pre></div>
        <div><h4>Станет</h4><pre class="small" style="white-space:pre-wrap;max-height:300px;overflow:auto">${esc(JSON.stringify(check.after, null, 2))}</pre></div></div>
        ${(check.issues || []).filter(i => i.severity !== "info").map(i => `<div class="notice notice-warn small">${esc(i.message)}</div>`).join("")}`;
      if (!await App.confirmDialog({title: "Сохранить изменения профиля?", html, confirmText: "Сохранить с резервной копией"})) return;
      body.dry_run = false;
      const res = await App.api("/api/registers/profile/patch", {method: "POST", json: body});
      App.toast(res.message, res.ok ? "success" : "error");
      if (res.ok) {
        await loadList();
        const fresh = items.find(x => x.code === d.code);
        if (fresh) { selected = fresh; renderDetail(); }
      }
    });
  }

  // --- Raw read by address -------------------------------------------------
  $("rawRead").addEventListener("click", async ev => {
    const out = $("rawResult");
    if (!inverterId()) { App.toast("Выберите инвертор", "warning"); return; }
    const addrText = $("rawAddr").value.trim();
    const address = addrText.toLowerCase().startsWith("0x") ? parseInt(addrText, 16) : parseInt(addrText, 10);
    if (!Number.isInteger(address)) { App.toast("Введите адрес", "warning"); return; }
    await App.busy(ev.currentTarget, async () => {
      const data = await App.api("/api/registers/read", {method: "POST", json: {inverter_id: inverterId(), address, count: Number($("rawCount").value) || 1, register_type: $("rawType").value}});
      if (!data.ok) { out.innerHTML = `<div class="notice notice-err small">${esc(data.message)}</div>`; return; }
      out.innerHTML = `<div class="notice notice-ok small">Прочитано ${App.fmtDate(data.read_at_utc)} · ${data.duration_ms} мс · ${esc(data.register_type)}</div>
        <div class="table-scroll"><table class="data-table"><thead><tr><th>Адрес</th><th>Raw</th><th>Hex</th><th>Двоичное</th><th>Со знаком</th><th>Расшифровка по профилю</th></tr></thead><tbody>
        ${data.values.map(v => `<tr><td class="mono">${v.address}</td><td class="mono">${v.raw}</td><td class="mono">${v.hex}</td><td class="mono small">${v.binary}</td><td class="mono">${v.signed}</td>
          <td class="small">${(v.fields || []).map(f => `<b class="mono">${esc(f.code)}</b>: ${esc(f.decoded.decoded_text)} ${esc(f.decoded.unit || "")}${(f.decoded.violations || []).length ? ' <span class="badge plain badge-err">вне профиля</span>' : ""}`).join("<br>") || '<span class="muted">не описан в профиле</span>'}</td></tr>`).join("")}
        </tbody></table></div>`;
    });
  });

  // --- Observations --------------------------------------------------------
  async function loadObservations() {
    const out = $("obsResult");
    const addr = $("obsAddr").value.trim();
    if (!addr) return;
    App.skeleton(out, 3);
    const p = new URLSearchParams({address: addr});
    if (inverterId()) p.set("inverter_id", inverterId());
    const data = await App.api("/api/registers/observations?" + p);
    if (!data.ok) { out.innerHTML = `<div class="notice notice-err small">${esc(data.message)}</div>`; return; }
    const a = data.analysis;
    const kindLabel = {read: "прочитано", write_verified: "записано и подтверждено", write_unverified: "записано, не подтверждено", write_rejected: "запись отклонена", accepted: "принимается (вручную)", rejected: "отклоняется (вручную)", note: "заметка"};
    out.innerHTML = `<dl class="kv small"><dt>Наблюдений</dt><dd>${a.observations}</dd>
      <dt>Диапазон по профилю</dt><dd>${esc(a.profile_range || "не задан")}</dd>
      <dt>Прочитанные значения</dt><dd class="mono">${esc((a.read_values || []).join(", ") || "—")}</dd>
      <dt>Принятые инвертором</dt><dd class="mono">${esc((a.accepted_values || []).join(", ") || "—")}</dd>
      <dt>Отклонённые</dt><dd class="mono">${esc((a.rejected_values || []).join(", ") || "—")}</dd>
      <dt>Без подтверждения</dt><dd class="mono">${esc((a.unverified_values || []).join(", ") || "—")}</dd>
      <dt>Достоверность</dt><dd><b>${esc(a.confidence_label)}</b></dd></dl>
      ${(a.contradictions || []).map(c => `<div class="notice notice-warn small">${esc(c)}</div>`).join("")}
      <div class="table-scroll" style="margin-top:10px"><table class="data-table"><thead><tr><th>Время</th><th>Инвертор</th><th>Код</th><th>Raw</th><th>Вид</th><th>Комментарий</th></tr></thead><tbody>
      ${data.items.map(o => `<tr><td class="nowrap">${App.fmtDate(o.ts_utc)}</td><td>${o.inverter_id || "—"}</td><td class="mono small">${esc(o.code || "—")}</td><td class="mono">${o.raw}</td><td>${esc(kindLabel[o.kind] || o.kind)}</td><td class="small">${esc(o.note)}</td></tr>`).join("") || '<tr><td colspan="6" class="empty">Наблюдений нет</td></tr>'}
      </tbody></table></div>`;
  }
  $("obsLoad").addEventListener("click", loadObservations);
  $("obsAdd").addEventListener("click", async () => {
    const addrText = $("obsAddr").value.trim();
    const address = addrText.toLowerCase().startsWith("0x") ? parseInt(addrText, 16) : parseInt(addrText, 10);
    if (!Number.isInteger(address) || $("obsRaw").value === "") { App.toast("Укажите адрес и значение", "warning"); return; }
    const res = await App.api("/api/registers/observations", {method: "POST", json: {inverter_id: inverterId(), address, raw: Number($("obsRaw").value), kind: $("obsKind").value, note: $("obsNote").value}});
    App.toast(res.message, res.ok ? "success" : "error");
    if (res.ok) loadObservations();
  });

  // --- Profiles ------------------------------------------------------------
  async function loadProfiles() {
    const rows = $("profilesRows");
    rows.innerHTML = '<tr><td colspan="7"><div class="skeleton skeleton-line"></div></td></tr>';
    const data = await App.api("/api/registers/profiles");
    if (!data.ok) { rows.innerHTML = `<tr><td colspan="7">${esc(data.message)}</td></tr>`; return; }
    rows.innerHTML = data.items.map(p => `<tr>
      <td>${esc(p.model_name)}<div class="small muted mono">${esc(p.model_key)}</div></td>
      <td class="small mono">${esc(p.file)}${p.customized ? ' <span class="badge plain badge-info">изменён пользователем</span>' : ""}</td>
      <td>${p.parameters}</td>
      <td>${p.structure_ok ? '<span class="badge badge-ok">в порядке</span>' : '<span class="badge badge-err">ошибки</span>'}</td>
      <td>${p.write_check_ok ? '<span class="badge badge-ok">пройдена</span>' : `<span class="badge badge-warn" title="${esc(p.write_check)}">не пройдена</span><div class="small muted">${esc(p.write_check)}</div>`}</td>
      <td>${p.validation_status === "verified" ? '<span class="badge badge-ok">проверен</span>' : '<span class="badge badge-muted">не проверен на оборудовании</span>'}${p.write_requires_confirmation ? ' <span class="badge plain badge-warn">нужно подтверждение записи</span>' : ""}</td>
      <td><a class="btn btn-secondary btn-sm" href="/api/registers/profile/export?model_key=${encodeURIComponent(p.model_key)}">Скачать</a></td></tr>`).join("");
    loadChanges();
  }

  async function loadChanges() {
    const rows = $("changesRows");
    const data = await App.api("/api/registers/profile/changes");
    if (!data.ok) { rows.innerHTML = `<tr><td colspan="7">${esc(data.message)}</td></tr>`; return; }
    const actionLabel = {patch: "исправление", rollback: "откат", install: "установка"};
    rows.innerHTML = data.items.map(c => `<tr><td class="nowrap">${App.fmtDate(c.ts_utc)}</td><td class="mono small">${esc(c.model_key)}</td>
      <td class="mono small">${esc(c.register_code || "—")}${c.register_address ? " · " + c.register_address : ""}</td><td>${esc(actionLabel[c.action] || c.action)}</td>
      <td class="small">${esc(c.note)}</td><td class="small mono">${esc(c.backup_file || "—")}</td>
      <td>${c.backup_file ? `<button type="button" class="btn btn-secondary btn-sm" data-rollback="${c.id}">Откатить</button>` : ""}</td></tr>`).join("") || '<tr><td colspan="7" class="empty">Изменений не было</td></tr>';
  }

  $("changesRows").addEventListener("click", async ev => {
    const b = ev.target.closest("button[data-rollback]");
    if (!b) return;
    if (!await App.confirmDialog({title: "Восстановить профиль из резервной копии?", html: "<p>Текущая версия файла профиля будет сохранена в новую резервную копию, затем файл будет заменён выбранной копией. Значения в инверторах не изменяются.</p>", confirmText: "Восстановить"})) return;
    const res = await App.busy(b, () => App.api("/api/registers/profile/rollback", {method: "POST", form: {change_id: b.dataset.rollback}}));
    App.toast(res.message, res.ok ? "success" : "error");
    if (res.ok) { loadProfiles(); loadList(); }
  });

  $("installForm").addEventListener("submit", async ev => {
    ev.preventDefault();
    const form = ev.currentTarget;
    const out = $("installResult");
    const send = async dry => {
      const fd = new FormData(form);
      if (dry) fd.set("dry_run", "1");
      const res = await fetch("/api/registers/profile/install", {method: "POST", body: fd});
      return res.json().catch(() => ({ok: false, message: "Некорректный ответ сервера"}));
    };
    const btn = form.querySelector("button[type=submit]");
    const check = await App.busy(btn, () => send(true));
    const issuesHTML = (check.issues || []).filter(i => i.severity !== "info").map(i => `<li>[${esc(i.severity)}] ${i.address ? "рег. " + i.address + ": " : ""}${esc(i.message)}</li>`).join("");
    if (!check.ok) { out.innerHTML = `<div class="notice notice-err small">${esc(check.message)}${issuesHTML ? `<ul>${issuesHTML}</ul>` : ""}</div>`; return; }
    const html = `<p>Параметров: ${check.parameters}. Структурная проверка пройдена.</p><p>Проверка безопасности записи: <b>${esc(check.write_check)}</b></p>${issuesHTML ? `<ul class="small">${issuesHTML}</ul>` : ""}<p class="small muted">Текущий файл будет сохранён в резервную копию.</p>`;
    if (!await App.confirmDialog({title: "Установить профиль?", html, confirmText: "Установить"})) return;
    const res = await App.busy(btn, () => send(false));
    out.innerHTML = `<div class="notice ${res.ok ? "notice-ok" : "notice-err"} small">${esc(res.message)}${res.backup ? " · резервная копия: " + esc(res.backup) : ""}</div>`;
    if (res.ok) { loadProfiles(); loadList(); }
  });

  loadList();
})();
