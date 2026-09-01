(function(){
  if (!window.scheduleEditor) return;

  document.getElementById('saveScheduleBtn')?.addEventListener('click', async function(){
    try {
      await saveCurrentSchedule(true);
    } catch (e) {
      showPopup(e.message || 'Ошибка соединения с сервером', 'error');
    }
  });

  document.getElementById('saveTemplateBtn')?.addEventListener('click', async function(){
    window.scheduleEditor.syncJSON();
    const name = prompt('Введите имя шаблона');
    if (!name) return;
    const description = prompt('Описание шаблона', '') || '';
    const params = new URLSearchParams();
    params.append('name', name);
    params.append('description', description);
    params.append('view_mode', document.getElementById('viewMode').value);
    params.append('schedule_json', document.getElementById('scheduleJSON').value);
    try {
      const {response, data} = await postForm('/templates', params);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка сохранения шаблона', 'error'); return; }
      showPopup(data.message || 'Шаблон сохранён');
      setTimeout(() => window.location.reload(), 600);
    } catch {
      showPopup('Ошибка соединения с сервером', 'error');
    }
  });

  async function refreshTemplateSelect(preferredNames = []) {
    const select = document.getElementById('templateSelect');
    if (!select) return '';
    const response = await fetch('/api/templates');
    const data = await response.json();
    if (!response.ok || !data.ok || !Array.isArray(data.items)) return select.value || '';
    const currentValue = select.value;
    select.innerHTML = '<option value="">-- выбрать шаблон --</option>';
    data.items.forEach(item => {
      const opt = document.createElement('option');
      opt.value = item.id;
      opt.textContent = item.name;
      select.appendChild(opt);
    });
    const preferred = preferredNames.find(name => Array.from(select.options).some(opt => opt.textContent === name));
    if (preferred) {
      const opt = Array.from(select.options).find(opt => opt.textContent === preferred);
      if (opt) select.value = opt.value;
    } else if (currentValue && Array.from(select.options).some(opt => opt.value === currentValue)) {
      select.value = currentValue;
    }
    return select.value || '';
  }

  document.getElementById('uploadTemplateBtn')?.addEventListener('click', function(){
    document.getElementById('uploadTemplateFile')?.click();
  });

  document.getElementById('uploadTemplateFile')?.addEventListener('change', async function(){
    const file = this.files && this.files[0];
    if (!file) return;
    const button = document.getElementById('uploadTemplateBtn');
    const originalText = button ? button.textContent : '';
    if (button) {
      button.disabled = true;
      button.textContent = 'Загружаю...';
    }
    try {
      const formData = new FormData();
      formData.append('xlsx_file', file);
      const response = await fetch('/templates/import/upload?format=json', {
        method: 'POST',
        headers: { 'Accept': 'application/json' },
        body: formData
      });
      const data = await response.json();
      if (!response.ok || !data.ok) {
        showPopup(data.message || 'Ошибка загрузки шаблона', 'error');
        return;
      }
      const imported = Array.isArray(data.imported) ? data.imported : [];
      const created = imported.filter(item => item.created);
      const selectedTemplateID = await refreshTemplateSelect(created.map(item => item.name));
      if (created.length > 0 && selectedTemplateID) {
        await loadTemplateIntoEditor(selectedTemplateID, false);
      }
      const failedCount = imported.length - created.length;
      const msg = failedCount > 0
        ? `Загружено шаблонов: ${created.length}, ошибок: ${failedCount}`
        : (data.message || `Загружено шаблонов: ${created.length}`);
      showPopup(msg, failedCount > 0 ? 'error' : 'success');
    } catch (e) {
      showPopup(e.message || 'Ошибка соединения с сервером', 'error');
    } finally {
      this.value = '';
      if (button) {
        button.disabled = false;
        button.textContent = originalText;
      }
    }
  });

  function escapeHTML(value) {
    return String(value ?? '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#039;');
  }

  function renderProfileRows(profile) {
    const rows = Array.isArray(profile.rows) ? profile.rows : [];
    if (!rows.length) return '<div class="muted">Нет регистров для этого профиля.</div>';
    return `<div class="register-profile-block">
      <h4>${escapeHTML(profile.model_name || profile.model_key || 'Профиль')}</h4>
      <div class="muted"><code>${escapeHTML(profile.model_key || '')}</code> · ${escapeHTML(profile.parameters_file || '')} · ${escapeHTML(profile.validation_status || '')}</div>
      <div class="table-wrap"><table class="register-preview-table"><thead><tr><th>#</th><th>Код</th><th>Адрес(а)</th><th>Тип</th><th>Writable</th><th>В расписании</th><th>Логическое</th><th>Raw Modbus</th><th>Комментарий</th></tr></thead><tbody>
      ${rows.map(row => `<tr class="${row.writable === false || row.encode_error ? 'register-row-muted' : ''}">
        <td>${escapeHTML(row.order)}</td>
        <td><code>${escapeHTML(row.code)}</code></td>
        <td>${Array.isArray(row.addresses) && row.addresses.length ? escapeHTML(row.addresses.join(', ')) : (row.address === null || row.address === undefined ? '<span class="muted">не найден</span>' : escapeHTML(row.address))}</td>
        <td>${escapeHTML(row.register_type || '')}</td>
        <td>${row.writable === true ? 'Да' : (row.writable === false ? 'Нет' : '—')}</td>
        <td>${escapeHTML(row.schedule_value)}${Number(row.schedule_input_scale_factor || 1) !== 1 ? ` <small class="muted">×${escapeHTML(row.schedule_input_scale_factor)}</small>` : ''}</td>
        <td><strong>${escapeHTML(row.logical_value)}</strong></td>
        <td>${row.encode_error ? `<span class="status-error-text">${escapeHTML(row.encode_error)}</span>` : `<strong>${escapeHTML(row.raw_value)}</strong>`}</td>
        <td class="muted">${escapeHTML(row.note || '')}</td>
      </tr>`).join('')}
      </tbody></table></div>
    </div>`;
  }

  function renderRegisterPreview(data) {
    const panel = document.getElementById('registerPreviewPanel');
    if (!panel) return;
    const profiles = Array.isArray(data.profiles) && data.profiles.length
      ? data.profiles
      : [{model_name:'Профиль по умолчанию', rows:Array.isArray(data.rows) ? data.rows : []}];
    const slot = data.slot || {};
    const inverters = Array.isArray(data.inverters) && data.inverters.length > 0
      ? data.inverters.map(inv => `${escapeHTML(inv.name || 'Инвертор')} · ${escapeHTML(inv.ip || '')}:${escapeHTML(inv.port || '')}<br><small>${escapeHTML(inv.model_name || inv.model_key || '')}</small>`).join('<br>')
      : 'Инвертор не выбран';
    const selectedSlotText = data.will_send === false
      ? `<div class="register-preview-warning">${escapeHTML(data.message || 'По текущему времени расписание не будет отправлено')}</div>`
      : `<div class="muted">Текущий слот: день ${escapeHTML(data.day)}, ${escapeHTML(slot.label || data.hour || '')}, minute=${escapeHTML(data.minute)}, source=${escapeHTML(slot.source || 'hour')}. Для каждого типа инвертора показаны его адреса и raw-значения.</div>`;
    panel.classList.remove('hidden');
    panel.innerHTML = `<div class="send-preview-head">
      <div>
        <h3>Предпросмотр Modbus-записи по моделям</h3>
        ${selectedSlotText}
      </div>
      <div class="send-preview-badge">Use Timer: ${escapeHTML(data.use_timer_mask)} · ${escapeHTML(data.timezone || '')}</div>
    </div>
    <div class="register-preview-meta">
      <div><strong>Время расчёта</strong><br>${escapeHTML(data.generated_at_local || '')}</div>
      <div><strong>Инверторы</strong><br>${inverters}</div>
    </div>
    ${data.will_send === false ? '' : profiles.map(renderProfileRows).join('')}`;
    panel.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }

  async function saveCurrentSchedule(redirectAfterSave = false) {
    window.scheduleEditor.syncJSON();
    const params = new URLSearchParams();
    params.append('ids', document.getElementById('selectedIDs').value);
    params.append('name', document.getElementById('scheduleName').value);
    params.append('view_mode', document.getElementById('viewMode').value);
    params.append('schedule_json', document.getElementById('scheduleJSON').value);
    params.append('template_id', document.getElementById('editingTemplateId')?.value || '');
    const {response, data} = await postForm('/schedules/save', params);
    if (!response.ok || !data.ok) throw new Error(data.message || 'Ошибка сохранения расписания');
    if (redirectAfterSave) {
      showPopup(data.message || 'Сохранено');
      setTimeout(() => window.location.href = data.redirect || '/', 700);
    }
    return data;
  }

  document.getElementById('previewScheduleRegistersBtn')?.addEventListener('click', async function(){
    window.scheduleEditor.syncJSON();
    const params = new URLSearchParams();
    params.append('ids', document.getElementById('selectedIDs').value);
    params.append('schedule_json', document.getElementById('scheduleJSON').value);
    const originalText = this.textContent;
    this.disabled = true;
    this.textContent = 'Считаю регистры...';
    try {
      const {response, data} = await postForm('/api/schedules/send-preview', params);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка расчёта регистров', 'error'); return; }
      renderRegisterPreview(data);
      showPopup(data.message || 'Предпросмотр готов');
    } catch (e) {
      showPopup(e.message || 'Ошибка соединения с сервером', 'error');
    } finally {
      this.disabled = false;
      this.textContent = originalText;
    }
  });

  document.getElementById('clearEmptySlotsBtn')?.addEventListener('click', function(){
    const removed = window.scheduleEditor.clearEmptyCustomSlots ? window.scheduleEditor.clearEmptyCustomSlots() : 0;
    window.scheduleEditor.syncJSON();
    const panel = document.getElementById('registerPreviewPanel');
    if (panel) panel.classList.add('hidden');
    if (removed > 0) {
      showPopup(`Дефолтные 5-минутные слоты удалены: ${removed}`);
    } else {
      showPopup('Дефолтных 5-минутных слотов не найдено');
    }
  });



  document.getElementById('clearAllFiveMinuteSlotsBtn')?.addEventListener('click', function(){
    if (!confirm('Удалить все кастомные 5-минутные слоты? Изменённые 5-минутные значения тоже будут удалены.')) return;
    const removed = window.scheduleEditor.clearAllFiveMinuteSlots ? window.scheduleEditor.clearAllFiveMinuteSlots() : 0;
    window.scheduleEditor.syncJSON();
    const panel = document.getElementById('registerPreviewPanel');
    if (panel) panel.classList.add('hidden');
    if (removed > 0) {
      showPopup(`Все 5-минутные слоты удалены: ${removed}`);
    } else {
      showPopup('5-минутных слотов не найдено');
    }
  });

  document.getElementById('sendScheduleNowBtn')?.addEventListener('click', async function(){
    window.scheduleEditor.syncJSON();
    if (!confirm('Сохранить текущие значения и сразу отправить расписание на инвертор?')) return;
    this.disabled = true;
    const originalText = this.textContent;
    this.textContent = 'Сохраняю и отправляю...';
    try {
      await saveCurrentSchedule(false);
      const {response, data} = await postForm('/api/tasks/run-now', new URLSearchParams());
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка отправки расписания', 'error'); return; }
      showPopup(data.message || 'Расписание сохранено и команда отправки запущена');
      setTimeout(() => window.location.href = '/tasks/logs', 1000);
    } catch (e) {
      showPopup(e.message || 'Ошибка соединения с сервером', 'error');
    } finally {
      this.disabled = false;
      this.textContent = originalText;
    }
  });

  async function loadTemplateIntoEditor(templateID, showSuccess = true) {
    if (!templateID) { showPopup('Выбери шаблон', 'error'); return false; }
    try {
      const response = await fetch('/templates/apply?id=' + encodeURIComponent(templateID));
      const data = await response.json();
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка загрузки шаблона', 'error'); return false; }
      document.getElementById('scheduleName').value = data.name || 'Расписание';
      document.getElementById('viewMode').value = data.view_mode || 'grid';
      const editId = document.getElementById('editingTemplateId');
      if (editId) editId.value = data.id || '';
      let parsed = {days: []};
      try { parsed = JSON.parse(data.schedule_json || '{}'); } catch (_) {}
      window.scheduleEditor.setScheduleData(parsed);
      if (showSuccess) showPopup('Шаблон применён');
      return true;
    } catch {
      showPopup('Ошибка соединения с сервером', 'error');
      return false;
    }
  }

  document.getElementById('applyTemplateBtn')?.addEventListener('click', async function(){
    await loadTemplateIntoEditor(document.getElementById('templateSelect').value, true);
  });

  document.getElementById('templateSelect')?.addEventListener('change', async function(){
    if (this.value) await loadTemplateIntoEditor(this.value, true);
  });
})();
