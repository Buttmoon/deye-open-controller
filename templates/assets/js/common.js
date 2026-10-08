(function applyStoredTheme() {
  try {
    const stored = localStorage.getItem("ui.theme") || document.documentElement.dataset.defaultTheme || "dark";
    const theme = stored === "auto" ? (window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark") : stored;
    document.documentElement.dataset.theme = theme;
  } catch (e) { document.documentElement.dataset.theme = "dark"; }
  if (!document.querySelector('link[href^="/static/css/app.css"]')) {
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = "/static/css/app.css?v=20261008";
    document.head.appendChild(link);
  }
})();

function toast(message, type = "info", timeoutMs = 4500) {
  let stack = document.querySelector(".toast-stack");
  if (!stack) {
    stack = document.createElement("div");
    stack.className = "toast-stack";
    stack.setAttribute("role", "status");
    stack.setAttribute("aria-live", "polite");
    document.body.appendChild(stack);
  }
  const el = document.createElement("div");
  el.className = "toast " + type;
  el.textContent = message;
  stack.appendChild(el);
  setTimeout(() => el.remove(), type === "error" ? Math.max(timeoutMs, 8000) : timeoutMs);
}

function showPopup(message, type = "success") {
  const popup = document.getElementById("popup");
  if (!popup) { toast(message, type === "error" ? "error" : "success"); return; }
  popup.textContent = message;
  popup.className = "popup " + type + " show";
  clearTimeout(popup._timer);
  popup._timer = setTimeout(() => popup.classList.remove("show"), 3000);
}

async function postForm(url, params) {
  const response = await fetch(url, {
    method: "POST",
    headers: {"Content-Type": "application/x-www-form-urlencoded;charset=UTF-8"},
    body: params.toString()
  });
  return {response, data: await response.json()};
}

function toggleTabGroup(buttonSelector, panelSelector, activeId, activeBtn) {
  document.querySelectorAll(panelSelector).forEach(el => el.classList.remove("active"));
  document.querySelectorAll(buttonSelector).forEach(el => el.classList.remove("active"));
  document.getElementById(activeId)?.classList.add("active");
  activeBtn?.classList.add("active");
}

// App: small helpers shared by the new pages.
window.App = (function () {
  const inflight = new Map();

  // api performs a JSON request with a timeout. Identical concurrent GETs share
  // one network request. Pass {signal} to cancel from the caller.
  async function api(url, opts = {}) {
    const method = (opts.method || "GET").toUpperCase();
    const key = method === "GET" && !opts.signal ? url : null;
    if (key && inflight.has(key)) return inflight.get(key);
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), opts.timeoutMs || 30000);
    if (opts.signal) opts.signal.addEventListener("abort", () => controller.abort());
    const headers = Object.assign({}, opts.headers || {});
    let body = opts.body;
    if (opts.json !== undefined) { headers["Content-Type"] = "application/json"; body = JSON.stringify(opts.json); }
    if (opts.form !== undefined) {
      headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8";
      body = opts.form instanceof URLSearchParams ? opts.form.toString() : new URLSearchParams(opts.form).toString();
    }
    const run = (async () => {
      try {
        const res = await fetch(url, {method, headers, body, signal: controller.signal, cache: "no-store"});
        let data = {};
        const text = await res.text();
        try { data = text ? JSON.parse(text) : {}; } catch { data = {ok: false, message: text || res.statusText}; }
        if (data && typeof data === "object") { data._status = res.status; }
        if (!res.ok && data.ok === undefined) data.ok = false;
        return data;
      } catch (e) {
        if (e.name === "AbortError") return {ok: false, aborted: true, message: opts.signal?.aborted ? "Запрос отменён" : "Превышено время ожидания ответа сервера"};
        return {ok: false, message: "Нет связи с сервером: " + e.message};
      } finally {
        clearTimeout(timeout);
        if (key) inflight.delete(key);
      }
    })();
    if (key) inflight.set(key, run);
    return run;
  }

  async function busy(button, fn) {
    if (!button) return fn();
    button.classList.add("is-busy");
    button.disabled = true;
    try { return await fn(); } finally { button.classList.remove("is-busy"); button.disabled = false; }
  }

  function debounce(fn, ms = 300) {
    let t;
    return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
  }

  function esc(v) {
    return String(v ?? "").replace(/[&<>"']/g, c => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"}[c]));
  }

  function fmtDate(v) {
    if (!v) return "—";
    const d = new Date(v);
    if (isNaN(d)) return String(v);
    return d.toLocaleString("ru-RU", {year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit"});
  }

  function ago(v) {
    if (!v) return "никогда";
    const s = Math.round((Date.now() - new Date(v).getTime()) / 1000);
    if (s < 5) return "только что";
    if (s < 60) return s + " с назад";
    if (s < 3600) return Math.round(s / 60) + " мин назад";
    if (s < 86400) return Math.round(s / 3600) + " ч назад";
    return Math.round(s / 86400) + " дн назад";
  }

  function skeleton(el, lines = 4) {
    if (!el) return;
    el.innerHTML = Array.from({length: lines}, () => '<div class="skeleton skeleton-line"></div>').join("");
  }

  // confirmDialog shows a modal and resolves true/false. With requireText the
  // confirm button stays disabled until the operator types the exact text.
  function confirmDialog({title, html, confirmText = "Подтвердить", danger = false, requireText = ""}) {
    return new Promise(resolve => {
      const back = document.createElement("div");
      back.className = "dialog-backdrop";
      back.innerHTML = `<div class="dialog" role="dialog" aria-modal="true"><h3>${esc(title)}</h3><div>${html || ""}</div>
        ${requireText ? `<div class="field" style="margin-top:12px"><label>Для подтверждения введите <code>${esc(requireText)}</code></label><input type="text" class="dlg-req" autocomplete="off"></div>` : ""}
        <div class="button-row" style="justify-content:flex-end;margin-top:16px"><button type="button" class="btn btn-secondary dlg-cancel">Отмена</button>
        <button type="button" class="btn ${danger ? "btn-danger" : "btn-primary"} dlg-ok" ${requireText ? "disabled" : ""}>${esc(confirmText)}</button></div></div>`;
      document.body.appendChild(back);
      const ok = back.querySelector(".dlg-ok");
      const req = back.querySelector(".dlg-req");
      const done = v => { back.remove(); document.removeEventListener("keydown", onKey); resolve(v); };
      const onKey = e => { if (e.key === "Escape") done(false); };
      document.addEventListener("keydown", onKey);
      req?.addEventListener("input", () => { ok.disabled = req.value.trim() !== requireText; });
      back.querySelector(".dlg-cancel").addEventListener("click", () => done(false));
      back.addEventListener("click", e => { if (e.target === back) done(false); });
      ok.addEventListener("click", () => done(true));
      (req || ok).focus();
    });
  }

  function setTheme(theme) {
    try { localStorage.setItem("ui.theme", theme); } catch {}
    const t = theme === "auto" ? (window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark") : theme;
    document.documentElement.dataset.theme = t;
  }

  function download(url) {
    const a = document.createElement("a");
    a.href = url;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  return {api, busy, debounce, esc, fmtDate, ago, skeleton, confirmDialog, setTheme, toast, download};
})();

// App shell: navigation, breadcrumbs and theme switch. Runs on every page.
(function initAppShell() {
  const routeMap = [
    {href: "/", label: "Главная", icon: "⌂", group: "Основное"},
    {href: "/status", label: "Состояние инверторов", icon: "◉", group: "Основное"},
    {href: "/#inverters", label: "Инверторы", icon: "▣", group: "Основное"},
    {href: "/#add-inverter", label: "Добавить инвертор", icon: "＋", group: "Основное"},
    {href: "/schedules/simple", label: "Упрощённый режим", icon: "◔", group: "Расписание"},
    {href: "/schedules", label: "Расширенный режим", icon: "◷", group: "Расписание"},
    {href: "/schedules/templates", label: "Шаблоны расписаний", icon: "▦", group: "Расписание"},
    {href: "/templates/list", label: "Шаблоны (расширенные)", icon: "▤", group: "Расписание"},
    {href: "/templates/import", label: "Импорт XLSX", icon: "⇪", group: "Расписание"},
    {href: "/templates/apply-page", label: "Применить шаблон", icon: "✓", group: "Расписание"},
    {href: "/history", label: "История заданий и изменений", icon: "☰", group: "Журналы"},
    {href: "/tasks/logs", label: "Журнал задач", icon: "☷", group: "Журналы"},
    {href: "/inverter-logging", label: "Логи инвертора", icon: "▤", group: "Журналы"},
    {href: "/logs/monitor", label: "Логи приложения", icon: "≋", group: "Журналы"},
    {href: "/registers/test", label: "Тестирование регистров", icon: "⚗", group: "Инструменты"},
    {href: "/api/docs", label: "API документация", icon: "{}", group: "Инструменты"},
    {href: "/guide", label: "Руководство пользователя", icon: "?", group: "Инструменты"},
    {href: "/settings", label: "Настройки", icon: "⚙", group: "Настройки"},
    {href: "/tasks/settings", label: "Периодические задачи", icon: "⏱", group: "Настройки"},
    {href: "/integration", label: "Интеграция", icon: "⇄", group: "Настройки"},
    {href: "/log-delivery", label: "Уведомления и рассылка", icon: "✉", group: "Настройки"}
  ];

  const pathAliases = {
    "/": ["Главная"],
    "/status": ["Главная", "Состояние инверторов"],
    "/history": ["Главная", "История заданий и изменений"],
    "/registers/test": ["Главная", "Тестирование регистров"],
    "/schedules/simple": ["Главная", "Расписание", "Упрощённый режим"],
    "/schedules/templates": ["Главная", "Расписание", "Шаблоны расписаний"],
    "/settings": ["Главная", "Настройки"],
    "/tasks/settings": ["Главная", "Периодические задачи"],
    "/tasks/logs": ["Главная", "Журнал задач"],
    "/logs/monitor": ["Главная", "Логи приложения"],
    "/inverter-logging": ["Главная", "Логирование инвертора"],
    "/integration": ["Главная", "Интеграция"],
    "/log-delivery": ["Главная", "Уведомления и рассылка"],
    "/api/docs": ["Главная", "API документация"],
    "/templates/list": ["Главная", "Шаблоны"],
    "/templates/edit": ["Главная", "Шаблоны", "Редактор"],
    "/templates/apply-page": ["Главная", "Шаблоны", "Применение"],
    "/templates/import": ["Главная", "Шаблоны", "Импорт XLSX"],
    "/schedules": ["Главная", "Расписание", "Расширенный режим"]
  };

  function currentPath() {
    const path = window.location.pathname || "/";
    if (path.startsWith("/schedules/simple")) return "/schedules/simple";
    if (path.startsWith("/schedules")) return "/schedules";
    if (path.startsWith("/templates/edit")) return "/templates/edit";
    if (path.startsWith("/templates/import")) return "/templates/import";
    if (path.startsWith("/templates/apply")) return "/templates/apply-page";
    if (path.startsWith("/templates/list") || path === "/templates") return "/templates/list";
    return path;
  }

  function isActive(routeHref, path) {
    if (routeHref === "/") return path === "/";
    if (routeHref.startsWith("/#")) return false;
    return path === routeHref;
  }

  function makeMenu(path) {
    const nav = document.createElement("nav");
    nav.className = "app-menu";
    let lastGroup = "";
    routeMap.forEach(route => {
      if (route.group !== lastGroup) {
        lastGroup = route.group;
        const group = document.createElement("div");
        group.className = "app-menu-group";
        group.textContent = route.group;
        nav.appendChild(group);
      }
      const a = document.createElement("a");
      a.className = "app-menu-link" + (isActive(route.href, path) ? " active" : "");
      a.href = route.href;
      a.innerHTML = `<span class="app-menu-icon">${route.icon}</span><span>${route.label}</span>`;
      a.addEventListener("click", closeMenu);
      nav.appendChild(a);
    });
    return nav;
  }

  function openMenu() { document.body.classList.add("menu-open"); }
  function closeMenu() { document.body.classList.remove("menu-open"); }

  function addBreadcrumbs(path) {
    const container = document.querySelector(".container");
    if (!container || container.querySelector(".app-breadcrumbs")) return;
    const parts = pathAliases[path] || ["Главная", document.title || "Раздел"];
    const breadcrumbs = document.createElement("div");
    breadcrumbs.className = "app-breadcrumbs";
    parts.forEach((part, index) => {
      if (index > 0) {
        const sep = document.createElement("span");
        sep.className = "separator";
        sep.textContent = "›";
        breadcrumbs.appendChild(sep);
      }
      if (index === 0 && parts.length > 1) {
        const link = document.createElement("a");
        link.href = "/";
        link.textContent = part;
        breadcrumbs.appendChild(link);
      } else {
        const span = document.createElement("span");
        span.className = index === parts.length - 1 ? "current" : "";
        span.textContent = part;
        breadcrumbs.appendChild(span);
      }
    });
    container.insertBefore(breadcrumbs, container.firstChild);
  }

  function themeLabel() {
    return document.documentElement.dataset.theme === "light" ? "☾ Тёмная тема" : "☀ Светлая тема";
  }

  function init() {
    if (document.querySelector(".app-topbar")) return;
    document.body.classList.add("app-ui-v2");
    const path = currentPath();

    const topbar = document.createElement("header");
    topbar.className = "app-topbar";
    topbar.innerHTML = `
      <div class="app-topbar-left">
        <button type="button" class="burger-btn" aria-label="Открыть меню"><span></span><span></span><span></span></button>
        <div class="app-brand">
          <div class="app-brand-title">Deye Open Controller</div>
          <div class="app-brand-subtitle">Расписания, состояние, история и регистры инверторов</div>
        </div>
      </div>
      <div class="app-topbar-actions">
        <button type="button" class="theme-toggle" aria-label="Переключить тему">${themeLabel()}</button>
        <div class="app-topbar-status">Локальная панель</div>
      </div>
    `;

    const overlay = document.createElement("div");
    overlay.className = "app-sidebar-overlay";
    overlay.addEventListener("click", closeMenu);

    const sidebar = document.createElement("aside");
    sidebar.className = "app-sidebar";
    sidebar.innerHTML = `
      <div class="app-sidebar-head">
        <div>
          <div class="app-sidebar-title">Меню</div>
          <div class="app-sidebar-subtitle">Быстрый переход по разделам системы</div>
        </div>
        <button type="button" class="app-sidebar-close" aria-label="Закрыть меню">×</button>
      </div>
    `;
    sidebar.appendChild(makeMenu(path));

    document.body.insertBefore(topbar, document.body.firstChild);
    document.body.insertBefore(overlay, topbar.nextSibling);
    document.body.insertBefore(sidebar, overlay.nextSibling);

    topbar.querySelector(".burger-btn")?.addEventListener("click", openMenu);
    const themeBtn = topbar.querySelector(".theme-toggle");
    themeBtn?.addEventListener("click", () => {
      App.setTheme(document.documentElement.dataset.theme === "light" ? "dark" : "light");
      themeBtn.textContent = themeLabel();
    });
    sidebar.querySelector(".app-sidebar-close")?.addEventListener("click", closeMenu);
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeMenu();
    });
    addBreadcrumbs(path);
    applyServerSettings(sidebar, themeBtn);
  }

  // Server-side defaults (theme, editor visibility) are cached per browser
  // session so pages do not request them on every navigation.
  async function applyServerSettings(sidebar, themeBtn) {
    let settings = null;
    try {
      const cached = JSON.parse(sessionStorage.getItem("app.settings") || "null");
      if (cached && Date.now() - cached.t < 60000) settings = cached.v;
    } catch {}
    if (!settings) {
      const data = await App.api("/api/app-settings", {timeoutMs: 5000});
      if (!data.ok) return;
      settings = {};
      (data.settings || []).forEach(s => { settings[s.Key] = s.value; });
      try { sessionStorage.setItem("app.settings", JSON.stringify({t: Date.now(), v: settings})); } catch {}
    }
    let stored = null;
    try { stored = localStorage.getItem("ui.theme"); } catch {}
    if (!stored && settings["ui.default_theme"]) {
      const t = settings["ui.default_theme"] === "auto" ? (window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark") : settings["ui.default_theme"];
      document.documentElement.dataset.theme = t;
      if (themeBtn) themeBtn.textContent = themeLabel();
    }
    const simpleLink = sidebar.querySelector('a[href="/schedules/simple"]');
    if (simpleLink && settings["feature.simple_scheduler"] === "0" && currentPath() !== "/schedules/simple") simpleLink.remove();
    const advancedLink = sidebar.querySelector('a[href="/schedules"]');
    if (simpleLink && advancedLink && settings["schedule.default_editor"] === "advanced") advancedLink.after(simpleLink);
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
