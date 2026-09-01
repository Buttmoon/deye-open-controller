(function(){
  document.getElementById('applyTemplateBtn')?.addEventListener('click', async function(){
    const templateID=document.getElementById('templateSelect').value; const ids=document.getElementById('selectedIDs').value; if(!templateID){showPopup('Выбери шаблон','error'); return;}
    const params=new URLSearchParams(); params.append('template_id', templateID); params.append('ids', ids);
    try{ const {response,data}=await postForm('/templates/apply-to-inverters', params); if(!response.ok||!data.ok){showPopup(data.message||'Ошибка применения шаблона','error'); return;} showPopup(data.message||'Шаблон применён'); setTimeout(()=>window.location.href=data.redirect||'/',700);}catch{showPopup('Ошибка соединения с сервером','error');}
  });
})();
