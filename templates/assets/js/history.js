(function () {
  const form = document.getElementById("historyFilters");
  const rowsEl = document.getElementById("historyRows");
  const countEl = document.getElementById("historyCount");
  const pagerEl = document.getElementById("historyPager");
  const esc = App.esc;
  let page = 1;
  let controller = null;

  const statusClass = {
    verified: "badge-ok", applied: "badge-info", info: "badge-muted", scheduled: "badge-muted", running: "badge-info",
    error: "badge-err", partial: "badge-warn", unverified: "badge-warn", skipped: "badge-muted", blocked: "badge-err", dry_run: "badge-purple"
  };

  function params() {
    const p = new URLSearchParams();
    new FormData(form).forEach((v, k) => { if (String(v).trim() !== "") p.set(k, v); });
    if (!document.getElementById("fTop").checked) p.delete("top");
    p.set("tz", Intl.DateTimeFormat().resolvedOptions().timeZone || "");
    return p;
  }

  const val = v => (v === null || v === undefined) ? "—" : esc(v);

  function regCell(e) {
    if (e.register_address === undefined || e.register_address === null) return "—";
    return `<span class="mono">${e.register_address}</span>${e.register_code ? `<div class="small muted">${esc(e.register_code)}</div>` : ""}`;
  }

  function rowHTML(e, child) {
    const expandable = !child;
    return `<tr class="${expandable ? "expandable" : ""}" data-op="${esc(e.operation_id)}" data-children="${e.child_count || 0}">
      <td class="nowrap">${child ? "&nbsp;&nbsp;↳ " : ""}${App.fmtDate(e.ts_utc)}</td>
      <td>${esc(e.operation_label)}${e.child_count ? ` <span class="badge plain badge-muted">${e.child_count}</span>` : ""}</td>
      <td><span class="badge ${statusClass[e.status] || "badge-muted"}">${esc(e.status_label)}</span></td>
      <td>${val(e.inverter_name)}${e.endpoint ? `<div class="small muted">${esc(e.endpoint)}</div>` : ""}</td>
      <td>${regCell(e)}</td>
      <td class="mono">${val(e.previous_value)}</td>
      <td class="mono">${val(e.requested_value)}</td>
      <td class="mono">${val(e.verified_value)}</td>
      <td class="small">${val(e.initiator)}</td>
      <td class="nowrap small">${e.duration_ms ? e.duration_ms + " мс" : "—"}</td>
      <td>${esc(e.message)}${e.error ? `<div class="small" style="color:var(--danger)">${esc(e.error)}</div>` : ""}</td>
    </tr>`;
  }

  function detailsHTML(e, children) {
    const d = e.details ? `<pre class="small" style="white-space:pre-wrap;max-height:240px;overflow:auto">${esc(JSON.stringify(e.details, null, 2))}</pre>` : "";
    const kids = children && children.length ? `<table class="data-table" style="margin-top:8px"><tbody>${children.map(c => rowHTML(c, true)).join("")}</tbody></table>` : "";
    return `<tr class="details-row"><td colspan="11">
      <dl class="kv"><dt>ID операции</dt><dd class="mono">${esc(e.operation_id)}</dd>
      ${e.parent_id ? `<dt>Родительская операция</dt><dd class="mono">${esc(e.parent_id)}</dd>` : ""}
      ${e.job_ref ? `<dt>Задание</dt><dd>${esc(e.job_ref)}</dd>` : ""}
      ${e.written_value !== undefined ? `<dt>Записано (raw)</dt><dd class="mono">${esc(e.written_value)}</dd>` : ""}
      ${e.attempt ? `<dt>Попыток</dt><dd>${e.attempt}</dd>` : ""}</dl>${d}${kids}</td></tr>`;
  }

  let lastItems = [];

  async function load() {
    controller?.abort();
    controller = new AbortController();
    const p = params();
    p.set("page", page);
    p.set("per_page", 50);
    rowsEl.innerHTML = '<tr><td colspan="11"><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-line"></div></td></tr>';
    const data = await App.api("/api/history?" + p.toString(), {signal: controller.signal});
    if (data.aborted) return;
    if (!data.ok) {
      rowsEl.innerHTML = `<tr><td colspan="11" class="empty">${esc(data.message || "Ошибка загрузки")}</td></tr>`;
      return;
    }
    lastItems = data.items;
    countEl.textContent = `Найдено записей: ${data.total}`;
    rowsEl.innerHTML = data.items.length ? data.items.map(e => rowHTML(e, false)).join("") : '<tr><td colspan="11" class="empty">Записей не найдено. История начинает заполняться после обновления приложения.</td></tr>';
    pagerEl.innerHTML = `<button class="btn btn-secondary btn-sm" ${data.page <= 1 ? "disabled" : ""} data-page="${data.page - 1}">← Назад</button>
      <span class="muted small">Страница ${data.page} из ${data.pages}</span>
      <button class="btn btn-secondary btn-sm" ${data.page >= data.pages ? "disabled" : ""} data-page="${data.page + 1}">Далее →</button>`;
  }

  rowsEl.addEventListener("click", async (ev) => {
    const tr = ev.target.closest("tr.expandable");
    if (!tr) return;
    const next = tr.nextElementSibling;
    if (next && next.classList.contains("details-row")) { next.remove(); return; }
    const entry = lastItems.find(e => e.operation_id === tr.dataset.op);
    if (!entry) return;
    let children = [];
    if (Number(tr.dataset.children) > 0) {
      const res = await App.api(`/api/history?operation_id=${encodeURIComponent(entry.operation_id)}&per_page=500`);
      children = (res.items || []).filter(c => c.parent_id === entry.operation_id).reverse();
    }
    tr.insertAdjacentHTML("afterend", detailsHTML(entry, children));
  });

  pagerEl.addEventListener("click", (ev) => {
    const b = ev.target.closest("button[data-page]");
    if (!b) return;
    page = Number(b.dataset.page);
    load();
  });

  const reload = App.debounce(() => { page = 1; load(); }, 350);
  form.addEventListener("input", reload);
  form.addEventListener("change", reload);
  form.addEventListener("submit", e => { e.preventDefault(); reload(); });
  form.addEventListener("reset", () => setTimeout(reload, 0));

  function exportAs(fmt) {
    const p = params();
    p.delete("top");
    p.set("format", fmt);
    App.download("/api/history/export?" + p.toString());
  }
  document.getElementById("exportCsv").addEventListener("click", () => exportAs("csv"));
  document.getElementById("exportXlsx").addEventListener("click", () => exportAs("xlsx"));

  load();
})();
