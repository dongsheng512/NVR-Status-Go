package main

import (
	"os"
	"path/filepath"
	"testing"

	"nvrstatus/internal/model"
)

func TestResolveOptionsKeepsProfileValues(t *testing.T) {
	profile := model.DefaultScanOptions()
	profile.BusyStart = 9
	profile.BusyEnd = 18
	profile.DeviceWorkers = 3
	profile.DeepAVCheck = true

	a := &agentArgs{set: map[string]bool{}} // no flags supplied
	got := resolveOptions(a, profile)
	if got.BusyStart != 9 || got.BusyEnd != 18 || got.DeviceWorkers != 3 || !got.DeepAVCheck {
		t.Fatalf("profile options not preserved: %+v", got)
	}
}

func TestResolveOptionsFlagsOverrideProfile(t *testing.T) {
	profile := model.DefaultScanOptions()
	profile.BusyStart = 9
	profile.BusyEnd = 18
	profile.DeviceWorkers = 3
	profile.DeepAVCheck = true

	a := &agentArgs{
		set:           map[string]bool{"device-workers": true, "deep-av-check": true, "no-search": true},
		deviceWorkers: 1,
		deep:          false,
		noSearch:      true,
	}
	got := resolveOptions(a, profile)
	if got.DeviceWorkers != 1 || got.DeepAVCheck || !got.NoSearch {
		t.Fatalf("explicit flags not applied: %+v", got)
	}
	if got.BusyStart != 9 || got.BusyEnd != 18 {
		t.Fatalf("unspecified profile fields were changed: %+v", got)
	}
}

func TestLoadConfigReadsProfileScanOptions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NVRSTATUS_DATA_DIR", dir)
	raw := `{
  "version": 1,
  "active_profile": "测试",
  "profiles": {
    "测试": {
      "name": "测试",
      "devices": [{"name":"NVR-A","ip":"192.0.2.10","port":80,"username":"admin","password":""}],
      "scan_options": {"lookback": 90, "busy_start": 9, "busy_end": 18, "device_workers": 3, "deep_av_check": true}
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	devices, name, opts, err := loadConfig("", "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "测试" || len(devices) != 1 {
		t.Fatalf("name=%q devices=%d", name, len(devices))
	}
	if opts.BusyStart != 9 || opts.BusyEnd != 18 || opts.DeviceWorkers != 3 || !opts.DeepAVCheck || opts.Lookback != 90 {
		t.Fatalf("scan options = %+v", opts)
	}
}
