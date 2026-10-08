(function () {
  const $ = id => document.getElementById(id);
  const esc = App.esc;
  const box = $("vizBox");
  if (!box) return;
  const invSel = $("vizInverter"), monthInput = $("vizMonth");
  let view = "timeline";
  let controller = null;
  const now = new Date();
  monthInput.value = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;

  async function load() {
    if (!invSel.value) return;
    controller?.abort();
    controller = new AbortController();
    App.skeleton(box, 4);
    const data = await App.api(`/api/schedules/timeline?inverter_id=${invSel.value}&month=${monthInput.value}`, {signal: controller.signal});
    if (data.aborted) return;
    if (!data.ok) { box.innerHTML = `<div class="empty">${esc(data.message)}</div>`; return; }
    const s = data.summary || {};
    const head = `<div class="small muted" style="margin-bottom:6px">«${esc(data.title)}» · включённых часов: ${s.enabled_hours ?? "—"} · 5-мин. переопределений: ${s.custom_slots ?? 0} · маска Use Timer: ${data.use_timer_mask}</div>`;
    box.innerHTML = head + '<div class="viz-inner"></div>';
    const inner = box.querySelector(".viz-inner");
    if (view === "calendar") ScheduleViz.calendar(inner, data); else ScheduleViz.timeline(inner, data);
  }

  document.querySelectorAll("[data-viz]").forEach(b => b.addEventListener("click", () => { view = b.dataset.viz; load(); }));
  invSel.addEventListener("change", load);
  monthInput.addEventListener("change", load);

  function parseDays(text) {
    const out = new Set();
    text.split(",").map(x => x.trim()).filter(Boolean).forEach(part => {
      const m = part.match(/^(\d+)\s*-\s*(\d+)$/);
      if (m) { for (let d = Number(m[1]); d <= Number(m[2]); d++) out.add(d); }
      else if (/^\d+$/.test(part)) out.add(Number(part));
    });
    return [...out].filter(d => d >= 1 && d <= 31).sort((a, b) => a - b);
  }

  $("copyDaysBtn").addEventListener("click", async ev => {
    const from = Number($("copyFromDay").value);
    const to = parseDays($("copyToDays").value).filter(d => d !== from);
    if (!to.length) { App.toast("Укажите дни назначения", "warning"); return; }
    if (!await App.confirmDialog({title: "Скопировать день?", html: `<p>Настройки дня ${from} заменят настройки дней: ${to.join(", ")} (включая 5-минутные переопределения) в сохранённом расписании выбранного инвертора.</p><p class="small muted">Несохранённые правки в редакторе выше не учитываются — сохраните их заранее. После копирования страница перезагрузится.</p>`, confirmText: "Копировать", danger: true})) return;
    const res = await App.busy(ev.currentTarget, () => App.api("/api/schedules/copy", {method: "POST", form: {mode: "days", source_inverter_id: invSel.value, from_day: from, to_days: to.join(",")}}));
    App.toast(res.message, res.ok ? "success" : "error");
    if (res.ok) setTimeout(() => location.reload(), 800);
  });

  async function loadTargets() {
    const data = await App.api("/api/inverters");
    const el = $("copyTargets");
    if (!data.ok) { el.innerHTML = '<span class="muted small">Список инверторов недоступен</span>'; return; }
    el.innerHTML = (data.items || []).map(i => `<label class="chip"><input type="checkbox" value="${i.ID}"> ${esc(i.Name)} <span class="muted small">${esc(i.ModelName || i.ModelKey)}</span></label>`).join("") || '<span class="muted small">Нет других инверторов</span>';
  }
  $("copyInvertersBtn").addEventListener("click", async ev => {
    const ids = [...document.querySelectorAll("#copyTargets input:checked")].map(x => x.value).filter(id => id !== invSel.value);
    if (!ids.length) { App.toast("Выберите инверторы назначения", "warning"); return; }
    const names = [...document.querySelectorAll("#copyTargets input:checked")].map(x => x.parentElement.textContent.trim());
    if (!await App.confirmDialog({title: "Скопировать расписание на другие инверторы?", html: `<p>Сохранённое расписание инвертора «${esc(invSel.selectedOptions[0].textContent)}» заменит расписания:</p><ul>${names.map(n => `<li>${esc(n)}</li>`).join("")}</ul><p class="small muted">Расписание проверяется по моделям назначения. Если у них было включено автопродление упрощённого режима, при следующем пересчёте оно обнаружит изменение и отключится, не перезаписав скопированное расписание.</p>`, confirmText: "Заменить расписания", danger: true})) return;
    const res = await App.busy(ev.currentTarget, () => App.api("/api/schedules/copy", {method: "POST", form: {mode: "inverters", source_inverter_id: invSel.value, target_ids: ids.join(",")}}));
    App.toast(res.message, res.ok ? "success" : "error");
  });

  document.querySelector("#scheduleOverview details")?.addEventListener("toggle", function () { if (this.open) loadTargets(); }, {once: true});
  load();
})();
