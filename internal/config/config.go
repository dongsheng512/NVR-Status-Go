// Package config implements the multi-profile profiles.json store used by the
// CLI and GUI. It is a Go port of config_store.py and keeps the on-disk format
// compatible with the Python v2.3.0 application.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"nvrstatus/internal/credentials"
	"nvrstatus/internal/model"
)

const appName = "NVRStatus"

// Store owns one profiles.json file plus its in-memory representation.
type Store struct {
	Path string
	Data model.Store
}

// Credential hooks are package variables so tests (and future alternate
// backends) can substitute an in-memory keyring without ever touching the real
// OS store.
var (
	credAvailable = credentials.Available
	credSet       = credentials.SetPassword
	credGet       = credentials.GetPassword
	credDelete    = credentials.DeletePassword
	credRekey     = credentials.RekeyPassword
)

// AppDataDir returns the per-user application data directory.
//
// NVRSTATUS_DATA_DIR can override it (useful for portable installs and for
// tests); otherwise the platform-specific location from the Python version is
// used.
func AppDataDir() string {
	if override := strings.TrimSpace(os.Getenv("NVRSTATUS_DATA_DIR")); override != "" {
		return expandPath(override)
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", appName)
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		return filepath.Join(base, appName)
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", appName)
	}
}

// DefaultPath returns the default profiles.json location.
func DefaultPath() string {
	return filepath.Join(AppDataDir(), "profiles.json")
}

// New creates a Store for path and loads it. An empty path selects
// DefaultPath().
func New(path string) *Store {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	s := &Store{Path: expandPath(strings.TrimSpace(path)), Data: defaultStore()}
	// The constructor mirrors the Python ConfigStore: it always attempts a
	// load. Load errors leave the store with usable defaults; callers that need
	// to surface them can call Load or Save explicitly.
	_ = s.Load()
	return s
}

// Load reads the store from disk.
//
// A missing file is created with the default profile (after attempting to
// migrate a legacy nvr_config.json). Invalid JSON is backed up to
// profiles.json.bak-<timestamp> before the store is reset to defaults.
func (s *Store) Load() error {
	if s.Path == "" {
		s.Path = DefaultPath()
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.Data = defaultStore()
			s.tryMigrateLegacy()
			return s.Save()
		}
		// A transient read error (locked file, permissions) must not destroy
		// the on-disk file. Keep defaults for this session and report it.
		s.Data = defaultStore()
		s.tryMigrateLegacy()
		return fmt.Errorf("读取配置失败: %w", err)
	}

	parsed, empty, err := parseStore(raw)
	if err != nil {
		s.backupCorrupt()
		s.Data = defaultStore()
		s.tryMigrateLegacy()
		return s.Save()
	}
	s.Data = parsed
	if empty {
		// A syntactically valid file with an empty profiles map is reset to
		// defaults and persisted (matching the Python loader).
		return s.Save()
	}
	return nil
}

