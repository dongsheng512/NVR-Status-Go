package history

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nvrstatus/internal/model"
)

// Diff is the change between two scans of the same device.
type Diff struct {
	HealthBefore string   `json:"health_before"`
	HealthAfter  string   `json:"health_after"`
	NewIssues    []string `json:"new_issues,omitempty"`
	Resolved     []string `json:"resolved,omitempty"`
	StillBad     []string `json:"still_bad,omitempty"`
}

// HasChanges reports whether anything worth showing changed.
func (d Diff) HasChanges() bool {
	return d.HealthBefore != d.HealthAfter || len(d.NewIssues) > 0 || len(d.Resolved) > 0
}

// Summary is a compact human sentence for the GUI/CLI.
func (d Diff) Summary() string {
	parts := make([]string, 0, 4)
	if d.HealthBefore != "" && d.HealthBefore != d.HealthAfter {
		parts = append(parts, fmt.Sprintf("健康 %s → %s", d.HealthBefore, d.HealthAfter))
	}
	if len(d.NewIssues) > 0 {
		parts = append(parts, fmt.Sprintf("新增异常 %d", len(d.NewIssues)))
	}
	if len(d.Resolved) > 0 {
		parts = append(parts, fmt.Sprintf("已恢复 %d", len(d.Resolved)))
	}
	if len(parts) == 0 {
		return "与上次相比无变化"
	}
	return strings.Join(parts, " · ")
}

// LatestForDevice returns the newest archived report for one device IP.
func LatestForDevice(ip string) (*model.Report, error) {
	return LatestForDeviceExcept(ip, "")
}

// LatestForDeviceExcept is LatestForDevice ignoring the archive at skipPath.
// The GUI saves the fresh report before computing the diff off the UI thread;
// without the exclusion it could compare that report against itself and always
// report "no changes".
func LatestForDeviceExcept(ip, skipPath string) (*model.Report, error) {
	if strings.TrimSpace(ip) == "" {
		return nil, nil
	}
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	cands := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if skipPath != "" && samePath(full, skipPath) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		cands = append(cands, candidate{path: full, modTime: info.ModTime()})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].modTime.Equal(cands[j].modTime) {
			return cands[i].path > cands[j].path
		}
		return cands[i].modTime.After(cands[j].modTime)
	})
	for _, c := range cands {
		rep, err := LoadReport(c.path)
		if err != nil {
			continue
		}
		if rep.IP == ip {
			return rep, nil
		}
	}
	return nil, nil
}

func samePath(a, b string) bool {
	if a == b || filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	if sa, err := os.Stat(a); err == nil {
		if sb, err := os.Stat(b); err == nil {
			return os.SameFile(sa, sb)
		}
	}
	return false
}

// Compare diffs two reports. Issue detection mirrors the CLI's
// "problem record" rule: recording/online/audio/probe values that are neither
// normal nor intentionally skipped count as an issue.
func Compare(prev, cur model.Report) Diff {
	d := Diff{HealthBefore: prev.Health.Status, HealthAfter: cur.Health.Status}
	if prev.Error != "" || cur.Error != "" {
		return d
	}
	prevByKey := make(map[string]model.Record, len(prev.Records))
	for _, r := range prev.Records {
		prevByKey[recordKey(r)] = r
	}
	seen := make(map[string]bool, len(cur.Records))
	for _, r := range cur.Records {
		key := recordKey(r)
		seen[key] = true
		bad, reason := recordIssue(r)
		prevRec, existed := prevByKey[key]
		prevBad, _ := recordIssue(prevRec)
		if bad {
			label := fmt.Sprintf("通道 %s %s: %s", r.Channel, r.Name, reason)
			if !existed || !prevBad {
				d.NewIssues = append(d.NewIssues, label)
			} else {
				d.StillBad = append(d.StillBad, label)
			}
		} else if existed && prevBad {
			d.Resolved = append(d.Resolved, fmt.Sprintf("通道 %s %s", r.Channel, r.Name))
		}
	}
	for _, r := range prev.Records {
		if seen[recordKey(r)] {
			continue
		}
		if bad, _ := recordIssue(r); bad {
			d.Resolved = append(d.Resolved, fmt.Sprintf("通道 %s %s（已移除）", r.Channel, r.Name))
		}
	}
	return d
}

func recordKey(r model.Record) string {
	if r.TrackID != "" {
		return r.Channel + "|" + r.TrackID
	}
	return r.Channel
}

func recordIssue(r model.Record) (bool, string) {
	if r.RecordingOK != "" && r.RecordingOK != model.StatusOK && r.RecordingOK != model.StatusSkip {
		return true, "录像 " + r.RecordingOK
	}
	if r.Online == "false" {
		return true, "摄像头离线"
	}
	if r.HasAudio != nil && !*r.HasAudio {
		return true, "未配置音频"
	}
	if r.VideoProbe != "" && r.VideoProbe != model.StatusOK && r.VideoProbe != model.StatusSkip {
		return true, "视频抽检 " + r.VideoProbe
	}
	if r.AudioProbe != "" && r.AudioProbe != model.StatusOK && r.AudioProbe != model.StatusSkip {
		return true, "音频抽检 " + r.AudioProbe
	}
	return false, ""
}
