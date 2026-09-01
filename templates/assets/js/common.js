function showPopup(message, type = "success") {
  const popup = document.getElementById("popup");
  if (!popup) return;
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


// UI v2: burger menu + breadcrumbs. Runs on every page where common.js is included.
(function initAppShell() {
  const routeMap = [
    {href:"/", label:"Главная", icon:"⌂", group:"Основное"},
    {href:"/#inverters", label:"Инверторы", icon:"▣", group:"Основное"},
    {href:"/#add-inverter", label:"Добавить инвертор", icon:"＋", group:"Основное"},
    {href:"/settings", label:"Общие настройки", icon:"⚙", group:"Настройки"},
    {href:"/tasks/settings", label:"Периодические задачи", icon:"⏱", group:"Настройки"},
    {href:"/inverter-logging", label:"Логирование инвертора", icon:"▣", group:"Настройки"},
    {href:"/integration", label:"Интеграция", icon:"⇄", group:"Настройки"},
    {href:"/schedules", label:"Расписание инвертора", icon:"◷", group:"Расписание"},
    {href:"/templates/list", label:"Шаблоны", icon:"▦", group:"Расписание"},
    {href:"/templates/edit", label:"Создать шаблон", icon:"＋", group:"Расписание"},
    {href:"/templates/import", label:"Импорт XLSX", icon:"⇪", group:"Расписание"},
    {href:"/templates/apply-page", label:"Применить шаблон", icon:"✓", group:"Расписание"},
    {href:"/inverter-logging", label:"Логи инвертора", icon:"▣", group:"Мониторинг"},
    {href:"/tasks/logs", label:"Журнал задач", icon:"☷", group:"Мониторинг"},
    {href:"/logs/monitor", label:"App logs", icon:"≋", group:"Мониторинг"},
    {href:"/api/docs", label:"API документация", icon:"{}", group:"Инструменты"}
  ];

  const pathAliases = {
    "/": ["Главная"],
    "/settings": ["Главная", "Настройки"],
    "/tasks/settings": ["Главная", "Периодические задачи"],
    "/tasks/logs": ["Главная", "Журнал задач"],
    "/logs/monitor": ["Главная", "App logs"],
    "/inverter-logging": ["Главная", "Логирование инвертора"],
    "/integration": ["Главная", "Интеграция"],
    "/api/docs": ["Главная", "API документация"],
    "/templates/list": ["Главная", "Шаблоны"],
    "/templates/edit": ["Главная", "Шаблоны", "Редактор"],
    "/templates/apply-page": ["Главная", "Шаблоны", "Применение"],
    "/templates/import": ["Главная", "Шаблоны", "Импорт XLSX"],
    "/schedules": ["Главная", "Расписание инвертора", "Редактор"]
  };

  function currentPath() {
    const path = window.location.pathname || "/";
    if (path.startsWith("/schedules")) return "/schedules";
    if (path.startsWith("/templates/edit")) return "/templates/edit";
    if (path.startsWith("/templates/apply")) return "/templates/apply-page";
    if (path.startsWith("/templates/import")) return "/templates/import";
    if (path.startsWith("/templates/apply-page")) return "/templates/apply-page";
    if (path.startsWith("/templates/list") || path === "/templates") return "/templates/list";
    return path;
  }

  function isActive(routeHref, path) {
    if (routeHref === "/") return path === "/";
    if (routeHref.startsWith("/#")) return path === "/";
    if (routeHref === "/templates/list") return path.startsWith("/templates") && !path.startsWith("/templates/edit") && !path.startsWith("/templates/import") && !path.startsWith("/templates/apply");
    return path === routeHref || path.startsWith(routeHref + "/");
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
          <div class="app-brand-title">Inverter Schedule</div>
          <div class="app-brand-subtitle">Расписания, шаблоны, интеграция и мониторинг</div>
        </div>
      </div>
      <div class="app-topbar-status">Локальная панель</div>
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
    sidebar.querySelector(".app-sidebar-close")?.addEventListener("click", closeMenu);
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeMenu();
    });
    addBreadcrumbs(path);
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
