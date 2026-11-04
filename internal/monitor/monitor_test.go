package monitor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

func sampleReports() []model.Report {
	return []model.Report{{
		DeviceName: "NVR-A", IP: "192.0.2.10",
		Health: model.HealthSummary{Status: "警告", Stats: model.HealthStats{CameraTotal: 4, Online: 3, ChannelTotal: 4, AudioProbeWarn: 1}},
		Drives: []model.Drive{{Name: "hdd1", Usage: "98.0%"}},
	}}
}

func TestMetrics(t *testing.T) {
	m := New()
	m.Update(sampleReports(), 2*time.Second)
	out := m.Snapshot().Metrics()
	for _, want := range []string{
		`nvr_up{device="NVR-A",ip="192.0.2.10"} 1`,
		`nvr_health_status{device="NVR-A",ip="192.0.2.10",status="警告"} 1`,
		`nvr_cameras_online{device="NVR-A",ip="192.0.2.10"} 3`,
		`nvr_disk_usage_percent{device="NVR-A",ip="192.0.2.10",drive="hdd1"} 98.0`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics missing %q\n%s", want, out)
		}
	}
}

func TestHealthz(t *testing.T) {
	m := New()
	m.Update(sampleReports(), time.Second)
	srv := httptest.NewServer(Handler(m))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHealthzDegraded(t *testing.T) {
	m := New()
	m.Update([]model.Report{{DeviceName: "bad", Error: "timeout"}}, time.Second)
	srv := httptest.NewServer(Handler(m))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
