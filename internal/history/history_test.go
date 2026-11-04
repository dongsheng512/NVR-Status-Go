package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

func setupHistory(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("NVRSTATUS_DATA_DIR", dataDir)
	dir := filepath.Join(dataDir, "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sampleReport(device, status string) model.Report {
	return model.Report{
		DeviceName: device,
		IP:         "10.0.0.1",
		Info:       map[string]string{},
		Health:     model.HealthSummary{Status: status, Stats: model.HealthStats{}},
		Records:    []model.Record{{Channel: "1", Name: "cam1", DiskStatus: "正常", RecordingOK: "正常"}},
	}
}

func TestSaveListLoad(t *testing.T) {
	dir := setupHistory(t)
	path, err := SaveReport(sampleReport("门卫", "良好"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("report path = %q, want dir %q", path, dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report file missing: %v", err)
	}
	if !strings.Contains(filepath.Base(path), "_门卫.json") {
		t.Fatalf("filename = %q, want timestamp_门卫.json", filepath.Base(path))
	}

	metas, err := ListReports()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("len(ListReports()) = %d, want 1", len(metas))
	}
	meta := metas[0]
	if meta.Path != path || meta.Name != filepath.Base(path) {
		t.Fatalf("meta path/name = %#v", meta)
	}
	if meta.DeviceName != "门卫" || meta.IP != "10.0.0.1" || meta.Health != "良好" || meta.Channels != 1 {
		t.Fatalf("meta = %#v", meta)
	}
	if meta.ModTime.IsZero() {
		t.Fatal("ModTime is zero")
	}

	report, err := LoadReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeviceName != "门卫" || report.Health.Status != "良好" || len(report.Records) != 1 {
		t.Fatalf("loaded report = %#v", report)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["saved_at"] == "" || payload["saved_at"] == nil {
		t.Fatal("saved_at missing from archived JSON")
	}
	if payload["_file"] != filepath.Base(path) {
		t.Fatalf("_file = %#v, want %q", payload["_file"], filepath.Base(path))
	}
}

func TestSaveReportSanitizesFilename(t *testing.T) {
	setupHistory(t)
	path, err := SaveReport(sampleReport("设备 A/B:C", "良好"))
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(path)
	if !strings.Contains(base, "设备_A_B_C") {
		t.Fatalf("filename = %q, want sanitized device name", base)
	}
	if strings.ContainsAny(base, `/\:`) {
		t.Fatalf("filename contains path separators: %q", base)
	}
}

func TestSaveReportAvoidsSameSecondOverwrite(t *testing.T) {
	dir := setupHistory(t)
	first, err := SaveReport(sampleReport("NVR1", "良好"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := SaveReport(sampleReport("NVR1", "良好"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("second archive reused %q", first)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d files, want 2", len(entries))
	}
}

func TestListReportsIgnoresCorruptFiles(t *testing.T) {
	dir := setupHistory(t)
	if err := os.WriteFile(filepath.Join(dir, "zzz.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveReport(sampleReport("好设备", "良好")); err != nil {
		t.Fatal(err)
	}
	metas, err := ListReports()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].DeviceName != "好设备" {
		t.Fatalf("ListReports() = %#v", metas)
	}
	if _, err := LoadReport(filepath.Join(dir, "zzz.json")); err == nil {
		t.Fatal("LoadReport(corrupt) error = nil, want error")
	}
	if _, err := LoadReport(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("LoadReport(missing) error = nil, want error")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	dir := setupHistory(t)
	oldMax := MaxReports
	MaxReports = 100
	t.Cleanup(func() { MaxReports = oldMax })

	baseTime := time.Now().Add(-time.Hour)
	var paths []string
	for i := 0; i < 5; i++ {
		path, err := SaveReport(sampleReport("NVR"+string(rune('0'+i)), "良好"))
		if err != nil {
			t.Fatal(err)
		}
		ts := baseTime.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}

	MaxReports = 3
	prune(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d files after prune, want 3", len(entries))
	}
	for _, removed := range paths[:2] {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("oldest file %q was not pruned", removed)
		}
	}
	metas, err := ListReports()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("len(ListReports()) = %d, want 3", len(metas))
	}
	wantOrder := []string{"NVR4", "NVR3", "NVR2"}
	for i, want := range wantOrder {
		if metas[i].DeviceName != want {
			t.Fatalf("ListReports()[%d].DeviceName = %q, want %q (full: %#v)", i, metas[i].DeviceName, want, metas)
		}
	}
}

func TestListReportsDefaultsHealthToUnknown(t *testing.T) {
	dir := setupHistory(t)
	raw := `{"device_name":"旧报告","ip":"10.0.0.2","records":[]}`
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	metas, err := ListReports()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Health != "未知" || metas[0].Channels != 0 {
		t.Fatalf("metas = %#v", metas)
	}
}
