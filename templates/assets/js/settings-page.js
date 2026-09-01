(function(){
  document.getElementById('timezoneForm')?.addEventListener('submit', async function(e){
    e.preventDefault();
    const params = new URLSearchParams();
    params.append('timezone', document.getElementById('timezone').value);
    try {
      const {response, data} = await postForm('/settings/timezone', params);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка', 'error'); return; }
      showPopup(data.message || 'Сохранено');
      setTimeout(()=>window.location.reload(), 500);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  });

  document.getElementById('runtimeSettingsForm')?.addEventListener('submit', async function(e){
    e.preventDefault();
    const params = new URLSearchParams();
    const applicationNameInput = document.getElementById('applicationName');
    if (applicationNameInput) params.append('application_name', applicationNameInput.value);
    params.append('scheduler_enabled', document.getElementById('schedulerEnabled').checked ? '1' : '0');
    params.append('log_level', document.getElementById('logLevel').value);
    params.append('file_logging_enabled', document.getElementById('fileLoggingEnabled').checked ? '1' : '0');
    const fillAllPointsCurrentInput = document.getElementById('schedulerFillAllPointsCurrent');
    if (fillAllPointsCurrentInput) params.append('scheduler_fill_all_points_current', fillAllPointsCurrentInput.checked ? '1' : '0');
    params.append('scheduler_interval_minutes', document.getElementById('schedulerIntervalMinutes').value);
    params.append('app_log_max_size_mb', document.getElementById('appLogMaxSizeMB').value);
    params.append('task_log_max_rows', document.getElementById('taskLogMaxRows').value);
    try {
      const {response, data} = await postForm('/settings/runtime', params);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка', 'error'); return; }
      showPopup(data.message || 'Обновлено');
      setTimeout(()=>window.location.reload(), 600);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  });

  document.getElementById('inverterLoggingForm')?.addEventListener('submit', async function(e){
    e.preventDefault();
    const params = new URLSearchParams();
    params.append('inverter_logging_enabled', document.getElementById('inverterLoggingEnabled').checked ? '1' : '0');
    params.append('inverter_logging_interval_seconds', document.getElementById('inverterLoggingIntervalSeconds').value);
    params.append('inverter_log_max_size_mb', document.getElementById('inverterLogMaxSizeMB').value);
    try {
      const {response, data} = await postForm('/settings/inverter-logging', params);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка', 'error'); return; }
      showPopup(data.message || 'Настройки сохранены');
      setTimeout(()=>window.location.reload(), 700);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  });

  document.getElementById('runNowBtn')?.addEventListener('click', async function(){
    try {
      const {response, data} = await postForm('/api/tasks/run-now', new URLSearchParams());
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка запуска', 'error'); return; }
      showPopup(data.message || 'Задача запущена');
      setTimeout(()=>window.location.reload(), 1000);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  });

  document.getElementById('restartLastTaskBtn')?.addEventListener('click', async function(){
    try {
      const {response, data} = await postForm('/api/tasks/restart-last', new URLSearchParams());
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка перезапуска', 'error'); return; }
      showPopup(data.message || 'Предыдущая задача перезапущена');
      setTimeout(()=>window.location.reload(), 1000);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  });

  document.getElementById('customModbusWriteForm')?.addEventListener('submit', async function(e){
    e.preventDefault();
    const resultBox = document.getElementById('customModbusWriteResult');
    const params = new URLSearchParams();
    params.append('ip', document.getElementById('customModbusIP').value);
    params.append('port', document.getElementById('customModbusPort').value);
    params.append('model_key', document.getElementById('customModbusModel')?.value || '');
    params.append('code', document.getElementById('customModbusCode').value);
    params.append('address', document.getElementById('customModbusAddress').value);
    params.append('value', document.getElementById('customModbusValue').value);
    params.append('retries', document.getElementById('customModbusRetries').value);
    params.append('timeout_ms', document.getElementById('customModbusTimeoutMS').value);
    params.append('retry_delay_seconds', document.getElementById('customModbusRetryDelay').value);
    if (resultBox) resultBox.textContent = 'Выполняю Modbus-запись...';
    try {
      const {response, data} = await postForm('/api/modbus/write', params);
      if (resultBox) resultBox.textContent = JSON.stringify(data, null, 2);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка Modbus-записи', 'error'); return; }
      showPopup(data.message || 'Modbus-запись выполнена');
    } catch {
      if (resultBox) resultBox.textContent = 'Ошибка соединения с сервером';
      showPopup('Ошибка соединения с сервером', 'error');
    }
  });



  async function submitInverterTime(mode){
    const resultBox = document.getElementById('inverterTimeResult');
    const params = new URLSearchParams();
    params.append('ip', document.getElementById('inverterTimeIP')?.value || '');
    params.append('port', document.getElementById('inverterTimePort')?.value || '');
    params.append('model_key', document.getElementById('inverterTimeModel')?.value || '');
    params.append('datetime', document.getElementById('inverterDateTime')?.value || '');
    params.append('second', document.getElementById('inverterSecond')?.value || '0');
    params.append('retries', document.getElementById('inverterTimeRetries')?.value || '3');
    params.append('timeout_ms', document.getElementById('inverterTimeTimeoutMS')?.value || '5000');
    params.append('retry_delay_seconds', document.getElementById('inverterTimeRetryDelay')?.value || '2');
    params.append('mode', mode || 'custom');
    if (resultBox) resultBox.textContent = 'Записываю время инвертора...';
    try {
      const {response, data} = await postForm('/api/inverter-time/set', params);
      if (resultBox) resultBox.textContent = JSON.stringify(data, null, 2);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка записи времени', 'error'); return; }
      showPopup(data.message || 'Время записано');
    } catch {
      if (resultBox) resultBox.textContent = 'Ошибка соединения с сервером';
      showPopup('Ошибка соединения с сервером', 'error');
    }
  }

  document.getElementById('inverterTimeForm')?.addEventListener('submit', function(e){ e.preventDefault(); submitInverterTime('custom'); });
  document.getElementById('setInverterTimeNowBtn')?.addEventListener('click', function(){ submitInverterTime('now'); });

  document.querySelectorAll('[data-clear-kind]').forEach(btn => btn.addEventListener('click', async function(){
    const kind = this.dataset.clearKind;
    if (!confirm('Очистить логи?')) return;
    try {
      const {response, data} = await postForm('/settings/logs/clear?kind=' + encodeURIComponent(kind), new URLSearchParams());
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка очистки', 'error'); return; }
      showPopup(data.message || 'Логи очищены');
      setTimeout(()=>window.location.reload(), 500);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  }));

  document.getElementById('clearSchedulesBtn')?.addEventListener('click', async function(){
    if (!confirm('Очистить все расписания? Инверторы и шаблоны останутся, но scheduler перестанет отправлять расписания.')) return;
    try {
      const {response, data} = await postForm('/settings/schedules/clear', new URLSearchParams());
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка очистки расписаний', 'error'); return; }
      showPopup(data.message || 'Расписания очищены');
      setTimeout(()=>window.location.reload(), 800);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  });

  document.getElementById('clearDbBtn')?.addEventListener('click', async function(){
    if (!confirm('Очистить БД? Это удалит инверторы, шаблоны, расписания и task logs. Системные настройки сохранятся.')) return;
    try {
      const {response, data} = await postForm('/settings/db/clear', new URLSearchParams());
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка очистки БД', 'error'); return; }
      showPopup(data.message || 'БД очищена');
      setTimeout(()=>window.location.reload(), 800);
    } catch { showPopup('Ошибка соединения с сервером', 'error'); }
  });
})();
