package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"nvrstatus/internal/model"
	"nvrstatus/internal/monitor"
)

func quietAgent(mon *monitor.Monitor) *agentRun {
	return &agentRun{
		mon:     mon,
		devices: []model.Device{{IP: "192.0.2.11", Username: "admin"}},
		opts:    model.DefaultScanOptions(),
		name:    "示例档案",
		out:     io.Discard,
		errOut:  io.Discard,
	}
}

// TestScanOnceSendsWebhookBeforeReturning is the regression test for
// "--once --webhook exits before the POST goes out": the webhook must be
// dispatched synchronously, i.e. already done when scanOnce returns.
//
// GOMAXPROCS(1) makes the race deterministic: with a detached goroutine the
// counter is still 0 at the assertion point because the goroutine cannot run
// until the current goroutine yields.
func TestScanOnceSendsWebhookBeforeReturning(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	a := quietAgent(monitor.New())
	a.webhook = "http://192.0.2.99/hook" // never dialled: notify is injected
	calls := 0
	a.scan = func(context.Context) []model.Report {
		return []model.Report{{DeviceName: "NVR-A"}}
	}
	a.notify = func(monitor.Snapshot) { calls++ }

	if ok := a.scanOnce(context.Background()); !ok {
		t.Fatal("未取消时 scanOnce 应返回 true")
	}
	if calls != 1 {
		t.Fatalf("Webhook 调用次数 = %d, 期望 1（必须在返回前同步发送）", calls)
	}
}

// TestScanOnceSkipsMetricsAndWebhookOnCancel: a cancelled round must not
// publish half-finished results as if they were real.
func TestScanOnceSkipsMetricsAndWebhookOnCancel(t *testing.T) {
	mon := monitor.New()
	a := quietAgent(mon)
	a.webhook = "http://192.0.2.99/hook"
	calls := 0

	ctx, cancel := context.WithCancel(context.Background())
	a.scan = func(context.Context) []model.Report {
		cancel()
		return []model.Report{{Error: "scan cancelled"}}
	}
	a.notify = func(monitor.Snapshot) { calls++ }

	if ok := a.scanOnce(ctx); ok {
		t.Fatal("取消后 scanOnce 应返回 false")
	}
	if calls != 0 {
		t.Fatalf("取消后不应发送 Webhook，实际调用 %d 次", calls)
	}
	if snaps := mon.Snapshot(); len(snaps.Reports) != 0 {
		t.Fatalf("取消后不应更新指标，实际有 %d 条报告", len(snaps.Reports))
	}
}

// TestScanOnceWithoutWebhookDoesNotNotify: no webhook configured, no request.
func TestScanOnceWithoutWebhookDoesNotNotify(t *testing.T) {
	a := quietAgent(monitor.New())
	calls := 0
	a.scan = func(context.Context) []model.Report { return []model.Report{{DeviceName: "NVR-A"}} }
	a.notify = func(monitor.Snapshot) { calls++ }

	if ok := a.scanOnce(context.Background()); !ok {
		t.Fatal("未取消时 scanOnce 应返回 true")
	}
	if calls != 0 {
		t.Fatalf("未配置 Webhook 却发送了 %d 次", calls)
	}
}

// TestPostWebhookPayload checks the JSON contract and that the body is drained
// so the connection can be reused.
func TestPostWebhookPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("解析 JSON 失败: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	snap := monitor.Snapshot{
		ScannedAt: time.Unix(1700000000, 0).UTC(),
		Duration:  1.5,
		Reports:   []model.Report{{DeviceName: "NVR-A"}},
	}
	status, err := postWebhook(srv.URL, snap)
	if err != nil {
		t.Fatalf("postWebhook: %v", err)
	}
	if status != http.StatusNoContent {
		t.Fatalf("status = %d", status)
	}
	if got["healthy"] != true {
		t.Fatalf("healthy = %v, 期望 true", got["healthy"])
	}
	reports, ok := got["reports"].([]any)
	if !ok || len(reports) != 1 {
		t.Fatalf("reports = %#v", got["reports"])
	}
}
