package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) createInverterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
			return
		}
	} else if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	ip := strings.TrimSpace(r.FormValue("ip"))
	portStr := strings.TrimSpace(r.FormValue("port"))
	modelKey := normalizeInverterModelKey(r.FormValue("model_key"))
	profileWriteConfirmed := r.FormValue("profile_write_confirmed") == "1" || strings.EqualFold(r.FormValue("profile_write_confirmed"), "true") || strings.EqualFold(r.FormValue("profile_write_confirmed"), "on")
	if ip == "" {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "IP обязателен"})
		return
	}
	if portStr == "" {
		portStr = "8899"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Неверный порт"})
		return
	}

	id, err := a.createInverter(name, ip, port, modelKey, profileWriteConfirmed)
	if err != nil {
		msg := "Ошибка сохранения инвертора"
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
			msg = "Инвертор с таким IP и портом уже существует"
		}
		log.Println("createInverter error:", err)
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: msg})
		return
	}

	if name == "" {
		name = fmt.Sprintf("Инвертор %d", id)
		if err := a.updateInverterName(id, name); err != nil {
			log.Println("updateInverterName error:", err)
			writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Инвертор создан, но не удалось установить имя"})
			return
		}
	}

	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: fmt.Sprintf("Инвертор \"%s\" успешно добавлен", name)})
}

func (a *App) deleteInverterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
			return
		}
	} else if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}

	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Неверный ID инвертора"})
		return
	}
	if err := a.deleteInverterByID(id); err != nil {
		log.Println("deleteInverterByID error:", err)
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка удаления инвертора"})
		return
	}
	a.recreateActiveSchedulerTask("inverter deleted")
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: "Инвертор удалён, задача пересоздана"})
}

func (a *App) updateInverterSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Ошибка чтения формы"})
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Неверный ID инвертора"})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = fmt.Sprintf("Инвертор %d", id)
	}
	modelKey := normalizeInverterModelKey(r.FormValue("model_key"))
	profileWriteConfirmed := r.FormValue("profile_write_confirmed") == "1" || strings.EqualFold(r.FormValue("profile_write_confirmed"), "true") || strings.EqualFold(r.FormValue("profile_write_confirmed"), "on")
	model, err := findInverterModel(modelKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: err.Error()})
		return
	}
	// Only a structurally broken profile prevents selecting the model. The
	// stricter write check keeps gating writes on its own and is reported here.
	if _, _, err := loadDeviceParametersForModelStructural(modelKey); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResponse{OK: false, Message: "Профиль модели не читается: " + err.Error()})
		return
	}
	note := ""
	if _, _, err := loadDeviceParametersForModel(modelKey); err != nil {
		note = " Внимание: проверка безопасности записи для профиля не пройдена — чтение работает, запись расписаний для этой модели заблокирована (" + err.Error() + ")."
	}
	if err := a.updateInverterSettings(id, name, modelKey, profileWriteConfirmed); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Ошибка сохранения настроек инвертора: " + err.Error()})
		return
	}
	a.recreateActiveSchedulerTask("inverter model changed")
	a.restartInverterLogger("inverter model changed")
	a.appendAppLog("info", "inverter settings updated", map[string]any{"component": "inverters", "inverter_id": id, "model_key": model.Key, "parameters_file": model.ParametersFile})
	writeJSON(w, http.StatusOK, jsonResponse{OK: true, Message: fmt.Sprintf("Настройки сохранены: %s.", model.Name) + note})
}
