(function () {
  const esc = App.esc;
  const tabs = document.getElementById("settingsTabs");
  const anchorTab = {"runtime-block": "schedules", "timezone-block": "general", "modbus-block": "registers", "system-block": "system", "inverter-models-block": "inverters"};

  function openTab(name) {
    if (!document.querySelector(`[data-panel="${name}"]`)) name = "general";
    tabs.querySelectorAll("button").forEach(b => b.classList.toggle("active", b.dataset.tab === name));
    document.querySelectorAll("[data-panel]").forEach(p => p.classList.toggle("active", p.dataset.panel === name));
    if (name === "system") loadSystemInfo();
    if (name === "registers") loadProfiles();
  }
  tabs.addEventListener("click", ev => {
    const b = ev.target.closest("button[data-tab]");
    if (!b) return;
    history.replaceState(null, "", "#" + b.dataset.tab);
    openTab(b.dataset.tab);
  });
  const hash = location.hash.replace("#", "");
  openTab(anchorTab[hash] || hash || "general");

  // --- Extended settings (app_kv) ------------------------------------------
  const optionLabels = {dark: "тёмная", light: "светлая", auto: "как в системе", advanced: "расширенный", simple: "упрощённый"};

  function fieldHTML(s) {
    const id = "kv_" + s.Key.replace(/\W/g, "_");
    let control;
    if (s.Kind === "bool") {
      control = `<label class="check"><input type="checkbox" id="${id}" data-kv="${esc(s.Key)}" ${s.value === "1" ? "checked" : ""}> ${esc(s.Label)}</label>`;
      return `<div class="field">${control}<small class="muted">${esc(s.Description)}</small></div>`;
    }
    if (s.Kind === "enum") {
      control = `<select id="${id}" data-kv="${esc(s.Key)}">${s.Options.map(o => `<option value="${esc(o)}" ${o === s.value ? "selected" : ""}>${esc(optionLabels[o] || o)}</option>`).join("")}</select>`;
    } else {
      control = `<input type="number" id="${id}" data-kv="${esc(s.Key)}" value="${esc(s.value)}" min="${s.Min}" ${s.Max ? `max="${s.Max}"` : ""}>`;
    }
    return `<div class="field"><label for="${id}">${esc(s.Label)}</label>${control}<small class="muted">${esc(s.Description)}${s.Kind === "int" ? ` (${s.Min}–${s.Max})` : ""}</small></div>`;
  }

  async function loadKV() {
    const data = await App.api("/api/app-settings");
    if (!data.ok) { App.toast(data.message || "Не удалось загрузить настройки", "error"); return; }
    const bySection = {};
    data.settings.forEach(s => { (bySection[s.Section] = bySection[s.Section] || []).push(s); });
    document.querySelectorAll("[data-kv-section]").forEach(box => {
      const list = bySection[box.dataset.kvSection] || [];
      box.innerHTML = list.map(fieldHTML).join("") + (list.length ? `<div class="button-row"><button type="button" class="btn btn-primary" data-kv-save>Сохранить</button></div>` : "");
    });
  }

  document.addEventListener("click", async ev => {
    const btn = ev.target.closest("[data-kv-save]");
    if (!btn) return;
    const box = btn.closest("[data-kv-section]");
    const form = new URLSearchParams();
    let gridPeakOn = null;
    box.querySelectorAll("[data-kv]").forEach(el => {
      const v = el.type === "checkbox" ? (el.checked ? "1" : "0") : el.value;
      form.set(el.dataset.kv, v);
      if (el.dataset.kv === "feature.grid_peak_shaving") gridPeakOn = v === "1";
    });
    if (gridPeakOn === true) {
      const before = await App.api("/api/app-settings");
      const was = (before.settings || []).find(s => s.Key === "feature.grid_peak_shaving");
      if (was && was.value !== "1" && !await App.confirmDialog({title: "Включить ограничение мощности?",
        html: "<p>На главной странице появится блок управления регистрами 178 (биты 4–5) и 191. Включение функции ничего не записывает в инвертор: запись выполняется только по вашей команде, с подтверждением и контрольным чтением.</p>", confirmText: "Включить"})) return;
    }
    const res = await App.busy(btn, () => App.api("/api/app-settings", {method: "POST", form}));
    App.toast(res.message || (res.ok ? "Сохранено" : "Ошибка"), res.ok ? "success" : "error");
    if (res.ok) {
      try { sessionStorage.removeItem("app.settings"); } catch {}
    }
  });

  // --- System info ---------------------------------------------------------
  let systemLoaded = false;
  async function loadSystemInfo() {
    if (systemLoaded) return;
    const box = document.getElementById("systemInfo");
    const d = await App.api("/api/system/info");
    if (!d.ok) { box.innerHTML = `<div class="notice notice-err small">${esc(d.message)}</div>`; return; }
    systemLoaded = true;
    const days = Math.floor(d.uptime_seconds / 86400), hours = Math.floor(d.uptime_seconds % 86400 / 3600), mins = Math.floor(d.uptime_seconds % 3600 / 60);
    const defaults = (d.defaults && d.defaults.files) || [];
    const actionLabel = {created: "создан из встроенной копии", unchanged: "совпадает со встроенной версией", upgraded: "обновлён (прежняя версия в data/backups)", customized: "изменён пользователем — не перезаписывается"};
    box.innerHTML = `<dl class="kv small">
      <dt>Версия</dt><dd class="mono">${esc(d.build)}</dd>
      <dt>Платформа</dt><dd>${esc(d.os)}/${esc(d.arch)}, ${esc(d.go)}</dd>
      <dt>Рабочий каталог</dt><dd class="mono">${esc(d.workdir)}</dd>
      <dt>Адрес</dt><dd class="mono">${esc(d.listen)}</dd>
      <dt>Работает</dt><dd>${days ? days + " дн " : ""}${hours} ч ${mins} мин (с ${App.fmtDate(d.started_utc)})</dd>
      <dt>База данных</dt><dd>${esc(d.db_size_human)} · инверторов ${d.counts.inverters}, расписаний ${d.counts.schedules}, шаблонов ${d.counts.templates} + упрощённых ${d.counts.simple_templates}, записей истории ${d.counts.history_rows}</dd>
      <dt>Функции</dt><dd>ограничение мощности: <b>${d.features.grid_peak_shaving ? "вкл" : "выкл"}</b> · запись при тестировании регистров: <b>${d.features.register_test_writes ? "вкл" : "выкл"}</b> · упрощённый режим: <b>${d.features.simple_scheduler ? "вкл" : "выкл"}</b></dd>
    </dl>
    ${defaults.length ? `<details class="plain small" style="margin-top:8px"><summary>Встроенные файлы по умолчанию (${defaults.length})</summary>
      <table class="data-table"><thead><tr><th>Файл</th><th>Состояние</th></tr></thead><tbody>${defaults.map(f => `<tr><td class="mono">${esc(f.path)}</td><td>${esc(actionLabel[f.action] || f.action)}${f.backup ? `<div class="muted mono">${esc(f.backup)}</div>` : ""}</td></tr>`).join("")}</tbody></table></details>` : ""}`;
  }

  // --- Profiles summary ----------------------------------------------------
  let profilesLoaded = false;
  async function loadProfiles() {
    if (profilesLoaded) return;
    const rows = document.getElementById("settingsProfiles");
    const data = await App.api("/api/registers/profiles");
    if (!data.ok) { rows.innerHTML = `<tr><td colspan="5">${esc(data.message)}</td></tr>`; return; }
    profilesLoaded = true;
    rows.innerHTML = data.items.map(p => `<tr><td>${esc(p.model_name)}<div class="small muted mono">${esc(p.file)}</div></td><td>${p.parameters}</td>
      <td>${p.structure_ok ? '<span class="badge badge-ok">в порядке</span>' : '<span class="badge badge-err">ошибки</span>'}</td>
      <td>${p.write_check_ok ? '<span class="badge badge-ok">пройдена</span>' : `<span class="badge badge-warn">не пройдена</span><div class="small muted">${esc(p.write_check)}</div>`}</td>
      <td>${p.customized ? "да" : "нет"}</td></tr>`).join("");
  }

  loadKV();
})();
