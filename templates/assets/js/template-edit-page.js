(function(){
  if (!window.scheduleEditor) return;
  document.getElementById('saveTemplateBtn')?.addEventListener('click', async function(){
    window.scheduleEditor.syncJSON();
    const params = new URLSearchParams();
    params.append('id', document.getElementById('templateID').value);
    params.append('name', document.getElementById('templateName').value);
    params.append('description', document.getElementById('templateDescription').value);
    params.append('view_mode', document.getElementById('viewMode').value);
    params.append('schedule_json', document.getElementById('scheduleJSON').value);
    try {
      const {response, data} = await postForm('/templates/save', params);
      if (!response.ok || !data.ok) { showPopup(data.message || 'Ошибка сохранения шаблона', 'error'); return; }
      showPopup(data.message || 'Шаблон сохранён');
      setTimeout(() => window.location.href = data.redirect || '/templates/list', 700);
    } catch {
      showPopup('Ошибка соединения с сервером', 'error');
    }
  });
})();
