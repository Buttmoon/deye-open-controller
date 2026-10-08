package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedAssetsPresent(t *testing.T) {
	checks := []struct {
		name string
		ok   bool
	}{
		{"templates/pages/main.html", len(tmplMainHTML) > 100},
		{"templates/pages/history.html", len(tmplHistoryHTML) > 100},
		{"templates/pages/register_test.html", len(tmplRegisterTestHTML) > 100},
		{"templates/pages/status.html", len(tmplStatusHTML) > 100},
		{"templates/pages/simple_schedule.html", len(tmplSimpleScheduleHTML) > 100},
		{"templates/pages/schedule_templates.html", len(tmplScheduleTemplatesHTML) > 100},
		{"inverter-user-guide.html", len(userGuideHTML) > 1000},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("embedded %s missing or too small", c.name)
		}
	}
	if !strings.Contains(string(userGuideHTML), "История заданий и изменений") {
		t.Error("user guide must mention the history page in Russian")
	}
	if !strings.Contains(string(userGuideHTML), "Упрощённый режим") {
		t.Error("user guide must document simplified scheduling")
	}
	if _, err := staticAssets.Open("templates/assets/js/common.js"); err != nil {
		t.Fatalf("embedded common.js: %v", err)
	}
	if _, err := staticAssets.Open("templates/assets/css/app.css"); err != nil {
		t.Fatalf("embedded app.css: %v", err)
	}
}

func TestEmbeddedDefaultsInstallWithoutExternalUI(t *testing.T) {
	dir := t.TempDir()
	report, err := installEmbeddedDefaults(dir)
	if err != nil {
		t.Fatalf("installEmbeddedDefaults: %v", err)
	}
	if len(report.Files) == 0 {
		t.Fatal("expected default files to be created")
	}
	if _, err := os.Stat(filepath.Join(dir, "inverter_models.json")); err != nil {
		t.Fatalf("models missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "device_parameters.json")); err != nil {
		t.Fatalf("legacy profile missing: %v", err)
	}
	// Second install must not overwrite identical files and must not require templates/.
	report2, err := installEmbeddedDefaults(dir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	for _, f := range report2.Files {
		if f.Action == "created" {
			t.Fatalf("second install recreated %s", f.Path)
		}
	}
}

func TestUserGuideHandlerServesStandaloneHTML(t *testing.T) {
	a := newTestApp(t)
	resp := a.testGet(t, "/guide")
	if resp.Code != 200 {
		t.Fatalf("status=%d", resp.Code)
	}
	body := string(resp.Body)
	if !strings.Contains(body, "<!DOCTYPE html>") || !strings.Contains(body, "Deye Open Controller") {
		t.Fatal("guide response is not the interactive HTML manual")
	}
}
