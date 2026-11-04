package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

// fakeKeyring is an in-memory credential store used by config tests. It never
// touches the real OS keychain.
type fakeKeyring struct {
	available bool
	failSet   bool
	store     map[string]string
}

func newFakeKeyring(available bool) *fakeKeyring {
	return &fakeKeyring{available: available, store: map[string]string{}}
}

func fakeAccount(profile string, d model.Device) string {
	host := strings.TrimSpace(d.IP)
	if host == "" {
		host = strings.TrimSpace(d.Name)
	}
	return profile + "::" + host
}

func (f *fakeKeyring) install(t *testing.T) {
	t.Helper()
	oldAvailable, oldSet, oldGet, oldDelete, oldRekey := credAvailable, credSet, credGet, credDelete, credRekey
	credAvailable = func() bool { return f.available }
	credSet = func(profile string, d model.Device, password string) bool {
		if f.failSet {
			return false
		}
		f.store[fakeAccount(profile, d)] = password
		return true
	}
	credGet = func(profile string, d model.Device) string {
		return f.store[fakeAccount(profile, d)]
	}
	credDelete = func(profile string, d model.Device) {
		delete(f.store, fakeAccount(profile, d))
	}
	credRekey = func(oldProfile, newProfile string, d model.Device) {
		if oldProfile == newProfile {
			return
		}
		oldKey := fakeAccount(oldProfile, d)
		pw := f.store[oldKey]
		if pw == "" {
			return
		}
		f.store[fakeAccount(newProfile, d)] = pw
		delete(f.store, oldKey)
	}
	t.Cleanup(func() {
		credAvailable, credSet, credGet, credDelete, credRekey = oldAvailable, oldSet, oldGet, oldDelete, oldRekey
	})
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	newFakeKeyring(false).install(t)
	return New(filepath.Join(t.TempDir(), "profiles.json"))
}

func sampleProfile(ip, password string) *model.Profile {
	return &model.Profile{
		Name: "默认",
		Devices: []model.Device{{
			Name:     "NVR-A",
			IP:       ip,
			Port:     80,
			Username: "admin",
			Password: password,
		}},
		Default:     0,
		ScanOptions: model.DefaultScanOptions(),
	}
}

