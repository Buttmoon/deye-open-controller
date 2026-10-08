package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func (a *App) apiSystemInfoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	wd, _ := os.Getwd()
	dbSize := int64(0)
	if st, err := os.Stat(filepath.Join("data", "app.db")); err == nil {
		dbSize = st.Size()
	}
	counts := map[string]int{}
	for table, key := range map[string]string{"inverters": "inverters", "schedule_templates": "templates", "schedules": "schedules", "operation_history": "history_rows", "task_runs": "task_runs", "simple_templates": "simple_templates"} {
		var n int
		_ = a.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		counts[key] = n
	}
	profiles := []map[string]any{}
	if models, err := availableInverterModels(); err == nil {
		for _, m := range models {
			entry := map[string]any{"key": m.Key, "name": m.Name, "file": m.ParametersFile, "validation_status": m.ValidationStatus, "write_requires_confirmation": m.WriteRequiresConfirm}
			if _, _, err := loadDeviceParametersForModel(m.Key); err != nil {
				entry["write_check"] = "ошибка: " + err.Error()
			} else {
				entry["write_check"] = "пройдена"
			}
			if _, params, err := loadDeviceParametersForModelStructural(m.Key); err == nil {
				issues := validateProfileStructure(params)
				warn := 0
				for _, i := range issues {
					if i.Severity == "warning" {
						warn++
					}
				}
				entry["structure_warnings"] = warn
				entry["parameters"] = len(params)
			}
			profiles = append(profiles, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": buildVersion, "build": buildInfoText(), "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"started_utc": a.startedAt.UTC(), "uptime_seconds": int64(time.Since(a.startedAt).Seconds()), "workdir": wd,
		"listen": listenAddress(), "db_size_bytes": dbSize, "db_size_human": formatBytes(dbSize), "counts": counts,
		"defaults": a.defaultsReport, "customized_defaults": a.defaultsReport.Customized(), "profiles": profiles,
		"features": map[string]bool{"grid_peak_shaving": a.gridPeakFeatureEnabled(), "register_test_writes": a.kvBool(kvFeatureRegisterTestWrites), "simple_scheduler": a.kvBool(kvFeatureSimpleScheduler)},
	})
}

// apiSystemBackupHandler streams a ZIP with a consistent SQLite snapshot and
// all model/register profiles. Logs are not included.
func (a *App) apiSystemBackupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, jsonResponse{OK: false, Message: "Метод не поддерживается"})
		return
	}
	tmpDir, err := os.MkdirTemp("", "inverter-backup-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось создать временный каталог: " + err.Error()})
		return
	}
	defer os.RemoveAll(tmpDir)
	snapshot := filepath.Join(tmpDir, "app.db")
	if _, err := a.db.Exec(`VACUUM INTO ?`, snapshot); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Не удалось создать снимок базы: " + err.Error()})
		return
	}
	files := map[string]string{"data/app.db": snapshot, "inverter_models.json": inverterModelsFilePath, "device_parameters.json": legacyParametersFilePath}
	if entries, err := os.ReadDir("device_parameters"); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				files["device_parameters/"+e.Name()] = filepath.Join("device_parameters", e.Name())
			}
		}
	}
	stamp := time.Now().Format("20060102-150405")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "inverter-schedule-backup-"+stamp+".zip"))
	zw := zip.NewWriter(w)
	for name, src := range files {
		f, err := os.Open(src)
		if err != nil {
			continue
		}
		dst, err := zw.Create(name)
		if err == nil {
			_, _ = io.Copy(dst, f)
		}
		f.Close()
	}
	if dst, err := zw.Create("README.txt"); err == nil {
		_, _ = io.WriteString(dst, "Резервная копия "+buildInfoText()+"\nСоздана: "+time.Now().Format(time.RFC3339)+
			"\nВосстановление: остановите службу, распакуйте архив в рабочий каталог (data/app.db, inverter_models.json, device_parameters/), запустите службу.\n")
	}
	_ = zw.Close()
	a.recordHistory(HistoryEntry{OperationType: opManualOperation, Status: histApplied, Initiator: requestInitiator(r), Message: "Создана резервная копия базы и профилей"})
}
