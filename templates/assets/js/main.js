(function(){
  function openTab(event, tabId) {
    toggleTabGroup('.tab-content', '.tab-content', tabId, event.currentTarget);
  }
  window.openTab = function(event, tabId) {
    document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
    document.querySelectorAll('.tab-button').forEach(b => b.classList.remove('active'));
    document.getElementById(tabId)?.classList.add('active');
    event.currentTarget.classList.add('active');
  }

  const addInverterForm = document.getElementById('addInverterForm');
  const timezoneForm = document.getElementById('timezoneForm');
  function getSelectedIDs(){ return Array.from(document.querySelectorAll('.row-check:checked')).map(el => el.value); }
  function updateBulkButtons(){
    const disabled = getSelectedIDs().length === 0;
    ['bulkEditBtn','bulkEnableBtn','bulkDisableBtn','bulkTemplateApplyBtn'].forEach(id => { const el=document.getElementById(id); if(el) el.disabled = disabled; });
  }
  document.querySelectorAll('.row-check').forEach(ch => ch.addEventListener('change', updateBulkButtons));
  document.getElementById('selectVisible')?.addEventListener('change', function(){ const checked=this.checked; document.querySelectorAll('.row-check').forEach(ch=> ch.checked=checked); updateBulkButtons(); });
  document.getElementById('bulkEditBtn')?.addEventListener('click', ()=> { const ids=getSelectedIDs(); if(ids.length) window.location.href='/schedules?ids='+ids.join(','); });
  document.getElementById('bulkTemplateApplyBtn')?.addEventListener('click', ()=> { const ids=getSelectedIDs(); if(ids.length) window.location.href='/templates/apply-page?ids='+ids.join(','); });
  document.querySelectorAll('.single-edit-btn').forEach(btn=>btn.addEventListener('click', function(){ window.location.href='/schedules?ids='+this.dataset.id; }));
  document.getElementById('bulkEnableBtn')?.addEventListener('click', async ()=>{
    const params=new URLSearchParams(); params.append('ids', getSelectedIDs().join(',')); params.append('enabled','1');
    const {response,data}=await postForm('/schedules/toggle', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка','error');return;} showPopup(data.message||'Успешно'); setTimeout(()=>window.location.reload(),500);
  });
  document.getElementById('bulkDisableBtn')?.addEventListener('click', async ()=>{
    const params=new URLSearchParams(); params.append('ids', getSelectedIDs().join(',')); params.append('enabled','0');
    const {response,data}=await postForm('/schedules/toggle', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка','error');return;} showPopup(data.message||'Успешно'); setTimeout(()=>window.location.reload(),500);
  });
  addInverterForm?.addEventListener('submit', async function(e){
    e.preventDefault(); const params=new URLSearchParams(); params.append('name', document.getElementById('name').value); params.append('ip', document.getElementById('ip').value); params.append('port', document.getElementById('port').value); params.append('model_key', document.getElementById('model_key').value);
    try{ const {response,data}=await postForm('/inverters', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка добавления','error');return;} showPopup(data.message||'Добавлено'); setTimeout(()=>window.location.reload(),600);}catch{showPopup('Ошибка соединения с сервером','error');}
  });
  document.querySelectorAll('.inverter-settings-form').forEach(form=>form.addEventListener('submit', async function(e){
    e.preventDefault(); const params=new URLSearchParams(new FormData(form));
    try{ const {response,data}=await postForm('/inverters/settings', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка сохранения','error');return;} showPopup(data.message||'Сохранено'); setTimeout(()=>window.location.reload(),600);}catch{showPopup('Ошибка соединения с сервером','error');}
  }));
  document.querySelectorAll('.delete-inverter-form').forEach(form=>form.addEventListener('submit', async function(e){
    e.preventDefault(); const name=form.querySelector('input[name="name"]').value||'инвертор'; if(!confirm(`Удалить ${name}?`)) return; const params=new URLSearchParams(); params.append('id', form.querySelector('input[name="id"]').value); try{ const {response,data}=await postForm('/inverters/delete', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка удаления','error');return;} showPopup(data.message||'Удалено'); setTimeout(()=>window.location.reload(),500);}catch{showPopup('Ошибка соединения с сервером','error');}
  }));
  timezoneForm?.addEventListener('submit', async function(e){
    e.preventDefault(); const params=new URLSearchParams(); params.append('timezone', document.getElementById('timezone').value); try{ const {response,data}=await postForm('/settings/timezone', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка','error');return;} showPopup(data.message||'Сохранено'); }catch{showPopup('Ошибка соединения с сервером','error');}
  });
  updateBulkButtons();

  const checkInverterStateBtn = document.getElementById('checkInverterStateBtn');
  const activeCodeStatus = document.getElementById('activeCodeStatus');
  const activeCodeItems = Array.from(document.querySelectorAll('[data-active-code]'));

  function setActiveCodeStatus(text, isError){
    if (!activeCodeStatus) return;
    activeCodeStatus.textContent = text;
    activeCodeStatus.classList.toggle('status-error-text', !!isError);
  }

  function formatActiveCodeValue(value){
    if (value === undefined || value === null || value === '') return '—';
    if (typeof value === 'object') {
      try { return JSON.stringify(value); } catch { return String(value); }
    }
    return String(value);
  }

  function renderActiveCodeValues(record){
    activeCodeItems.forEach((item) => {
      const code = item.dataset.activeCode;
      const valueEl = item.querySelector('[data-active-code-value]');
      if (!valueEl) return;
      valueEl.textContent = formatActiveCodeValue(record ? record[code] : undefined);
      item.classList.toggle('has-value', !!record && Object.prototype.hasOwnProperty.call(record, code));
    });
  }

  async function loadLatestActiveCodeValues(){
    const response = await fetch('/api/inverter-logs/file?code=latest&format=json&limit=1', {cache:'no-store'});
    const data = await response.json();
    if (!response.ok || !data.ok) {
      throw new Error(data.message || 'Не удалось получить последний лог');
    }
    const raw = (data.lines || [])[0];
    if (!raw) {
      renderActiveCodeValues(null);
      setActiveCodeStatus('Последний лог пустой', true);
      return;
    }
    const record = JSON.parse(raw);
    renderActiveCodeValues(record);
    setActiveCodeStatus(`Данные обновлены: ${record.timestamp || record.generated_at_local || 'последняя запись'}`, false);
  }

  checkInverterStateBtn?.addEventListener('click', async function(){
    if (checkInverterStateBtn.disabled) return;
    checkInverterStateBtn.disabled = true;
    checkInverterStateBtn.classList.add('loading');
    const label = checkInverterStateBtn.querySelector('.btn-label');
    const oldLabel = label ? label.textContent : '';
    if (label) label.textContent = 'Проверка выполняется...';
    setActiveCodeStatus('Подключение к инвертору и чтение активных кодов...', false);
    try {
      const {response, data} = await postForm('/api/inverter-logs/run-now', new URLSearchParams());
      if (!response.ok || !data.ok) {
        throw new Error(data.message || 'Проверка завершилась ошибкой');
      }
      await loadLatestActiveCodeValues();
      showPopup(data.message || 'Состояние инвертора проверено');
    } catch (e) {
      setActiveCodeStatus('Ошибка проверки: ' + e.message, true);
      showPopup('Ошибка проверки: ' + e.message, 'error');
    } finally {
      checkInverterStateBtn.disabled = false;
      checkInverterStateBtn.classList.remove('loading');
      if (label) label.textContent = oldLabel || 'Проверить состояние инвертора';
    }
  });



  const inverterScreenDetails = document.getElementById('inverterScreenDetails');
  const refreshInverterScreenBtn = document.getElementById('refreshInverterScreenBtn');
  const inverterScreenStatus = document.getElementById('inverterScreenStatus');
  const inverterScreenRows = document.getElementById('inverterScreenRows');
  const inverterScreenTarget = document.getElementById('inverterScreenTarget');
  const inverterScreenToggleLabel = document.getElementById('inverterScreenToggleLabel');
  const inverterScreenSettingsRows = document.getElementById('inverterScreenSettingsRows');
  const powerLimitControl = document.getElementById('powerLimitControl');
  const powerLimitCurrentValue = document.getElementById('powerLimitCurrentValue');
  const powerLimitTarget = document.getElementById('powerLimitTarget');
  const powerLimitState = document.getElementById('powerLimitState');
  const powerLimitToggleBtn = document.getElementById('powerLimitToggleBtn');
  const powerLimitForm = document.getElementById('powerLimitForm');
  const powerLimitInput = document.getElementById('powerLimitInput');
  const powerLimitApplyBtn = document.getElementById('powerLimitApplyBtn');
  const powerLimitRange = document.getElementById('powerLimitRange');
  const powerLimitStatus = document.getElementById('powerLimitStatus');
  const powerLimitRefreshBtn = document.getElementById('powerLimitRefreshBtn');
  const screenTabs = Array.from(document.querySelectorAll('[data-screen-page]'));
  const screenPanels = Array.from(document.querySelectorAll('[data-screen-page-panel]'));
  let inverterScreenLoaded = false;
  let latestInverterScreenData = null;
  let powerLimitBusy = false;

  function screenMark(value){
    const enabled = value === true || value === 1 || value === '1' || value === 'true';
    return enabled
      ? '<span class="lcd-state lcd-state-on">Вкл</span>'
      : '<span class="lcd-state lcd-state-off">Выкл</span>';
  }

  function screenValue(value, suffix){
    if (value === undefined || value === null || value === '') return '—';
    return `${value}${suffix || ''}`;
  }

  function renderModeChoice(active, leftLabel, rightLabel){
    const rightActive = !!active;
    return `
      <div class="mode-choice">
        <span class="mode-pill ${!rightActive ? 'active' : ''}">${leftLabel}</span>
        <span class="mode-pill ${rightActive ? 'active' : ''}">${rightLabel}</span>
      </div>
    `;
  }

  function renderDayChips(days){
    const active = new Set(Array.isArray(days) ? days : []);
    return ['Пн','Вт','Ср','Чт','Пт','Сб','Вс'].map((day) =>
      `<span class="day-chip ${active.has(day) ? 'active' : ''}">${day}</span>`
    ).join('');
  }

  function profileAddress(addresses, code){
    const values = addresses && Array.isArray(addresses[code]) ? addresses[code] : [];
    return values.length ? values.join(', ') : '—';
  }

  function renderInverterScreenSettings(settings, addresses){
    if (!inverterScreenSettingsRows) return;
    const st = settings || {};
    inverterScreenSettingsRows.innerHTML = `
      <div class="screen-setting-card wide">
        <div class="setting-head"><span>USE TIMER</span>${screenMark(st.use_timer_enabled)}</div>
        <div class="setting-value">Регистр ${profileAddress(addresses, 'use_timer')}: <strong>${screenValue(st.use_timer_raw)}</strong></div>
        <div class="day-chip-row">${renderDayChips(st.use_timer_days)}</div>
      </div>
      <div class="screen-setting-card">
        <div class="setting-head"><span>Work mode</span>${screenMark((st.work_mode_raw ?? 0) !== 0)}</div>
        <div class="setting-value"><strong>${st.work_mode_label || '—'}</strong></div>
        <div class="setting-hint">Регистр ${profileAddress(addresses, 'inverter_work_mode')}: ${screenValue(st.work_mode_raw)}</div>
      </div>
      <div class="screen-setting-card">
        <div class="setting-head"><span>Grid Peak Shaving</span>${screenMark(st.grid_peak_enabled)}</div>
        <div class="setting-value"><strong>${screenValue(st.grid_peak_value, ' W')}</strong></div>
        <div class="setting-hint">Мощность: регистр ${profileAddress(addresses, 'grid_peak_shaving_power')}; вкл/выкл: регистр ${profileAddress(addresses, 'grid_peak_shaving_enabled')} bits4-5 (raw ${screenValue(st.grid_peak_enable_raw)})</div>
      </div>
      <div class="screen-setting-card wide">
        <div class="setting-head"><span>Energy pattern</span>${screenMark(['Battery First', 'Load First'].includes(st.energy_pattern))}</div>
        <div class="setting-value"><strong>${st.energy_pattern || 'Unknown'}</strong></div>
        <div class="setting-hint">Регистр ${profileAddress(addresses, 'priority_load')} Priority load: ${screenValue(st.priority_load_raw)}</div>
      </div>
    `;
  }

  function renderInverterScreen(data){
    if (inverterScreenTarget) inverterScreenTarget.textContent = `${data.target || 'Инвертор'} · ${data.model_name || ''} · ${data.updated_at || ''}`;
    if (!inverterScreenRows) return;
    const points = Array.isArray(data.points) ? data.points : [];
    if (!points.length) {
      inverterScreenRows.innerHTML = '<tr><td colspan="7" class="muted">Нет данных для отображения.</td></tr>';
      return;
    }
    inverterScreenRows.innerHTML = points.map((p) => `
      <tr>
        <td>${screenMark(p.grid)}</td>
        <td>${screenMark(p.gen)}</td>
        <td>${screenMark(p.sell)}</td>
        <td>${p.time_start || '—'}</td>
        <td>${p.time_end || '—'}</td>
        <td>${p.power ?? '—'}</td>
        <td>${p.batt ?? '—'}</td>
      </tr>
    `).join('');
  }

  function formatPower(value){
    const watts = Number(value);
    if (!Number.isFinite(watts)) return '—';
    const kw = watts / 1000;
    const kwText = kw.toLocaleString('ru-RU', {maximumFractionDigits: 2});
    return `${watts.toLocaleString('ru-RU')} Вт · ${kwText} кВт`;
  }

  function setPowerLimitBusy(busy){
    powerLimitBusy = !!busy;
    if (powerLimitToggleBtn) powerLimitToggleBtn.disabled = busy || !latestInverterScreenData;
    if (powerLimitInput) powerLimitInput.disabled = busy || !latestInverterScreenData;
    if (powerLimitApplyBtn) powerLimitApplyBtn.disabled = busy || !latestInverterScreenData;
    if (powerLimitRefreshBtn) powerLimitRefreshBtn.disabled = busy;
  }

  function renderPowerLimit(data){
    if (!powerLimitControl) return;
    latestInverterScreenData = data;
    const st = data.settings || {};
    const enabled = !!st.grid_peak_enabled;
    const maxW = Number(st.grid_peak_max_w) || 0;
    if (powerLimitCurrentValue) powerLimitCurrentValue.textContent = formatPower(st.grid_peak_value);
    if (powerLimitTarget) powerLimitTarget.textContent = data.target || 'Инвертор';
    if (powerLimitState) {
      powerLimitState.textContent = enabled ? 'Включено' : 'Выключено';
      powerLimitState.classList.toggle('lcd-state-on', enabled);
      powerLimitState.classList.toggle('lcd-state-off', !enabled);
    }
    if (powerLimitToggleBtn) powerLimitToggleBtn.textContent = enabled ? 'Выключить' : 'Включить';
    if (powerLimitInput) {
      powerLimitInput.max = maxW > 0 ? String(maxW) : '';
      powerLimitInput.value = String(st.grid_peak_value ?? '');
    }
    if (powerLimitRange) {
      const rangeText = maxW > 0 ? `0–${maxW.toLocaleString('ru-RU')} Вт` : 'диапазон выбранной модели';
      powerLimitRange.textContent = `Допустимо: ${rangeText}, шаг 10 Вт. Значение хранится в регистре ${profileAddress(data.register_addresses || {}, 'grid_peak_shaving_power')}.`;
    }
    if (powerLimitStatus) {
      powerLimitStatus.textContent = `${data.message || 'Данные обновлены'} · ${data.updated_at || ''}`;
      powerLimitStatus.classList.remove('status-error-text');
    }
    setPowerLimitBusy(false);
  }

  function powerLimitWriteParams(code, value){
    const params = new URLSearchParams();
    params.append('code', code);
    params.append('value', String(value));
    if (latestInverterScreenData?.inverter_id) params.append('inverter_id', String(latestInverterScreenData.inverter_id));
    if (latestInverterScreenData?.model_key) params.append('model_key', latestInverterScreenData.model_key);
    return params;
  }

  async function loadInverterScreen(refreshFromInverter){
    if (!inverterScreenRows && !powerLimitControl) return;
    if (inverterScreenStatus) inverterScreenStatus.textContent = 'Читаю регистры расписания из профиля выбранного инвертора...';
    if (refreshInverterScreenBtn) refreshInverterScreenBtn.disabled = true;
    if (powerLimitStatus) {
      powerLimitStatus.textContent = refreshFromInverter
        ? 'Читаю текущее состояние и ограничение с инвертора…'
        : 'Читаю первое сохранённое значение из файла лога…';
      powerLimitStatus.classList.remove('status-error-text');
    }
    setPowerLimitBusy(true);
    try {
      const url = refreshFromInverter ? '/api/inverter/screen?refresh=1' : '/api/inverter/screen';
      const response = await fetch(url, {cache:'no-store'});
      const data = await response.json();
      if (!response.ok || !data.ok) throw new Error(data.message || 'Не удалось прочитать экран инвертора');
      renderInverterScreen(data);
      renderInverterScreenSettings(data.settings, data.register_addresses || {});
      renderPowerLimit(data);
      if (inverterScreenStatus) inverterScreenStatus.textContent = data.message || 'Данные обновлены';
      inverterScreenLoaded = true;
    } catch (e) {
      latestInverterScreenData = null;
      if (inverterScreenStatus) inverterScreenStatus.textContent = 'Ошибка: ' + e.message;
      if (inverterScreenRows) inverterScreenRows.innerHTML = '<tr><td colspan="7" class="status-error-text">' + e.message + '</td></tr>';
      if (inverterScreenSettingsRows) inverterScreenSettingsRows.innerHTML = '<div class="screen-setting-empty status-error-text">' + e.message + '</div>';
      if (powerLimitStatus) {
        powerLimitStatus.textContent = 'Ошибка чтения: ' + e.message;
        powerLimitStatus.classList.add('status-error-text');
      }
      showPopup('Ошибка визуализации: ' + e.message, 'error');
    } finally {
      if (refreshInverterScreenBtn) refreshInverterScreenBtn.disabled = false;
      setPowerLimitBusy(false);
    }
  }

  function setScreenPage(page){
    screenTabs.forEach((tab) => tab.classList.toggle('active', tab.dataset.screenPage === page));
    screenPanels.forEach((panel) => panel.classList.toggle('active', panel.dataset.screenPagePanel === page));
  }

  screenTabs.forEach((tab) => tab.addEventListener('click', () => setScreenPage(tab.dataset.screenPage)));

  inverterScreenDetails?.addEventListener('toggle', function(){
    if (inverterScreenToggleLabel) inverterScreenToggleLabel.textContent = inverterScreenDetails.open ? 'Закрыть' : 'Открыть';
    if (inverterScreenDetails.open && !inverterScreenLoaded) loadInverterScreen(false);
  });
  refreshInverterScreenBtn?.addEventListener('click', () => loadInverterScreen(true));
  powerLimitRefreshBtn?.addEventListener('click', () => loadInverterScreen(true));

  powerLimitToggleBtn?.addEventListener('click', async function(){
    if (powerLimitBusy || !latestInverterScreenData) return;
    const enabled = !!latestInverterScreenData.settings?.grid_peak_enabled;
    const desired = enabled ? 0 : 1;
    if (!await App.confirmDialog({title: `${enabled ? 'Выключить' : 'Включить'} ограничение мощности?`, html: '<p>Будут изменены только биты включения в регистре 178 (чтение-изменение-запись), затем выполнено контрольное чтение. Операция записывается в историю.</p>', confirmText: enabled ? 'Выключить' : 'Включить', danger: true})) return;
    setPowerLimitBusy(true);
    if (powerLimitStatus) powerLimitStatus.textContent = `${enabled ? 'Выключаю' : 'Включаю'} Grid Peak Shaving…`;
    try {
      const {response, data} = await postForm('/api/modbus/write', powerLimitWriteParams('grid_peak_shaving_enabled', desired));
      if (!response.ok || !data.ok) throw new Error(data.message || 'Не удалось изменить состояние');
      latestInverterScreenData.settings.grid_peak_enabled = desired === 1;
      const currentRaw = Number(latestInverterScreenData.settings.grid_peak_enable_raw) || 0;
      latestInverterScreenData.settings.grid_peak_enable_raw = (currentRaw & ~0x30) | (desired === 1 ? 0x30 : 0x10);
      latestInverterScreenData.message = data.message || 'Состояние изменено и сохранено';
      renderPowerLimit(latestInverterScreenData);
      showPopup(data.message || 'Состояние изменено');
    } catch (e) {
      if (powerLimitStatus) {
        powerLimitStatus.textContent = 'Ошибка записи: ' + e.message;
        powerLimitStatus.classList.add('status-error-text');
      }
      showPopup('Ошибка записи: ' + e.message, 'error');
      setPowerLimitBusy(false);
    }
  });

  powerLimitForm?.addEventListener('submit', async function(event){
    event.preventDefault();
    if (powerLimitBusy || !latestInverterScreenData || !powerLimitInput) return;
    const value = Number(powerLimitInput.value);
    const maxW = Number(latestInverterScreenData.settings?.grid_peak_max_w) || 0;
    if (!Number.isInteger(value) || value < 0 || value % 10 !== 0 || (maxW > 0 && value > maxW)) {
      showPopup(`Укажи целое значение с шагом 10 Вт${maxW > 0 ? ` в диапазоне 0–${maxW} Вт` : ''}`, 'error');
      return;
    }
    const prevW = latestInverterScreenData.settings?.grid_peak_value;
    if (!await App.confirmDialog({title: 'Изменить ограничение мощности?', html: `<p>Сейчас: <b>${prevW ?? '—'} Вт</b> → будет: <b>${value.toLocaleString('ru-RU')} Вт</b>${maxW > 0 ? ` (максимум для модели ${maxW.toLocaleString('ru-RU')} Вт)` : ''}.</p><p class="small muted">После записи выполняется контрольное чтение регистра 191.</p>`, confirmText: 'Записать', danger: true})) return;
    setPowerLimitBusy(true);
    if (powerLimitStatus) powerLimitStatus.textContent = `Записываю ограничение ${value.toLocaleString('ru-RU')} Вт…`;
    try {
      const {response, data} = await postForm('/api/modbus/write', powerLimitWriteParams('grid_peak_shaving_power', value));
      if (!response.ok || !data.ok) throw new Error(data.message || 'Не удалось изменить ограничение');
      latestInverterScreenData.settings.grid_peak_value = value;
      latestInverterScreenData.message = data.message || 'Ограничение изменено и сохранено';
      renderPowerLimit(latestInverterScreenData);
      showPopup(data.message || 'Ограничение изменено');
    } catch (e) {
      if (powerLimitStatus) {
        powerLimitStatus.textContent = 'Ошибка записи: ' + e.message;
        powerLimitStatus.classList.add('status-error-text');
      }
      showPopup('Ошибка записи: ' + e.message, 'error');
      setPowerLimitBusy(false);
    }
  });

  if (powerLimitControl) loadInverterScreen(false);

document.getElementById('runSchedulerNowBtn')?.addEventListener('click', async function(){
  try{ const {response,data}=await postForm('/tasks/run-now', new URLSearchParams()); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка запуска','error');return;} showPopup(data.message||'Задача запущена'); }catch{showPopup('Ошибка соединения с сервером','error');}
});
document.getElementById('toggleSchedulerBtn')?.addEventListener('click', async function(){
  const enabled = this.dataset.enabled === '1' ? '0' : '1';
  const params=new URLSearchParams(); params.append('enabled', enabled);
  try{ const {response,data}=await postForm('/settings/scheduler', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка','error');return;} showPopup(data.message||'Сохранено'); setTimeout(()=>window.location.reload(),500);}catch{showPopup('Ошибка соединения с сервером','error');}
});
document.getElementById('toggleFileLoggingBtn')?.addEventListener('click', async function(){
  const enabled = this.dataset.enabled === '1' ? '0' : '1';
  const params=new URLSearchParams(); params.append('enabled', enabled);
  try{ const {response,data}=await postForm('/settings/file-logging', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка','error');return;} showPopup(data.message||'Сохранено'); setTimeout(()=>window.location.reload(),500);}catch{showPopup('Ошибка соединения с сервером','error');}
});
document.getElementById('clearAppLogsBtn')?.addEventListener('click', async function(){
  if(!confirm('Очистить app логи?')) return;
  const params=new URLSearchParams(); params.append('kind','app');
  try{ const {response,data}=await postForm('/settings/logs/clear', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка','error');return;} showPopup(data.message||'Логи очищены'); }catch{showPopup('Ошибка соединения с сервером','error');}
});
document.getElementById('clearTaskLogsBtn')?.addEventListener('click', async function(){
  if(!confirm('Очистить task логи?')) return;
  const params=new URLSearchParams(); params.append('kind','task');
  try{ const {response,data}=await postForm('/settings/logs/clear', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка','error');return;} showPopup(data.message||'Логи очищены'); }catch{showPopup('Ошибка соединения с сервером','error');}
});

})();
