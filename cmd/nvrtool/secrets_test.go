package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTrueValues(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profiles.json")
	data := map[string]any{
		"profiles": map[string]any{
			"测试档案": map[string]any{
				"devices": []any{
					map[string]any{"name": "真实NVR-A", "ip": "10.1.2.3", "host": "10.1.2.4"},
					map[string]any{"name": "真实NVR-B", "address": "172.16.5.6"},
				},
			},
		},
	}
	b, _ := json.Marshal(data)
	if err := os.WriteFile(profile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadTrueValues([]string{filepath.Join(dir, "missing.json"), profile})
	for _, want := range []string{"真实NVR-A", "真实NVR-B", "10.1.2.3", "10.1.2.4", "172.16.5.6"} {
		if !got[want] {
			t.Fatalf("missing value %q in %v", want, got)
		}
	}
}

func TestLoadTrueValuesIgnoresNonIPHostAndShortNames(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profiles.json")
	raw := `{"profiles":{"p":{"devices":[{"name":"ab","ip":"not-an-ip","host":"example.com"}]}}}`
	if err := os.WriteFile(profile, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadTrueValues([]string{profile}); len(got) != 0 {
		t.Fatalf("values = %v, want empty", got)
	}
}

func TestRFC1918Regex(t *testing.T) {
	for _, s := range []string{"10.1.2.3", "172.16.5.6", "172.31.255.255", "192.168.1.64"} {
		if !rfc1918Re.MatchString(s) {
			t.Errorf("%q should match", s)
		}
	}
	for _, s := range []string{"192.0.2.10", "203.0.113.5", "172.15.1.1", "8.8.8.8"} {
		if rfc1918Re.MatchString(s) {
			t.Errorf("%q should not match", s)
		}
	}
}

func TestIsTextFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"internal/scan/scan.go", true},
		{"README.md", true},
		{"build/darwin-arm64/x.md", false},
		{".git/config.json", false},
		{"resources/darwin/bin/ffmpeg", false},
		{"docs/ui.png", false},
	}
	for _, c := range cases {
		if got := isTextFile(c.path); got != c.want {
			t.Errorf("isTextFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestContainsAnyNeverPrintsValues(t *testing.T) {
	values := map[string]bool{"真实NVR-A": true, "10.1.2.3": true}
	if !containsAny("设备 真实NVR-A 正常", values) {
		t.Fatal("expected a hit")
	}
	if containsAny("示例 NVR-A / 192.0.2.10", values) {
		t.Fatal("sample values must not hit")
	}
}

func TestSortedUnique(t *testing.T) {
	got := sortedUnique([]string{"b.go", "a.go", "b.go"})
	if strings.Join(got, ",") != "a.go,b.go" {
		t.Fatalf("got %v", got)
	}
}

// The gate must actually block a staged-style scan when a real value appears,
// and must never print the value itself in the process.
func TestRunSecretsBlocksRealValue(t *testing.T) {
	profileDir := t.TempDir()
	profile := filepath.Join(profileDir, "profiles.json")
	if err := os.WriteFile(profile, []byte(`{"profiles":{"p":{"devices":[{"name":"内网NVR-真实","ip":"10.9.8.7"}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "notes.md"), []byte("设备 内网NVR-真实 在线\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NVRSTATUS_PROFILE", profile)
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := runSecrets([]string{"--all"}); !errors.Is(err, errBlocked) {
		t.Fatalf("runSecrets = %v, want errBlocked", err)
	}
}

// With no local profile the hook degrades to a warning, never a block.
func TestRunSecretsWithoutProfileOnlyWarns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("APPDATA", "")
	t.Setenv("NVRSTATUS_PROFILE", filepath.Join(home, "no-such-file.json"))
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "sample.md"), []byte("示例 192.168.1.64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := runSecrets([]string{"--all"}); err != nil {
		t.Fatalf("runSecrets = %v, want nil (warning only)", err)
	}
}
