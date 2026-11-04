// Package history archives inspection reports as JSON files and lists them for
// the GUI/CLI. It is a Go port of services/history.py, with the history
// directory rooted at <app data>/reports (same as the Python app).
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nvrstatus/internal/config"
	"nvrstatus/internal/model"
	"nvrstatus/internal/util"
)

// MaxReports is the maximum number of archived reports kept on disk. Older
// files are pruned first.
var MaxReports = 500

// dataDir is indirected so tests can redirect the archive without touching the
// real user data directory.
var dataDir = config.AppDataDir

// Dir returns the history directory, creating it if needed.
func Dir() string {
	path := filepath.Join(dataDir(), "reports")
	_ = os.MkdirAll(path, 0o755)
	return path
}

// ReportMeta is one row of the history listing.
type ReportMeta struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	ModTime    time.Time `json:"mod_time"`
	DeviceName string    `json:"device_name"`
	IP         string    `json:"ip"`
	Health     string    `json:"health"`
	Channels   int       `json:"channels"`
}

// archivedReport is the on-disk representation: the report plus the two
// bookkeeping fields the Python version added.
type archivedReport struct {
	model.Report
	SavedAt string `json:"saved_at"`
	File    string `json:"_file"`
}

// SaveReport archives one report and returns the file path. Filenames follow
// the Python app: <timestamp>_<device>.json, with a numeric suffix on collision.
func SaveReport(r model.Report) (string, error) {
	dir := Dir()
	stamp := time.Now().Format("20060102_150405")

	device := strings.TrimSpace(r.DeviceName)
	if device == "" {
		device = "NVR"
	}
	device = util.SafeFilename(device, 60)
	base := fmt.Sprintf("%s_%s", stamp, device)
	path := filepath.Join(dir, base+".json")
	for n := 1; ; n++ {
		if _, err := os.Stat(path); err != nil {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s_%d.json", base, n))
	}

	payload := archivedReport{
		Report:  r,
		SavedAt: time.Now().Format("2006-01-02 15:04:05"),
		File:    filepath.Base(path),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化巡检报告失败: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("写入巡检报告失败: %w", err)
	}
	prune(dir)
	return path, nil
}

// ListReports returns report metadata newest-first. Corrupt or unreadable
// files are skipped.
func ListReports() ([]ReportMeta, error) {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取历史报告目录失败: %w", err)
	}
	type payload struct {
		SavedAt    string `json:"saved_at"`
		DeviceName string `json:"device_name"`
		IP         string `json:"ip"`
		Health     struct {
			Status string `json:"健康状态"`
		} `json:"health"`
		Records []json.RawMessage `json:"records"`
	}
	metas := make([]ReportMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var data payload
		if err := json.Unmarshal(raw, &data); err != nil {
			continue
		}
		health := data.Health.Status
		if health == "" {
			health = "未知"
		}
		metas = append(metas, ReportMeta{
			Path:       path,
			Name:       entry.Name(),
			ModTime:    info.ModTime(),
			DeviceName: data.DeviceName,
			IP:         data.IP,
			Health:     health,
			Channels:   len(data.Records),
		})
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].ModTime.Equal(metas[j].ModTime) {
			return metas[i].Name > metas[j].Name
		}
		return metas[i].ModTime.After(metas[j].ModTime)
	})
	return metas, nil
}

// LoadReport reads one archived report. Missing or corrupt files return an
// error.
func LoadReport(path string) (*model.Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取历史报告失败: %w", err)
	}
	var report model.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("解析历史报告失败: %w", err)
	}
	return &report, nil
}

// prune deletes the oldest .json files until at most MaxReports remain.
func prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		path    string
		modTime time.Time
	}
	files := make([]file, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, file{path: filepath.Join(dir, entry.Name()), modTime: info.ModTime()})
	}
	if len(files) <= MaxReports {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	for _, old := range files[:len(files)-MaxReports] {
		_ = os.Remove(old.path)
	}
}