// Save atomically writes the store: a unique temporary file in the target
// directory is renamed over profiles.json (mode 0600 on Unix).
func (s *Store) Save() error {
	if s.Path == "" {
		s.Path = DefaultPath()
	}
	if s.Data.Profiles == nil {
		s.Data.Profiles = map[string]*model.Profile{}
	}
	data, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	data = append(data, '\n')

	target := expandPath(s.Path)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".profiles-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭配置失败: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpName, 0o600); err != nil {
			return fmt.Errorf("设置配置文件权限失败: %w", err)
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

// ListProfiles returns profile names in ascending order.
func (s *Store) ListProfiles() []string {
	names := make([]string, 0, len(s.Data.Profiles))
	for name := range s.Data.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ActiveName returns the active profile name, defaulting to 默认.
func (s *Store) ActiveName() string {
	if s.Data.ActiveProfile == "" {
		return "默认"
	}
	return s.Data.ActiveProfile
}

// SetActive switches the active profile and persists the change.
func (s *Store) SetActive(name string) bool {
	if _, ok := s.Data.Profiles[name]; !ok {
		return false
	}
	s.Data.ActiveProfile = name
	return s.Save() == nil
}

// GetProfile returns a deep copy of a profile. Missing profiles yield a
// fully-populated default snapshot named after the request; they are never
// inserted into the store.
func (s *Store) GetProfile(name string) (*model.Profile, error) {
	if name == "" {
		name = s.ActiveName()
	}
	if stored, ok := s.Data.Profiles[name]; ok && stored != nil {
		cp := cloneProfile(stored)
		return &cp, nil
	}
	fresh := defaultProfile(name)
	return fresh, nil
}

// ResolveDevices returns the runtime device view for a profile, filling empty
// passwords from the OS credential store when it is available.
func (s *Store) ResolveDevices(name string) ([]model.Device, error) {
	profileName := name
	if profileName == "" {
		profileName = s.ActiveName()
	}
	prof, err := s.GetProfile(profileName)
	if err != nil {
		return nil, err
	}
	devices := make([]model.Device, len(prof.Devices))
	copy(devices, prof.Devices)
	if !credAvailable() {
		return devices, nil
	}
	for i := range devices {
		if devices[i].Password == "" {
			devices[i].Password = credGet(profileName, devices[i])
		}
	}
	return devices, nil
}

// UpdateProfile replaces a profile, moving plaintext passwords into the OS
// credential store when possible. Removed devices have their credentials
// purged.
func (s *Store) UpdateProfile(name string, p *model.Profile) error {
	if p == nil {
		return errors.New("profile 不能为空")
	}
	if name == "" {
		name = s.ActiveName()
	}
	next := cloneProfile(p)
	next.Name = name

	var oldDevices []model.Device
	if old, ok := s.Data.Profiles[name]; ok && old != nil {
		oldDevices = old.Devices
	}
	purgeStaleDevices(name, oldDevices, next.Devices)

	next.Devices = migratePasswords(name, next.Devices)
	s.Data.Profiles[name] = &next
	return s.Save()
}

// CreateProfile creates a profile, optionally cloning an existing one, and
// makes it active. The returned name may carry a numeric suffix when the
// requested name already exists.
func (s *Store) CreateProfile(name, cloneFrom string) (string, error) {
	base := safeProfileName(name)
	name = base
	for i := 1; ; i++ {
		if _, exists := s.Data.Profiles[name]; !exists {
			break
		}
		name = fmt.Sprintf("%s_%d", base, i)
	}

	var prof *model.Profile
	if cloneFrom != "" {
		if src, ok := s.Data.Profiles[cloneFrom]; ok && src != nil {
			cp := cloneProfile(src)
			cp.Name = name
			if credAvailable() {
				for i := range cp.Devices {
					pw := cp.Devices[i].Password
					if pw == "" {
						pw = credGet(cloneFrom, cp.Devices[i])
					}
					if pw != "" && credSet(name, cp.Devices[i], pw) {
						cp.Devices[i].Password = ""
					}
				}
			}
			prof = &cp
		}
	}
	if prof == nil {
		prof = defaultProfile(name)
	}
	prof.Name = name
	prof.Devices = migratePasswords(name, prof.Devices)
	s.Data.Profiles[name] = prof
	s.Data.ActiveProfile = name
	if err := s.Save(); err != nil {
		return name, err
	}
	return name, nil
}

// RenameProfile renames old to new, rekeying stored credentials. It returns
// false when old does not exist or new is already taken.
func (s *Store) RenameProfile(old, newName string) bool {
	newName = safeProfileName(newName)
	if old == newName {
		return true
	}
	prof, ok := s.Data.Profiles[old]
	if !ok || prof == nil {
		return false
	}
	if _, exists := s.Data.Profiles[newName]; exists {
		return false
	}
	delete(s.Data.Profiles, old)
	prof.Name = newName
	s.Data.Profiles[newName] = prof
	if s.Data.ActiveProfile == old {
		s.Data.ActiveProfile = newName
	}
	if credAvailable() {
		for _, d := range prof.Devices {
			credRekey(old, newName, d)
		}
	}
	return s.Save() == nil
}

// DeleteProfile removes a profile and purges its credentials. At least one
// profile is always retained.
func (s *Store) DeleteProfile(name string) bool {
	prof, ok := s.Data.Profiles[name]
	if !ok || prof == nil {
		return false
	}
	if len(s.Data.Profiles) <= 1 {
		return false
	}
	delete(s.Data.Profiles, name)
	purgeKeyring(name, prof.Devices)
	if s.Data.ActiveProfile == name {
		for _, candidate := range s.ListProfiles() {
			s.Data.ActiveProfile = candidate
			break
		}
	}
	return s.Save() == nil
}

// ExportProfile writes a single profile to path. Passwords are resolved from
// the credential store so the export can migrate to another machine; the file
// is written atomically with mode 0600 on Unix.
func (s *Store) ExportProfile(name, path string) error {
	prof, err := s.GetProfile(name)
	if err != nil {
		return err
	}
	devices, err := s.ResolveDevices(name)
	if err != nil {
		return err
	}
	prof.Devices = devices
	data, err := json.MarshalIndent(prof, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化档案失败: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(expandPath(path), data, 0o600)
}

// ImportProfile imports a single-profile JSON file under name (or the file's
// own name / 导入 when name is empty) and makes it active.
func (s *Store) ImportProfile(path, name string) (string, error) {
	raw, err := os.ReadFile(expandPath(path))
	if err != nil {
		return "", fmt.Errorf("读取导入文件失败: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", fmt.Errorf("导入文件不是有效 JSON: %w", err)
	}
	if _, ok := root["devices"]; !ok {
		return "", errors.New("无法识别的配置文件格式")
	}
	if _, ok := root["profiles"]; ok {
		return "", errors.New("无法识别的配置文件格式")
	}

	importedName := ""
	if value, ok := root["name"]; ok {
		_ = json.Unmarshal(value, &importedName)
	}
	if name == "" {
		name = importedName
		if name == "" {
			name = "导入"
		}
	}
	profileName := safeProfileName(name)

	prof := defaultProfile(profileName)
	if value, ok := root["default"]; ok {
		_ = json.Unmarshal(value, &prof.Default)
	}
	if value, ok := root["devices"]; ok {
		var devices []model.Device
		if err := json.Unmarshal(value, &devices); err != nil {
			return "", fmt.Errorf("导入设备列表无效: %w", err)
		}
		// Python treats an empty list as "use the empty-device default".
		if len(devices) > 0 {
			prof.Devices = devices
		}
	}
	if value, ok := root["scan_options"]; ok {
		if err := json.Unmarshal(value, &prof.ScanOptions); err != nil {
			return "", fmt.Errorf("导入扫描选项无效: %w", err)
		}
	}
	prof.Name = profileName
	prof.Devices = migratePasswords(profileName, prof.Devices)
	s.Data.Profiles[profileName] = prof
	s.Data.ActiveProfile = profileName
	if err := s.Save(); err != nil {
		return profileName, err
	}
	return profileName, nil
}

// ---- internals ----

func defaultDevice() model.Device {
	return model.Device{
		Name:     "NVR1",
		IP:       "192.168.1.100",
		Port:     80,
		Username: "admin",
		Password: "",
		SSL:      false,
	}
}

func defaultProfile(name string) *model.Profile {
	return &model.Profile{
		Name:        name,
		Devices:     []model.Device{defaultDevice()},
		Default:     0,
		ScanOptions: model.DefaultScanOptions(),
	}
}

func defaultStore() model.Store {
	return model.Store{
		Version:       1,
		ActiveProfile: "默认",
		Profiles: map[string]*model.Profile{
			"默认": defaultProfile("默认"),
		},
	}
}

func cloneProfile(p *model.Profile) model.Profile {
	if p == nil {
		return *defaultProfile("")
	}
	out := *p
	out.Devices = append([]model.Device(nil), p.Devices...)
	out.ScanOptions = p.ScanOptions
	out.ScanOptions.AVChannels = append([]int(nil), p.ScanOptions.AVChannels...)
	out.ScanOptions.AVAt = append([]time.Time(nil), p.ScanOptions.AVAt...)
	return out
}

// parseStore decodes profiles.json, layering each profile over the built-in
// defaults so that omitted fields keep their v2 defaults (important because 0
// is a meaningful value for several scan options).
func parseStore(raw []byte) (model.Store, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return model.Store{}, false, err
	}
	profilesRaw, ok := root["profiles"]
	if !ok {
		return model.Store{}, false, errors.New("缺少 profiles 字段")
	}
	var rawProfiles map[string]json.RawMessage
	if err := json.Unmarshal(profilesRaw, &rawProfiles); err != nil {
		return model.Store{}, false, fmt.Errorf("profiles 字段无效: %w", err)
	}

	out := model.Store{Version: 1, Profiles: map[string]*model.Profile{}}
	if value, ok := root["version"]; ok {
		_ = json.Unmarshal(value, &out.Version)
	}
	if out.Version == 0 {
		out.Version = 1
	}
	if value, ok := root["active_profile"]; ok {
		_ = json.Unmarshal(value, &out.ActiveProfile)
	}

	for name, rawProfile := range rawProfiles {
		prof := defaultProfile(name)
		if err := json.Unmarshal(rawProfile, prof); err != nil {
			return model.Store{}, false, fmt.Errorf("档案 %q 无效: %w", name, err)
		}
		prof.Name = name
		out.Profiles[name] = prof
	}
	if len(out.Profiles) == 0 {
		return defaultStore(), true, nil
	}
	if _, ok := out.Profiles[out.ActiveProfile]; !ok {
		out.ActiveProfile = firstProfileName(out.Profiles)
	}
	return out, false, nil
}

func firstProfileName(profiles map[string]*model.Profile) string {
	if _, ok := profiles["默认"]; ok {
		return "默认"
	}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "默认"
	}
	return names[0]
}

// backupCorrupt moves an unreadable profiles.json aside, keeping at most the
// three most recent backups.
func (s *Store) backupCorrupt() {
	stamp := time.Now().Format("20060102-150405")
	dst := fmt.Sprintf("%s.bak-%s", s.Path, stamp)
	for n := 1; ; n++ {
		if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = fmt.Sprintf("%s.bak-%s-%d", s.Path, stamp, n)
	}
	if err := os.Rename(s.Path, dst); err != nil {
		return
	}
	matches, _ := filepath.Glob(s.Path + ".bak-*")
	sort.Strings(matches)
	for len(matches) > 3 {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
}

// tryMigrateLegacy imports an old single-profile nvr_config.json when the new
// profiles.json does not exist yet.
func (s *Store) tryMigrateLegacy() {
	type legacyConfig struct {
		Devices []model.Device `json:"devices"`
		Default int            `json:"default"`
	}
	for _, candidate := range legacyCandidates(s.Path) {
		raw, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var legacy legacyConfig
		if err := json.Unmarshal(raw, &legacy); err != nil {
			continue
		}
		if len(legacy.Devices) == 0 {
			continue
		}
		prof := defaultProfile("默认")
		prof.Devices = legacy.Devices
		prof.Default = legacy.Default
		s.Data.Profiles["默认"] = prof
		s.Data.ActiveProfile = "默认"
		return
	}
}

func legacyCandidates(profilesPath string) []string {
	var candidates []string
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		for _, existing := range candidates {
			if existing == p {
				return
			}
		}
		candidates = append(candidates, p)
	}
	if profilesPath != "" {
		add(filepath.Join(filepath.Dir(profilesPath), "nvr_config.json"))
	}
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "nvr_config.json"))
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(exe), "nvr_config.json"))
	}
	return candidates
}

