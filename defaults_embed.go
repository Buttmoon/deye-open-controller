package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed inverter_models.json device_parameters.json device_parameters/*.json
var embeddedDefaults embed.FS

// knownPreviousDefaults lists SHA-256 hashes of earlier bundled versions. An
// on-disk file that still matches one of them was never customised and is
// upgraded (with a backup); any other difference is treated as a user edit.
var knownPreviousDefaults = map[string][]string{
	"inverter_models.json":   {"d81df2ca24421a5fb765e796f84689e6acf72b1937ad2b06561c12ad0f8d6e2f"},
	"device_parameters.json": {"c1e3c1d38627b2b949737e3f103bc2671cc6294d03becf96b4d9792ef6ee8983"},
	"device_parameters/device_parameters_deye_60kw.json":                     {"c1e3c1d38627b2b949737e3f103bc2671cc6294d03becf96b4d9792ef6ee8983"},
	"device_parameters/device_parameters_deye_60kw_v1054_hv_x10.json":        {"c457f87a7939ec0132c9c8d7bb123d8ea1dd85c137fc8665b271a2785d00aafe"},
	"device_parameters/device_parameters_deye_sun_25k_sg01hp3_eu_am2.json":   {"7af76d13025ab24b3aa19ae9e71d22181dc860231a2805b5eac3a64f5c393948"},
	"device_parameters/device_parameters_deye_sun_25k_sg01hp3_eu_am2_v1054.json": {"4212495cc8b09af85fe39b7752cd0815a43ff58c0619e104925b66155104c7e6"},
	"device_parameters/device_parameters_deye_sun_30k_sg02hp3_eu_am3.json":   {"6f417c03c897b5a7a77be33440730c84e20286d9100c94429c48c1162b12f6c6"},
}

type defaultFileState struct {
	Path   string `json:"path"`
	Action string `json:"action"` // created | unchanged | upgraded | customized
	Backup string `json:"backup,omitempty"`
}

type defaultsInstallReport struct {
	Files []defaultFileState `json:"files"`
}

func (r defaultsInstallReport) Summary() string {
	counts := map[string]int{}
	for _, f := range r.Files {
		counts[f.Action]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

func (r defaultsInstallReport) Customized() []string {
	out := []string{}
	for _, f := range r.Files {
		if f.Action == "customized" {
			out = append(out, f.Path)
		}
	}
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func embeddedDefaultFile(name string) ([]byte, bool) {
	b, err := embeddedDefaults.ReadFile(name)
	return b, err == nil
}

// installEmbeddedDefaults creates the data directories and writes bundled model
// and register profiles that are missing under root. Customised files are never
// overwritten.
func installEmbeddedDefaults(root string) (defaultsInstallReport, error) {
	report := defaultsInstallReport{}
	for _, dir := range []string{"data", filepath.Join("data", "backups"), filepath.Join("data", "inverter_logs"), "device_parameters"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			return report, err
		}
	}
	names := []string{}
	err := fs.WalkDir(embeddedDefaults, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	sort.Strings(names)
	stamp := time.Now().Format("20060102-150405")
	for _, name := range names {
		data, _ := embeddedDefaults.ReadFile(name)
		target := filepath.Join(root, filepath.FromSlash(name))
		current, err := os.ReadFile(target)
		switch {
		case os.IsNotExist(err):
			if err := writeFileAtomic(target, data); err != nil {
				return report, err
			}
			report.Files = append(report.Files, defaultFileState{Path: name, Action: "created"})
		case err != nil:
			return report, err
		case sha256Hex(current) == sha256Hex(data):
			report.Files = append(report.Files, defaultFileState{Path: name, Action: "unchanged"})
		case containsString(knownPreviousDefaults[name], sha256Hex(current)):
			backup := filepath.Join(root, "data", "backups", strings.ReplaceAll(name, "/", "__")+"."+stamp+".bak")
			if err := os.WriteFile(backup, current, 0644); err != nil {
				return report, err
			}
			if err := writeFileAtomic(target, data); err != nil {
				return report, err
			}
			report.Files = append(report.Files, defaultFileState{Path: name, Action: "upgraded", Backup: backup})
		default:
			report.Files = append(report.Files, defaultFileState{Path: name, Action: "customized"})
		}
	}
	return report, nil
}

func writeFileAtomic(target string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+path.Base(filepath.ToSlash(target))+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return err
	}
	deviceParametersCache.Lock()
	delete(deviceParametersCache.entries, target)
	deviceParametersCache.Unlock()
	return nil
}
