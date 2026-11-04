// Package monitor holds the latest site scan and exposes it as JSON and
// Prometheus metrics. It is used by the headless nvr-agent command.
package monitor

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"nvrstatus/internal/model"
)

// Snapshot is a consistent view of the latest scan.
type Snapshot struct {
	ScannedAt time.Time      `json:"scanned_at"`
	Duration  float64        `json:"duration_seconds"`
	Reports   []model.Report `json:"reports"`
}

// Monitor stores the latest reports and their scan metadata.
type Monitor struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

// New returns an empty monitor.
func New() *Monitor { return &Monitor{} }

// Update replaces the latest snapshot.
func (m *Monitor) Update(reports []model.Report, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshot = Snapshot{
		ScannedAt: time.Now(),
		Duration:  duration.Seconds(),
		Reports:   append([]model.Report(nil), reports...),
	}
}

// Snapshot returns a copy of the latest scan.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := m.snapshot
	out.Reports = append([]model.Report(nil), m.snapshot.Reports...)
	return out
}

// Healthy reports false when any device failed or is in 严重 health.
func (s Snapshot) Healthy() bool {
	if len(s.Reports) == 0 {
		return false
	}
	for _, r := range s.Reports {
		if r.Error != "" || r.Health.Status == "严重" {
			return false
		}
	}
	return true
}

// Metrics renders the latest scan in Prometheus text exposition format.
func (s Snapshot) Metrics() string {
	var b strings.Builder
	b.WriteString("# HELP nvr_scan_timestamp_seconds Unix time of the latest scan.\n")
	b.WriteString("# TYPE nvr_scan_timestamp_seconds gauge\n")
	fmt.Fprintf(&b, "nvr_scan_timestamp_seconds %.0f\n", float64(s.ScannedAt.Unix()))
	b.WriteString("# HELP nvr_scan_duration_seconds Duration of the latest scan.\n")
	b.WriteString("# TYPE nvr_scan_duration_seconds gauge\n")
	fmt.Fprintf(&b, "nvr_scan_duration_seconds %.3f\n", s.Duration)
	b.WriteString("# HELP nvr_up Whether the device responded (1) or failed (0).\n")
	b.WriteString("# TYPE nvr_up gauge\n")
	for _, r := range s.Reports {
		up := 1
		if r.Error != "" {
			up = 0
		}
		fmt.Fprintf(&b, "nvr_up{device=%q,ip=%q} %d\n", r.DeviceName, r.IP, up)
	}
	b.WriteString("# HELP nvr_health_status Device health verdict as a one-hot gauge.\n")
	b.WriteString("# TYPE nvr_health_status gauge\n")
	for _, r := range s.Reports {
		status := r.Health.Status
		if status == "" {
			status = "未知"
		}
		fmt.Fprintf(&b, "nvr_health_status{device=%q,ip=%q,status=%q} 1\n", r.DeviceName, r.IP, status)
	}
	b.WriteString("# HELP nvr_cameras_total Configured camera channels.\n")
	b.WriteString("# TYPE nvr_cameras_total gauge\n")
	for _, r := range s.Reports {
		fmt.Fprintf(&b, "nvr_cameras_total{device=%q,ip=%q} %d\n", r.DeviceName, r.IP, r.Health.Stats.CameraTotal)
	}
	b.WriteString("# HELP nvr_cameras_online Camera channels online.\n")
	b.WriteString("# TYPE nvr_cameras_online gauge\n")
	for _, r := range s.Reports {
		fmt.Fprintf(&b, "nvr_cameras_online{device=%q,ip=%q} %d\n", r.DeviceName, r.IP, r.Health.Stats.Online)
	}
	b.WriteString("# HELP nvr_channels_total Recording channels.\n")
	b.WriteString("# TYPE nvr_channels_total gauge\n")
	for _, r := range s.Reports {
		fmt.Fprintf(&b, "nvr_channels_total{device=%q,ip=%q} %d\n", r.DeviceName, r.IP, r.Health.Stats.ChannelTotal)
	}
	b.WriteString("# HELP nvr_disk_usage_percent Disk usage by drive.\n")
	b.WriteString("# TYPE nvr_disk_usage_percent gauge\n")
	for _, r := range s.Reports {
		for _, d := range r.Drives {
			pct := parsePercent(d.Usage)
			fmt.Fprintf(&b, "nvr_disk_usage_percent{device=%q,ip=%q,drive=%q} %.1f\n",
				r.DeviceName, r.IP, d.Name, pct)
		}
	}
	b.WriteString("# HELP nvr_audio_probe_warnings Audio probe warnings.\n")
	b.WriteString("# TYPE nvr_audio_probe_warnings gauge\n")
	for _, r := range s.Reports {
		fmt.Fprintf(&b, "nvr_audio_probe_warnings{device=%q,ip=%q} %d\n", r.DeviceName, r.IP, r.Health.Stats.AudioProbeWarn)
	}
	return b.String()
}

func parsePercent(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	var v float64
	_, _ = fmt.Sscanf(s, "%f", &v)
	return v
}

// SortedReports returns reports sorted by device name for stable output.
func (s Snapshot) SortedReports() []model.Report {
	out := append([]model.Report(nil), s.Reports...)
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceName < out[j].DeviceName })
	return out
}
