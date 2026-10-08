(function () {
  const esc = App.esc;
  const $ = id => document.getElementById(id);
  let items = [];
  let current = null;
  let dirty = false;
  let meta = {hardware_slots: 6, load_limit_options: [], priority_options: []};

  const llOpts = () => meta.load_limit_options.length ? meta.load_limit_options : [
    {value: 0, label: "Selling first"}, {value: 1, label: "Zero export load"}, {value: 2, label: "Zero export CT"}
  ];
  const prOpts = () => meta.priority_options.length ? meta.priority_options : [
    {value: 0, label: "Нагрузка"}, {value: 1, label: "Батарея"}
  ];

  function timeOpts(sel) {
    let h = "";
    for (let i = 0; i < 24; i++) {
      const v = `${String(i).padStart(2, "0")}:00`;
      h += `<option value="${v}" ${v === sel ? "selected" : ""}>${v}</option>`;
    }
    h += `<option value="24:00" ${sel === "24:00" ? "selected" : ""}>24:00</option>`;
    return h;
  }

  function emptyRule() {
    return {name: "", start_time: "00:00", end_time: "06:00", grid_charge_enabled: false, load_limit_mode: 0, priority_load: 0, power_w: 0, battery_soc: 20, mode: "custom"};
  }

  function markDirty(v = true) {
    dirty = v;
    $("tplDirty").hidden = !dirty;
  }

  function readEditor() {
    return {
      id: current?.id || 0,
      name: $("tplName").value.trim(),
      description: $("tplDesc").value.trim(),
      tags: $("tplTags").value.split(",").map(s => s.trim()).filter(Boolean),
      model_keys: $("tplModels").value.split(",").map(s => s.trim()).filter(Boolean),
      rules: [...document.querySelectorAll("#tplIntervals tr")].map(tr => {
        const g = f => tr.querySelector(`[data-f="${f}"]`);
        return {
          name: g("name")?.value || "",
          start_time: g("start_time").value,
          end_time: g("end_time").value,
          grid_charge_enabled: g("grid_charge_enabled").checked,
          load_limit_mode: Number(g("load_limit_mode").value),
          priority_load: Number(g("priority_load").value),
          power_w: Number(g("power_w").value) || 0,
          battery_soc: Number(g("battery_soc").value) || 0,
          mode: "custom"
        };
      })
    };
  }

  function renderIntervals(rules) {
    $("tplIntervals").innerHTML = (rules || []).map((r, i) => `<tr data-i="${i}">
      <td>${i + 1}<input type="hidden" data-f="name" value="${esc(r.name || "")}"></td>
      <td><select data-f="start_time">${timeOpts(r.start_time || "00:00")}</select></td>
      <td><select data-f="end_time">${timeOpts(r.end_time || "06:00")}</select></td>
      <td><label class="switch"><input type="checkbox" data-f="grid_charge_enabled" ${r.grid_charge_enabled ? "checked" : ""}><span></span></label></td>
      <td><select data-f="load_limit_mode">${llOpts().map(o => `<option value="${o.value}" ${Number(r.load_limit_mode) === o.value ? "selected" : ""}>${esc(o.label)}</option>`).join("")}</select></td>
      <td><select data-f="priority_load">${prOpts().map(o => `<option value="${o.value}" ${Number(r.priority_load) === o.value ? "selected" : ""}>${esc(o.label)}</option>`).join("")}</select></td>
      <td><input type="number" data-f="power_w" min="0" step="10" value="${r.power_w || 0}" style="width:80px"></td>
      <td><input type="number" data-f="battery_soc" min="0" max="100" value="${r.battery_soc || 0}" style="width:60px"></td>
      <td><button type="button" class="btn btn-danger btn-xs" data-act="del">×</button></td>
    </tr>`).join("");
    renderTimeline(rules || []);
  }

  function renderTimeline(rules) {
    const el = $("tplTimeline");
    if (!rules.length) { el.innerHTML = ""; return; }
    const blocks = rules.map((r, i) => {
      const toMin = t => t === "24:00" ? 1440 : (() => { const [h, m] = t.split(":").map(Number); return h * 60 + (m || 0); })();
      let s = toMin(r.start_time || "0:0"), e = toMin(r.end_time || "0:0");
      const mk = (a, b) => `<div class="tl-block gc-${r.grid_charge_enabled ? "on" : "off"}" style="left:${(a / 1440) * 100}%;width:${Math.max(((b - a) / 1440) * 100, 0.8)}%"><span>${esc(r.start_time)}–${esc(r.end_time)}</span></div>`;
      if (e > s) return mk(s, e);
      return mk(s, 1440) + (e > 0 ? mk(0, e) : "");
    }).join("");
    el.innerHTML = `<div class="tl-track">${blocks}</div><div class="tl-axis"><span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></div>`;
  }

  function openTemplate(t) {
    if (dirty && !confirm("Есть несохранённые изменения. Продолжить без сохранения?")) return;
    current = t;
    $("tplEditorTitle").textContent = t ? `Редактор: ${t.name}` : "Новый шаблон";
    $("tplName").value = t?.name || "";
    $("tplDesc").value = t?.description || "";
    $("tplTags").value = (t?.tags || []).join(", ");
    $("tplModels").value = (t?.model_keys || []).join(", ");
    $("tplVersion").value = t ? `v${t.version || 1}` : "новая";
    const rules = (t?.rules || []).map(r => {
      const n = Object.assign(emptyRule(), r);
      if (!n.start_time && n.start_hour !== undefined) {
        n.start_time = `${String(n.start_hour).padStart(2, "0")}:00`;
        n.end_time = n.end_hour === 24 ? "24:00" : `${String(n.end_hour).padStart(2, "0")}:00`;
      }
      return n;
    });
    if (!rules.length) rules.push(emptyRule());
    renderIntervals(rules);
    markDirty(false);
    loadVersions(t?.id);
  }

  function renderList() {
    const q = ($("tplSearch").value || "").toLowerCase();
    const sort = $("tplSort").value;
    let list = items.slice();
    if (q) list = list.filter(t => `${t.name} ${t.description} ${(t.tags || []).join(" ")}`.toLowerCase().includes(q));
    list.sort((a, b) => {
      if (sort === "updated") return String(b.updated_at).localeCompare(String(a.updated_at));
      if (sort === "version") return (b.version || 0) - (a.version || 0);
      return String(a.name).localeCompare(String(b.name), "ru");
    });
    $("tplList").innerHTML = list.length ? list.map(t => `<div class="tpl-card ${current && current.id === t.id ? "active" : ""}" data-id="${t.id}">
      <div class="row between"><strong>${esc(t.name)}</strong><span class="badge">v${t.version || 1}</span></div>
      <div class="small muted">${esc(t.description || "без описания")}</div>
      <div class="small">${t.interval_count || t.rules?.length || 0} интерв. · слотов/день ${t.logical_day_slots || "—"}
        ${t.exceeds_hardware_slots ? '<span class="badge badge-warn">больше аппаратных</span>' : ""}</div>
      <div class="small muted">изменён ${esc(t.updated_at || "—")}${t.last_applied_utc ? " · применялся " + esc(t.last_applied_utc) : ""}</div>
      <div class="row" style="margin-top:8px">
        <button type="button" class="btn btn-secondary btn-xs" data-act="open">Открыть</button>
        <button type="button" class="btn btn-secondary btn-xs" data-act="dup">Дублировать</button>
        <a class="btn btn-secondary btn-xs" href="/api/simple-templates/export?id=${t.id}">Экспорт</a>
      </div>
    </div>`).join("") : '<div class="empty">Шаблонов нет</div>';
  }

  async function refresh() {
    const data = await App.api("/api/simple-templates");
    if (!data.ok) { App.toast(data.message || "Ошибка", "error"); return; }
    items = data.items || [];
    meta.hardware_slots = data.hardware_slots || 6;
    meta.load_limit_options = data.load_limit_options || [];
    meta.priority_options = data.priority_options || [];
    renderList();
  }

  async function loadVersions(id) {
    const box = $("tplVersions");
    if (!id) { box.innerHTML = "Сохраните шаблон, чтобы вести версии."; return; }
    const data = await App.api("/api/simple-templates/versions?id=" + id);
    if (!data.ok) { box.innerHTML = esc(data.message); return; }
    const list = data.items || [];
    box.innerHTML = list.length ? `<ul>${list.map(v => `<li>v${v.version} · ${esc(v.ts_utc)} · ${esc(v.summary || "")}
      <button type="button" class="btn btn-secondary btn-xs" data-restore="${v.version}">Восстановить</button></li>`).join("")}</ul>` : "История пуста";
    box.querySelectorAll("[data-restore]").forEach(b => b.addEventListener("click", async () => {
      if (!await App.confirmDialog({title: "Восстановить версию?", html: `<p>Будет создана новая версия на основе v${b.dataset.restore}. В инвертор ничего не пишется.</p>`, confirmText: "Восстановить"})) return;
      const res = await App.api("/api/simple-templates/versions?id=" + id, {method: "POST", json: {action: "restore", version: Number(b.dataset.restore)}});
      App.toast(res.message, res.ok ? "success" : "error");
      if (res.ok) { await refresh(); openTemplate(items.find(x => x.id === id)); }
    }));
  }

  $("tplList").addEventListener("click", async ev => {
    const card = ev.target.closest(".tpl-card");
    if (!card) return;
    const t = items.find(x => String(x.id) === card.dataset.id);
    const act = ev.target.closest("[data-act]")?.dataset.act || "open";
    if (act === "dup") {
      const res = await App.api("/api/simple-templates", {method: "POST", json: {action: "duplicate", id: t.id}});
      App.toast(res.message, res.ok ? "success" : "error");
      if (res.ok) { await refresh(); openTemplate(items.find(x => x.id === res.id)); }
      return;
    }
    openTemplate(t);
    renderList();
  });

  $("tplCreate").addEventListener("click", () => openTemplate(null));
  $("tplAdd").addEventListener("click", () => {
    const rules = readEditor().rules;
    rules.push(emptyRule());
    renderIntervals(rules);
    markDirty();
  });
  $("tplIntervals").addEventListener("input", () => { markDirty(); renderTimeline(readEditor().rules); });
  $("tplIntervals").addEventListener("change", () => { markDirty(); renderTimeline(readEditor().rules); });
  $("tplIntervals").addEventListener("click", ev => {
    if (ev.target.dataset.act !== "del") return;
    const tr = ev.target.closest("tr");
    tr.remove();
    markDirty();
    renderTimeline(readEditor().rules);
  });
  ["tplName", "tplDesc", "tplTags", "tplModels"].forEach(id => $(id).addEventListener("input", () => markDirty()));

  $("tplSave").addEventListener("click", async () => {
    const body = readEditor();
    if (!body.name) { App.toast("Укажите название", "warning"); return; }
    if (!body.rules.length) { App.toast("Добавьте интервал", "warning"); return; }
    const res = await App.api("/api/simple-templates", {method: "POST", json: Object.assign(body, {change_summary: "Сохранение в редакторе шаблонов"})});
    App.toast(res.message || (res.ok ? "Сохранено (без записи в инвертор)" : "Ошибка"), res.ok ? "success" : "error");
    if (res.ok) { markDirty(false); await refresh(); openTemplate(items.find(x => x.id === res.id)); }
  });

  $("tplDup").addEventListener("click", async () => {
    if (!current?.id) { App.toast("Сначала сохраните шаблон", "warning"); return; }
    const res = await App.api("/api/simple-templates", {method: "POST", json: {action: "duplicate", id: current.id}});
    App.toast(res.message, res.ok ? "success" : "error");
    if (res.ok) { await refresh(); openTemplate(items.find(x => x.id === res.id)); }
  });

  $("tplDel").addEventListener("click", async () => {
    if (!current?.id) { App.toast("Нечего удалять", "warning"); return; }
    if (!await App.confirmDialog({title: "Удалить шаблон?", html: `<p>«${esc(current.name)}» будет удалён. Расписания на инверторах не изменятся.</p>`, confirmText: "Удалить", danger: true})) return;
    const res = await App.api("/api/simple-templates", {method: "POST", json: {action: "delete", id: current.id}});
    App.toast(res.message, res.ok ? "success" : "error");
    if (res.ok) { current = null; openTemplate(null); refresh(); }
  });

  $("tplSearch").addEventListener("input", App.debounce(renderList, 200));
  $("tplSort").addEventListener("change", renderList);

  $("tplImport").addEventListener("change", async ev => {
    const file = ev.target.files[0];
    if (!file) return;
    const fd = new FormData();
    fd.set("file", file);
    if ($("tplImpOverwrite").checked) fd.set("overwrite", "1");
    const r = await fetch("/api/simple-templates/import", {method: "POST", body: fd});
    const res = await r.json().catch(() => ({ok: false, message: "Ошибка"}));
    ev.target.value = "";
    App.toast(res.ok ? `Импортировано: ${res.imported}` : (res.message || "Ошибка"), res.ok ? "success" : "error");
    if (res.ok) refresh();
  });

  async function previewApply() {
    if (!current?.id) { App.toast("Откройте сохранённый шаблон", "warning"); return; }
    const ids = [...document.querySelectorAll("#tplInverters input:checked")].map(x => Number(x.value));
    if (!ids.length) { App.toast("Выберите инверторы", "warning"); return; }
    const data = await App.api("/api/simple-templates/preview-apply", {method: "POST", json: {template_id: current.id, inverter_ids: ids, base_mode: "replace"}});
    if (!data.ok) { App.toast(data.message || "Ошибка", "error"); return; }
    $("tplApplyOut").innerHTML = (data.devices || []).map(d => {
      const c = d.compatibility || {};
      return `<div class="notice ${d.ok ? "notice-info" : "notice-err"} small">
        <b>${esc(d.name || "#" + d.inverter_id)}</b> — ${d.ok ? "можно применять" : "проблемы"}
        · совместимость: ${esc(c.status || "?")}
        ${(c.messages || []).map(m => `<div>${esc(m)}</div>`).join("")}
        ${d.exceeds_hardware ? "<div>Превышен лимит аппаратных слотов — расписание сохранится, запись пойдёт через планировщик.</div>" : ""}
        ${d.error ? `<div>${esc(d.error)}</div>` : ""}
        ${d.current ? `<div>Сейчас: ${d.current.summary?.enabled_hours || 0} ч.</div>` : "<div>Текущего расписания нет</div>"}
      </div>`;
    }).join("") + `<p class="small muted">${esc(data.note || "")}</p>`;
    return data;
  }

  $("tplPreviewApply").addEventListener("click", () => previewApply());
  $("tplApply").addEventListener("click", async () => {
    const data = await previewApply();
    if (!data) return;
    const bad = (data.devices || []).filter(d => !d.ok || (d.compatibility && d.compatibility.status === "incompatible"));
    if (bad.length && !await App.confirmDialog({title: "Есть проблемы совместимости", html: "<p>Часть устройств несовместима. Продолжить только для совместимых?</p>", confirmText: "Продолжить", danger: true})) return;
    if (!await App.confirmDialog({
      title: "Применить шаблон к инверторам?",
      html: `<p>Будет сохранено расписание в приложении (не путать с «Сохранить шаблон»). Затем можно запустить планировщик для записи регистров.</p>`,
      confirmText: "Сохранить расписание", danger: true
    })) return;
    const ids = (data.devices || []).filter(d => d.ok).map(d => d.inverter_id);
    for (const id of ids) {
      const rules = dirty ? readEditor().rules : current.rules;
      const req = {
        inverter_ids: [id], month: new Date().toISOString().slice(0, 7),
        rules, base_mode: "replace", name: current.name, confirm_destructive: true,
        template_id: current.id
      };
      const res = await App.api("/api/simple-schedule/apply", {method: "POST", json: req});
      App.toast(`Инвертор #${id}: ${res.message || (res.ok ? "OK" : "ошибка")}`, res.ok ? "success" : "error", 5000);
    }
    if (await App.confirmDialog({title: "Запустить планировщик сейчас?", html: "<p>Запись регистров TOU с контрольным чтением для активных заданий.</p>", confirmText: "Запустить"})) {
      const run = await App.api("/api/tasks/run-now", {method: "POST"});
      App.toast(run.message || (run.ok ? "Запущено" : "Ошибка"), run.ok ? "success" : "error");
    }
    refresh();
  });

  window.addEventListener("beforeunload", e => { if (dirty) { e.preventDefault(); e.returnValue = ""; } });

  refresh().then(() => openTemplate(null));
})();