func migratePasswords(profileName string, devices []model.Device) []model.Device {
	if !credAvailable() {
		return devices
	}
	for i := range devices {
		pw := devices[i].Password
		if pw == "" {
			continue
		}
		// Only clear the plaintext after the keychain write succeeded.
		if credSet(profileName, devices[i], pw) {
			devices[i].Password = ""
		}
	}
	return devices
}

func purgeStaleDevices(profileName string, oldDevices, newDevices []model.Device) {
	if !credAvailable() {
		return
	}
	type key struct{ ip, name string }
	keep := make(map[key]bool, len(newDevices))
	for _, d := range newDevices {
		keep[key{strings.TrimSpace(d.IP), strings.TrimSpace(d.Name)}] = true
	}
	for _, d := range oldDevices {
		if !keep[key{strings.TrimSpace(d.IP), strings.TrimSpace(d.Name)}] {
			credDelete(profileName, d)
		}
	}
}

func purgeKeyring(profileName string, devices []model.Device) {
	if !credAvailable() {
		return
	}
	for _, d := range devices {
		credDelete(profileName, d)
	}
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".nvr-export-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭文件失败: %w", err)
	}
	if runtime.GOOS != "windows" && mode != 0 {
		if err := os.Chmod(tmpName, mode); err != nil {
			return fmt.Errorf("设置文件权限失败: %w", err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换文件失败: %w", err)
	}
	return nil
}

var unsafeProfileChars = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]+`)

func safeProfileName(name string) string {
	cleaned := strings.TrimSpace(name)
	cleaned = unsafeProfileChars.ReplaceAllString(cleaned, "_")
	runes := []rune(cleaned)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	cleaned = string(runes)
	if cleaned == "" {
		return "未命名"
	}
	return cleaned
}

func expandPath(path string) string {
	if path == "" {
		return path
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
