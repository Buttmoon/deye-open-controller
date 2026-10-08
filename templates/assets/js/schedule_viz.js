// ScheduleViz renders /api/schedules/timeline responses and simplified-mode
// previews as an hourly timeline or a month calendar.
window.ScheduleViz = (function () {
  const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"}[c]));
  const weekdays = ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"];
  const stateLabel = {configured: "", pending: "ожидает выполнения", executing: "выполняется сейчас", completed: "выполнено", failed: "ошибка выполнения", past: "время прошло, запуска не было"};
  let tip;

  function legend() {
    return `<div class="tl-legend">
      <span style="--c:var(--mode-self)">Собственное потребление</span>
      <span style="--c:var(--mode-grid)">Заряд от сети</span>
      <span style="--c:var(--mode-sell)">Продажа</span>
      <span style="--c:var(--mode-sellgrid)">Продажа + заряд от сети</span>
      <span style="--c:var(--mode-off)">Не задано</span>
      <span style="--c:#fde047">● 5-мин. переопределения</span>
    </div>`;
  }

  function hourTip(day, h) {
    if (!h.enabled) return `${day.date || "День " + day.day} ${String(h.hour).padStart(2, "0")}:00 — не задано${h.run_status ? "<br>запуск: " + esc(h.run_status) : ""}`;
    return `<b>${esc(day.date || "День " + day.day)} ${day.weekday ? "(" + esc(day.weekday) + ")" : ""} ${String(h.hour).padStart(2, "0")}:00–${String(h.hour + 1).padStart(2, "0")}:00</b><br>
      ${esc(h.mode_label)}<br>Мощность: ${h.power_w} Вт · SOC: ${h.soc}%<br>Экспорт в сеть: ${h.export_w} Вт · заряд от сети: ${h.grid_charge ? "да" : "нет"}
      ${h.custom_slots ? `<br>5-мин. переопределений: ${h.custom_slots}` : ""}${stateLabel[h.state] ? `<br><i>${stateLabel[h.state]}</i>` : ""}${h.run_status ? `<br>статус запуска: ${esc(h.run_status)}` : ""}`;
  }

  function bindTips(root) {
    if (!tip) { tip = document.createElement("div"); tip.className = "tl-tooltip"; tip.hidden = true; document.body.appendChild(tip); }
    root.addEventListener("mousemove", ev => {
      const cell = ev.target.closest("[data-tip]");
      if (!cell) { tip.hidden = true; return; }
      tip.innerHTML = cell.dataset.tip;
      tip.hidden = false;
      const x = Math.min(ev.clientX + 14, window.innerWidth - 300);
      tip.style.left = x + "px";
      tip.style.top = (ev.clientY + 14) + "px";
    });
    root.addEventListener("mouseleave", () => { tip.hidden = true; });
  }

  function timeline(container, data) {
    const head = `<div></div>${Array.from({length: 24}, (_, h) => `<div class="tl-head">${h % 3 === 0 ? h : ""}</div>`).join("")}`;
    const rows = data.days.map(d => {
      const weekend = d.weekday === "Сб" || d.weekday === "Вс";
      return `<div class="tl-day${weekend ? " weekend" : ""}">${d.date ? d.day + " " + esc(d.weekday) : "День " + d.day}</div>` +
        d.hours.map(h => `<div class="tl-cell ${h.enabled ? esc(h.category) : ""} state-${h.enabled ? esc(h.state) : "configured"}${h.custom_slots ? " custom" : ""}" data-tip="${esc(hourTip(d, h))}"></div>`).join("");
    }).join("");
    container.innerHTML = legend() + `<div class="table-scroll"><div class="tl-grid" style="min-width:620px">${head}${rows}</div></div>`;
    bindTips(container);
  }

  function calendar(container, data) {
    if (data.is_template || !data.days.length || !data.days[0].date) {
      container.innerHTML = '<p class="muted small">Шаблон не привязан к месяцу: дни показаны по номерам. Календарь доступен для расписания инвертора.</p>';
      timeline(container.appendChild(document.createElement("div")), data);
      return;
    }
    const first = new Date(data.days[0].date + "T00:00:00");
    const offset = (first.getDay() + 6) % 7;
    const today = new Date();
    const todayStr = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, "0")}-${String(today.getDate()).padStart(2, "0")}`;
    const cells = [];
    for (let i = 0; i < offset; i++) cells.push('<div class="cal-day empty"></div>');
    data.days.forEach(d => {
      const enabled = d.hours.filter(h => h.enabled);
      const failed = d.hours.filter(h => h.state === "failed").length;
      const done = d.hours.filter(h => h.state === "completed").length;
      const modes = {};
      enabled.forEach(h => { modes[h.mode_label] = (modes[h.mode_label] || 0) + 1; });
      const tipText = `<b>${esc(d.date)} (${esc(d.weekday)})</b><br>${enabled.length ? Object.entries(modes).map(([k, v]) => `${esc(k)}: ${v} ч`).join("<br>") : "часы не заданы"}${done ? `<br>выполнено запусков: ${done}` : ""}${failed ? `<br>ошибок: ${failed}` : ""}`;
      cells.push(`<div class="cal-day${d.date === todayStr ? " today" : ""}" data-tip="${esc(tipText)}"><div class="row between"><span class="cal-num">${d.day}</span><span class="small muted">${enabled.length ? enabled.length + " ч" : ""}${failed ? ' <span class="badge plain badge-err">!</span>' : ""}</span></div>
        <div class="cal-bar">${d.hours.map(h => `<i class="${h.enabled ? esc(h.category) : ""}"></i>`).join("")}</div></div>`);
    });
    container.innerHTML = legend() + `<div class="cal-grid">${weekdays.map(w => `<div class="cal-head">${w}</div>`).join("")}${cells.join("")}</div>`;
    bindTips(container);
  }

  // fromPreview converts a simplified-mode preview into the timeline format.
  function fromPreview(result, modeCategory) {
    return {
      month: result.month, is_template: false,
      days: result.days.map(d => {
        const hours = Array.from({length: 24}, (_, h) => ({hour: h, enabled: false, state: "configured", category: "off", mode_label: "не задано"}));
        (d.kept_hours || []).forEach(h => { hours[h] = {hour: h, enabled: true, state: "configured", category: "kept", mode_label: "сохранено из текущего расписания", power_w: "—", soc: "—", export_w: "—"}; });
        d.intervals.forEach(iv => {
          for (let h = iv.start; h < iv.end; h++) {
            hours[h] = {hour: h, enabled: true, state: "configured", category: modeCategory(iv), mode_label: `${iv.mode_label} — правило ${iv.rule}${iv.rule_name ? " «" + iv.rule_name + "»" : ""}`,
              power_w: iv.power_w, soc: iv.soc, export_w: "—", grid_charge: (iv.charge_mode & 1) === 1};
          }
        });
        return {day: d.day, date: d.date, weekday: d.weekday, hours};
      })
    };
  }

  return {timeline, calendar, fromPreview, legend};
})();
