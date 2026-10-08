(function () {
  const grid = document.getElementById("statusGrid");
  const esc = App.esc;
  const stateClass = {online: "badge-ok", unstable: "badge-warn", offline: "badge-err", unknown: "badge-muted"};
  let snapshots = new Map();

  function fmtValue(m) {
    if (!m.supported) return `<span class="muted small">${esc(m.text || "не поддерживается")}</span>`;
    if (m.value === undefined || m.value === null) return `<span class="muted small">${esc(m.error || "нет данных")}</span>`;
    const v = Math.abs(m.value) >= 100 ? Math.round(m.value) : Math.round(m.value * 10) / 10;
    return `${v.toLocaleString("ru-RU")} <span class="muted small">${esc(m.unit || "")}</span>`;
  }

  function sourceBadge(s) {
    if (s.source === "live") return '<span class="badge badge-ok">Живые данные</span>';
    if (s.source === "cache") return `<span class="badge ${s.stale ? "badge-warn" : "badge-muted"}">${s.stale ? "Кэш, устарел" : "Кэш"}</span>`;
    return '<span class="badge badge-muted">Нет данных</span>';
  }

  function card(s) {
    const metrics = (s.metrics || []).map(m => `<div class="stat"><div class="stat-label">${esc(m.label)}</div><div class="stat-value">${fmtValue(m)}</div>${m.code ? `<div class="small muted mono">${esc(m.code)} · рег. ${m.address}</div>` : ""}</div>`).join("");
    const tou = s.use_timer_mask !== undefined && s.use_timer_mask !== null
      ? `TOU ${s.tou_enabled ? "включён" : "выключен"} · маска ${s.use_timer_mask} · дни: ${(s.tou_days || []).join(", ") || "нет"}`
      : "TOU: нет данных";
    return `<div class="card status-card" data-id="${s.inverter_id}">
      <div class="row between">
        <div><h2 class="mb-0">${esc(s.name)}</h2><div class="small muted">${esc(s.endpoint)} · ${esc(s.model_name || s.model_key)}${s.profile_status && s.profile_status !== "verified" ? ' <span class="badge plain badge-warn">профиль не проверен на оборудовании</span>' : ""}</div></div>
        <div class="row"><span class="badge ${stateClass[s.state] || "badge-muted"}">${esc(s.state_label || s.state)}</span>${sourceBadge(s)}</div>
      </div>
      <div class="small muted" style="margin:6px 0">
        Последний успешный опрос: <b>${s.last_success_utc ? App.fmtDate(s.last_success_utc) + " (" + App.ago(s.last_success_utc) + ")" : "не было"}</b>
        ${s.last_attempt_utc ? ` · последняя попытка: ${App.ago(s.last_attempt_utc)}` : ""}
        ${s.read_requests ? ` · ${s.read_requests} запрос(ов) Modbus, ${s.read_duration_ms} мс` : ""}
      </div>
      ${s.notice ? `<div class="notice notice-warn small">${esc(s.notice)}</div>` : ""}
      ${s.last_error && s.state !== "online" ? `<div class="notice notice-err small">Последняя ошибка: ${esc(s.last_error)}</div>` : ""}
      <div class="stat-grid">${metrics || '<div class="empty">Нет сохранённых показаний. Нажмите «Обновить».</div>'}</div>
      <dl class="kv small" style="margin-top:10px">
        <dt>Режим работы</dt><dd>${esc(s.work_mode || "—")}</dd>
        <dt>Регистр 146</dt><dd>${esc(tou)}</dd>
        <dt>Неисправности</dt><dd>${s.has_fault ? `<span class="badge badge-err">есть</span> ${esc(s.fault_text)}` : esc(s.fault_text || "—")}</dd>
        <dt>Расписание</dt><dd>${s.schedule_name ? `${esc(s.schedule_name)} · ${s.schedule_enabled ? "включено" : "выключено"}${s.schedule_template ? " · шаблон «" + esc(s.schedule_template) + "»" : ""}` : "не задано"}</dd>
        <dt>Последний запуск</dt><dd>${s.last_schedule_run ? App.fmtDate(s.last_schedule_run) + " · " + esc(s.last_schedule_status) : "—"}</dd>
        ${(s.unsupported || []).length ? `<dt>Не поддерживается профилем</dt><dd class="muted">${esc(s.unsupported.join(", "))}</dd>` : ""}
      </dl>
      <div class="button-row" style="margin-top:10px">
        <button type="button" class="btn btn-secondary btn-sm" data-refresh="${s.inverter_id}">Обновить</button>
        <a class="btn btn-secondary btn-sm" href="/history?inverter_id=${s.inverter_id}">История</a>
        <a class="btn btn-secondary btn-sm" href="/schedules?inverter_id=${s.inverter_id}">Расписание</a>
      </div>
    </div>`;
  }

  function render() {
    if (!snapshots.size) {
      grid.innerHTML = '<div class="card empty">Инверторы не добавлены. <a href="/#add-inverter">Добавить инвертор</a></div>';
      return;
    }
    grid.innerHTML = [...snapshots.values()].map(card).join("");
  }

  function merge(items) { (items || []).forEach(s => snapshots.set(s.inverter_id, s)); render(); }

  async function loadCached() {
    const data = await App.api("/api/inverters/status");
    if (!data.ok) { grid.innerHTML = `<div class="card empty">${esc(data.message)}</div>`; return; }
    snapshots = new Map();
    merge(data.items);
  }

  async function refresh(id, btn) {
    await App.busy(btn, async () => {
      const data = await App.api("/api/inverters/status/refresh", {method: "POST", form: {inverter_id: id || "all"}, timeoutMs: 120000});
      if (!data.ok) { App.toast(data.message || "Ошибка опроса", "error"); return; }
      merge(data.items);
      const failed = (data.items || []).filter(s => s.source !== "live");
      if (failed.length) App.toast(`Не удалось получить живые данные: ${failed.map(s => s.name).join(", ")}`, "warning");
      else App.toast("Данные обновлены", "success");
    });
  }

  grid.addEventListener("click", ev => {
    const b = ev.target.closest("button[data-refresh]");
    if (b) refresh(b.dataset.refresh, b);
  });
  document.getElementById("refreshAll").addEventListener("click", ev => refresh("all", ev.currentTarget));

  loadCached();
  // With background polling enabled the server updates the cache; the page
  // only re-reads that cache and never triggers Modbus traffic itself.
  if (grid.dataset.background === "1") {
    const every = Math.max(10, Number(grid.dataset.interval) || 60) * 1000;
    setInterval(() => { if (!document.hidden) loadCached(); }, every);
  }
  setInterval(() => { if (!document.hidden) render(); }, 30000);
})();