func TestDefaultStore(t *testing.T) {
	cases := []struct {
		name       string
		useDefault bool
	}{
		{name: "explicit path"},
		{name: "empty path uses default", useDefault: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newFakeKeyring(false).install(t)
			var path string
			wantPath := ""
			if tc.useDefault {
				dataDir := t.TempDir()
				t.Setenv("NVRSTATUS_DATA_DIR", dataDir)
				wantPath = filepath.Join(dataDir, "profiles.json")
			} else {
				path = filepath.Join(t.TempDir(), "profiles.json")
				wantPath = path
			}
			store := New(path)
			if store.Path != wantPath {
				t.Fatalf("Path = %q, want %q", store.Path, wantPath)
			}
			if _, err := os.Stat(store.Path); err != nil {
				t.Fatalf("default store was not created: %v", err)
			}
			if got := store.ListProfiles(); !reflect.DeepEqual(got, []string{"默认"}) {
				t.Fatalf("ListProfiles() = %#v, want [默认]", got)
			}
			if got := store.ActiveName(); got != "默认" {
				t.Fatalf("ActiveName() = %q, want 默认", got)
			}
			profile, err := store.GetProfile("默认")
			if err != nil {
				t.Fatal(err)
			}
			if profile.Name != "默认" {
				t.Fatalf("profile.Name = %q, want 默认", profile.Name)
			}
			if len(profile.Devices) != 1 {
				t.Fatalf("len(devices) = %d, want 1", len(profile.Devices))
			}
			device := profile.Devices[0]
			if device.Name != "NVR1" || device.IP != "192.168.1.100" || device.Port != 80 || device.Username != "admin" {
				t.Fatalf("default device = %#v", device)
			}
			if !reflect.DeepEqual(profile.ScanOptions, model.DefaultScanOptions()) {
				t.Fatalf("scan options = %#v, want %#v", profile.ScanOptions, model.DefaultScanOptions())
			}

			raw, err := os.ReadFile(store.Path)
			if err != nil {
				t.Fatal(err)
			}
			var decoded model.Store
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("profiles.json is not valid JSON: %v", err)
			}
			if decoded.Profiles["默认"] == nil {
				t.Fatal("decoded store missing 默认 profile")
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("NVRSTATUS_DATA_DIR", "")
	t.Setenv("HOME", home)
	var want string
	switch runtime.GOOS {
	case "darwin":
		want = filepath.Join(home, "Library", "Application Support", "NVRStatus", "profiles.json")
	case "windows":
		appData := filepath.Join(home, "AppData", "Roaming")
		t.Setenv("APPDATA", appData)
		want = filepath.Join(appData, "NVRStatus", "profiles.json")
	default:
		want = filepath.Join(home, ".config", "NVRStatus", "profiles.json")
	}
	if got := DefaultPath(); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestCorruptBackup(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "malformed json", content: "{not valid json"},
		{name: "json array", content: "[]"},
		{name: "missing profiles key", content: `{"version": 1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newFakeKeyring(false).install(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "profiles.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			store := New(path)
			backups, err := filepath.Glob(path + ".bak-*")
			if err != nil {
				t.Fatal(err)
			}
			if len(backups) != 1 {
				t.Fatalf("got %d backups, want 1: %#v", len(backups), backups)
			}
			backup, err := os.ReadFile(backups[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(backup) != tc.content {
				t.Fatalf("backup content = %q, want %q", string(backup), tc.content)
			}
			if store.ActiveName() != "默认" || len(store.ListProfiles()) != 1 {
				t.Fatalf("store was not reset to defaults: active=%q profiles=%#v", store.ActiveName(), store.ListProfiles())
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("reset profiles.json is invalid: %v", err)
			}
		})
	}
}

func TestGetProfileDeepCopyAndMissing(t *testing.T) {
	kr := newFakeKeyring(false)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	profile := sampleProfile("1.2.3.4", "keep")
	profile.ScanOptions.Workers = 12
	profile.ScanOptions.AVChannels = []int{1, 2}
	if err := store.UpdateProfile("默认", profile); err != nil {
		t.Fatal(err)
	}

	first, err := store.GetProfile("默认")
	if err != nil {
		t.Fatal(err)
	}
	first.Name = "changed"
	first.Devices[0].IP = "9.9.9.9"
	first.Devices = append(first.Devices, model.Device{Name: "extra", IP: "8.8.8.8"})
	first.ScanOptions.Workers = 99
	first.ScanOptions.AVChannels[0] = 9

	second, err := store.GetProfile("默认")
	if err != nil {
		t.Fatal(err)
	}
	if second.Name != "默认" {
		t.Fatalf("Name = %q, want 默认", second.Name)
	}
	if len(second.Devices) != 1 || second.Devices[0].IP != "1.2.3.4" {
		t.Fatalf("devices leaked from snapshot: %#v", second.Devices)
	}
	if second.ScanOptions.Workers != 12 || second.ScanOptions.AVChannels[0] != 1 {
		t.Fatalf("scan options leaked from snapshot: %#v", second.ScanOptions)
	}

	missing, err := store.GetProfile("不存在")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Name != "不存在" || len(missing.Devices) != 1 || missing.ScanOptions.Workers != 8 {
		t.Fatalf("missing profile snapshot = %#v", missing)
	}
	for _, name := range store.ListProfiles() {
		if name == "不存在" {
			t.Fatal("GetProfile must not register a missing profile")
		}
	}
}

func TestCreateProfileNames(t *testing.T) {
	cases := []struct {
		name     string
		request  string
		clone    string
		wantName string
	}{
		{name: "new profile", request: "测试", wantName: "测试"},
		{name: "duplicate gets suffix", request: "默认", wantName: "默认_1"},
		{name: "blank becomes 未命名", request: "   ", wantName: "未命名"},
		{name: "clone missing falls back to default", request: "副本", clone: "不存在", wantName: "副本"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			got, err := store.CreateProfile(tc.request, tc.clone)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantName {
				t.Fatalf("CreateProfile() = %q, want %q", got, tc.wantName)
			}
			if _, ok := store.Data.Profiles[got]; !ok {
				t.Fatalf("profile %q missing from store", got)
			}
			if store.ActiveName() != got {
				t.Fatalf("active = %q, want %q", store.ActiveName(), got)
			}
		})
	}
}

func TestCreateCloneCopiesPasswords(t *testing.T) {
	kr := newFakeKeyring(true)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.UpdateProfile("默认", sampleProfile("1.2.3.4", "s3cret")); err != nil {
		t.Fatal(err)
	}
	if _, ok := kr.store["默认::1.2.3.4"]; !ok {
		t.Fatalf("source password not migrated: %#v", kr.store)
	}
	created, err := store.CreateProfile("副本", "默认")
	if err != nil {
		t.Fatal(err)
	}
	if created != "副本" {
		t.Fatalf("created = %q, want 副本", created)
	}
	devices, err := store.ResolveDevices("副本")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Password != "s3cret" || devices[0].IP != "1.2.3.4" {
		t.Fatalf("cloned devices = %#v", devices)
	}
	if kr.store["默认::1.2.3.4"] != "s3cret" {
		t.Fatal("clone must keep the source keyring entry")
	}
}

func TestRenameProfile(t *testing.T) {
	cases := []struct {
		name    string
		old     string
		newName string
		want    bool
	}{
		{name: "success", old: "默认", newName: "机房A", want: true},
		{name: "missing old", old: "不存在", newName: "新名字", want: false},
		{name: "conflict", old: "默认", newName: "B", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			if _, err := store.CreateProfile("B", ""); err != nil {
				t.Fatal(err)
			}
			got := store.RenameProfile(tc.old, tc.newName)
			if got != tc.want {
				t.Fatalf("RenameProfile() = %v, want %v", got, tc.want)
			}
			if tc.want {
				if _, ok := store.Data.Profiles[tc.newName]; !ok {
					t.Fatalf("renamed profile %q missing", tc.newName)
				}
				if _, ok := store.Data.Profiles[tc.old]; ok {
					t.Fatalf("old profile %q still present", tc.old)
				}
			}
		})
	}
}

func TestRenameProfileReKeysCredentials(t *testing.T) {
	kr := newFakeKeyring(true)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.UpdateProfile("默认", sampleProfile("1.2.3.4", "s3cret")); err != nil {
		t.Fatal(err)
	}
	if !store.RenameProfile("默认", "机房A") {
		t.Fatal("RenameProfile returned false")
	}
	if _, ok := kr.store["默认::1.2.3.4"]; ok {
		t.Fatal("old keyring entry was not removed")
	}
	if kr.store["机房A::1.2.3.4"] != "s3cret" {
		t.Fatalf("new keyring entry = %q, want s3cret", kr.store["机房A::1.2.3.4"])
	}
	devices, err := store.ResolveDevices("机房A")
	if err != nil {
		t.Fatal(err)
	}
	if devices[0].Password != "s3cret" {
		t.Fatalf("resolved password = %q", devices[0].Password)
	}
}

func TestDeleteProfile(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*Store)
		target string
		want   bool
	}{
		{name: "missing", target: "不存在", want: false},
		{name: "last profile", target: "默认", want: false},
		{name: "success", setup: func(s *Store) { _, _ = s.CreateProfile("B", "") }, target: "B", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			if tc.setup != nil {
				tc.setup(store)
			}
			if got := store.DeleteProfile(tc.target); got != tc.want {
				t.Fatalf("DeleteProfile() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeleteProfilePurgesCredentials(t *testing.T) {
	kr := newFakeKeyring(true)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.UpdateProfile("默认", sampleProfile("1.2.3.4", "s3cret")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProfile("备份", ""); err != nil {
		t.Fatal(err)
	}
	if !store.DeleteProfile("默认") {
		t.Fatal("DeleteProfile returned false")
	}
	if _, ok := kr.store["默认::1.2.3.4"]; ok {
		t.Fatal("deleted profile's keyring entry still exists")
	}
}

func TestStaleDeviceCredentialPurgedOnUpdate(t *testing.T) {
	kr := newFakeKeyring(true)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	two := &model.Profile{
		Name: "默认",
		Devices: []model.Device{
			{Name: "A", IP: "1.1.1.1", Username: "admin", Password: "p1"},
			{Name: "B", IP: "2.2.2.2", Username: "admin", Password: "p2"},
		},
		ScanOptions: model.DefaultScanOptions(),
	}
	if err := store.UpdateProfile("默认", two); err != nil {
		t.Fatal(err)
	}
	one := &model.Profile{
		Name:        "默认",
		Devices:     []model.Device{{Name: "A", IP: "1.1.1.1", Username: "admin"}},
		ScanOptions: model.DefaultScanOptions(),
	}
	if err := store.UpdateProfile("默认", one); err != nil {
		t.Fatal(err)
	}
	if _, ok := kr.store["默认::1.1.1.1"]; !ok {
		t.Fatal("kept device lost its credential")
	}
	if _, ok := kr.store["默认::2.2.2.2"]; ok {
		t.Fatal("removed device credential was not purged")
	}
}

func TestPlaintextKeptWhenKeyringUnavailable(t *testing.T) {
	kr := newFakeKeyring(false)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.UpdateProfile("默认", sampleProfile("1.2.3.4", "s3cret")); err != nil {
		t.Fatal(err)
	}
	profile, _ := store.GetProfile("默认")
	if profile.Devices[0].Password != "s3cret" {
		t.Fatalf("plaintext password = %q, want s3cret", profile.Devices[0].Password)
	}
	devices, _ := store.ResolveDevices("默认")
	if devices[0].Password != "s3cret" {
		t.Fatalf("resolved password = %q, want s3cret", devices[0].Password)
	}
}

func TestKeyringWriteFailureKeepsPlaintext(t *testing.T) {
	kr := newFakeKeyring(true)
	kr.failSet = true
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.UpdateProfile("默认", sampleProfile("1.2.3.4", "s3cret")); err != nil {
		t.Fatal(err)
	}
	profile, _ := store.GetProfile("默认")
	if profile.Devices[0].Password != "s3cret" {
		t.Fatalf("plaintext password = %q, want s3cret", profile.Devices[0].Password)
	}
	if len(kr.store) != 0 {
		t.Fatalf("keyring should be empty on write failure: %#v", kr.store)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	kr := newFakeKeyring(false)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.UpdateProfile("默认", sampleProfile("10.0.0.7", "s3cret")); err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "profile.json")
	if err := store.ExportProfile("默认", exportPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	var exported map[string]any
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if _, ok := exported["profiles"]; ok {
		t.Fatal("single-profile export must not contain a profiles map")
	}
	devicesRaw, ok := exported["devices"].([]any)
	if !ok || len(devicesRaw) != 1 {
		t.Fatalf("exported devices = %#v", exported["devices"])
	}
	device := devicesRaw[0].(map[string]any)
	if device["password"] != "s3cret" {
		t.Fatalf("exported password = %#v, want s3cret", device["password"])
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(exportPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("export mode = %o, want 600", info.Mode().Perm())
		}
	}

	other := New(filepath.Join(t.TempDir(), "profiles.json"))
	imported, err := other.ImportProfile(exportPath, "导入A")
	if err != nil {
		t.Fatal(err)
	}
	if imported != "导入A" {
		t.Fatalf("ImportProfile() = %q, want 导入A", imported)
	}
	profile, _ := other.GetProfile("导入A")
	if len(profile.Devices) != 1 || profile.Devices[0].IP != "10.0.0.7" || profile.Devices[0].Password != "s3cret" {
		t.Fatalf("imported profile = %#v", profile)
	}
	if other.ActiveName() != "导入A" {
		t.Fatalf("imported profile is not active: %q", other.ActiveName())
	}
}

func TestExportProfileResolvesKeyringPassword(t *testing.T) {
	kr := newFakeKeyring(true)
	kr.install(t)
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	if err := store.UpdateProfile("默认", sampleProfile("10.0.0.7", "s3cret")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := store.ExportProfile("默认", path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var exported model.Profile
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Devices[0].Password != "s3cret" {
		t.Fatalf("exported password = %q, want s3cret", exported.Devices[0].Password)
	}
	// The in-memory/on-disk store must remain without the plaintext.
	profile, _ := store.GetProfile("默认")
	if profile.Devices[0].Password != "" {
		t.Fatalf("store password = %q, want empty", profile.Devices[0].Password)
	}
}

func TestImportProfileVariants(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "import.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name      string
		content   string
		requested string
		wantName  string
		wantIP    string
		wantErr   bool
	}{
		{
			name:     "name from file",
			content:  `{"name":"源档案","devices":[{"name":"A","ip":"1.1.1.1","port":80,"username":"admin","password":""}]}`,
			wantName: "源档案",
			wantIP:   "1.1.1.1",
		},
		{
			name:      "explicit name overrides",
			content:   `{"name":"源档案","devices":[{"name":"A","ip":"2.2.2.2","port":80,"username":"admin","password":""}]}`,
			requested: "指定名",
			wantName:  "指定名",
			wantIP:    "2.2.2.2",
		},
		{
			name:     "empty devices uses default device",
			content:  `{"name":"空设备","devices":[]}`,
			wantName: "空设备",
			wantIP:   "192.168.1.100",
		},
		{
			name:    "not a profile",
			content: `{"version":1}`,
			wantErr: true,
		},
		{
			name:    "full store rejected",
			content: `{"profiles":{"默认":{}}}`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newFakeKeyring(false).install(t)
			store := New(filepath.Join(t.TempDir(), "profiles.json"))
			path := write(t, tc.content)
			got, err := store.ImportProfile(path, tc.requested)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ImportProfile() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantName {
				t.Fatalf("ImportProfile() = %q, want %q", got, tc.wantName)
			}
			profile, _ := store.GetProfile(got)
			if len(profile.Devices) != 1 || profile.Devices[0].IP != tc.wantIP {
				t.Fatalf("imported devices = %#v", profile.Devices)
			}
		})
	}
}

func TestLegacyMigration(t *testing.T) {
	newFakeKeyring(false).install(t)
	dir := t.TempDir()
	legacy := `{"devices":[{"name":"旧NVR","ip":"10.0.0.8","port":8000,"username":"admin","password":""}],"default":0}`
	if err := os.WriteFile(filepath.Join(dir, "nvr_config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(dir, "profiles.json"))
	profile, err := store.GetProfile("默认")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Devices) != 1 || profile.Devices[0].Name != "旧NVR" || profile.Devices[0].Port != 8000 {
		t.Fatalf("legacy devices = %#v", profile.Devices)
	}
	if store.ActiveName() != "默认" {
		t.Fatalf("active = %q, want 默认", store.ActiveName())
	}
}

func TestAtomicSaveLeavesNoTemps(t *testing.T) {
	store := newTestStore(t)
	profile := sampleProfile("10.1.1.1", "")
	if err := store.UpdateProfile("默认", profile); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(store.Path)
	matches, err := filepath.Glob(filepath.Join(dir, ".profiles-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %#v", matches)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("profiles.json mode = %o, want 600", info.Mode().Perm())
		}
	}
	reloaded := New(store.Path)
	got, _ := reloaded.GetProfile("默认")
	if got.Devices[0].IP != "10.1.1.1" {
		t.Fatalf("reloaded IP = %q, want 10.1.1.1", got.Devices[0].IP)
	}
}

func TestSetActive(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "existing", target: "B", want: true},
		{name: "missing", target: "不存在", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			if _, err := store.CreateProfile("B", ""); err != nil {
				t.Fatal(err)
			}
			if got := store.SetActive(tc.target); got != tc.want {
				t.Fatalf("SetActive() = %v, want %v", got, tc.want)
			}
			if tc.want && store.ActiveName() != tc.target {
				t.Fatalf("active = %q, want %q", store.ActiveName(), tc.target)
			}
		})
	}
}
