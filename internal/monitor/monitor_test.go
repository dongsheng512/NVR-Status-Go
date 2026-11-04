package monitor

import (
	"io"
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

// TestMetricsEscapesLabels: the exposition format only allows \\, \" and \n in
// label values. Go's %q would emit \t / \x00 for other control characters,
// which makes the whole scrape fail — so a hostile device name must never leak
// a raw newline or a non-Prometheus escape.
func TestMetricsEscapesLabels(t *testing.T) {
	m := New()
	m.Update([]model.Report{{
		DeviceName: "NVR\"A\\B\n第二行",
		IP:         "192.0.2.10",
	}}, time.Second)
	out := m.Snapshot().Metrics()

	want := `nvr_up{device="NVR\"A\\B\n第二行",ip="192.0.2.10"} 1`
	if !strings.Contains(out, want) {
		t.Fatalf("转义不正确，缺少:\n  %s\n实际输出:\n%s", want, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		// 每条样本必须是单行：换行若未被转义会在这里暴露。
		if strings.Count(line, "nvr_") != 1 {
			t.Fatalf("样本行包含未转义的换行: %q", line)
		}
	}
	if strings.Contains(out, `\t`) || strings.Contains(out, `\x`) {
		t.Fatalf("输出含 Prometheus 不接受的转义序列:\n%s", out)
	}
}

// TestMetricsUnknownDiskUsage: an unparsable usage must not be reported as 0%
// (that reads as "empty disk"); it gets its own unknown series instead.
func TestMetricsUnknownDiskUsage(t *testing.T) {
	m := New()
	m.Update([]model.Report{{
		DeviceName: "NVR-A", IP: "192.0.2.10",
		Drives: []model.Drive{
			{Name: "hdd1", Usage: "98.0%"},
			{Name: "hdd2", Usage: ""},
			{Name: "hdd3", Usage: "未知"},
			{Name: "hdd4", Usage: " 45 % "},
		},
	}}, time.Second)
	out := m.Snapshot().Metrics()

	if !strings.Contains(out, `nvr_disk_usage_percent{device="NVR-A",ip="192.0.2.10",drive="hdd1"} 98.0`) {
		t.Fatalf("hdd1 缺失:\n%s", out)
	}
	if !strings.Contains(out, `nvr_disk_usage_percent{device="NVR-A",ip="192.0.2.10",drive="hdd4"} 45.0`) {
		t.Fatalf("hdd4（带空格与百分号）应能解析:\n%s", out)
	}
	for _, bad := range []string{"hdd2", "hdd3"} {
		if strings.Contains(out, `nvr_disk_usage_percent{device="NVR-A",ip="192.0.2.10",drive="`+bad+`"}`) {
			t.Fatalf("%s 的未知使用率不应出现在 percent 序列:\n%s", bad, out)
		}
		if !strings.Contains(out, `nvr_disk_usage_unknown{device="NVR-A",ip="192.0.2.10",drive="`+bad+`"} 1`) {
			t.Fatalf("%s 应出现在 unknown 序列:\n%s", bad, out)
		}
	}
}

func TestParsePercent(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"98.0%", 98, true},
		{" 45 % ", 45, true},
		{"0", 0, true},
		{"", 0, false},
		{"未知", 0, false},
		{"-", 0, false},
	}
	for _, tc := range cases {
		got, ok := parsePercent(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parsePercent(%q) = (%v, %v), 期望 (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
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

// TestHandlerRequiresToken: with a token configured, every endpoint that
// carries device data must reject anonymous requests.
func TestHandlerRequiresToken(t *testing.T) {
	m := New()
	m.Update(sampleReports(), time.Second)
	srv := httptest.NewServer(HandlerWith(m, Options{Token: "s3cr3t"}))
	defer srv.Close()

	for _, path := range []string{"/healthz", "/metrics", "/api/reports"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 无令牌时 status = %d, 期望 401", path, resp.StatusCode)
		}
		if strings.Contains(string(body), "NVR-A") {
			t.Fatalf("%s 未授权仍泄漏了报告内容: %s", path, body)
		}
		if got := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
			t.Fatalf("%s WWW-Authenticate = %q", path, got)
		}

		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer s3cr3t")
		resp2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp2.Body.Close()
		if resp2.StatusCode == http.StatusUnauthorized {
			t.Fatalf("%s 带正确令牌仍被拒绝", path)
		}

		req3, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req3.Header.Set("Authorization", "Bearer wrong")
		resp3, err := http.DefaultClient.Do(req3)
		if err != nil {
			t.Fatal(err)
		}
		resp3.Body.Close()
		if resp3.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 错误令牌 status = %d, 期望 401", path, resp3.StatusCode)
		}
	}
}

// TestHandlerWithoutTokenStaysOpen is the loopback default: no token, no auth.
func TestHandlerWithoutTokenStaysOpen(t *testing.T) {
	m := New()
	m.Update(sampleReports(), time.Second)
	srv := httptest.NewServer(Handler(m))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
