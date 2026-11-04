package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

// blackHoleNVR answers the Digest challenge once and then blocks forever on the
// authenticated request, mimicking a device that accepts TCP but never replies.
func blackHoleNVR(t *testing.T) (srv *httptest.Server, reached <-chan struct{}) {
	t.Helper()
	hit := make(chan struct{}, 1)
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", nonce="abc123", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		select {
		case hit <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv, hit
}

// TestQueueCtxAbortsInFlightRequest is the regression test for "Ctrl-C leaves
// the scan (and ffmpeg) running": cancelling the context must abort a request
// that is already on the wire instead of waiting for the device to answer.
func TestQueueCtxAbortsInFlightRequest(t *testing.T) {
	srv, reached := blackHoleNVR(t)
	dev := serverDevice(t, srv)

	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.NoSearch = true
	opt.DeviceWorkers = 1

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan []model.Report, 1)
	go func() {
		done <- r.QueueCtx(ctx, []model.Device{dev}, opt, nil, nil)
	}()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("请求没有到达假设备，测试前提不成立")
	}

	cancel()

	select {
	case reps := <-done:
		if len(reps) != 1 {
			t.Fatalf("报告数 = %d, 期望 1", len(reps))
		}
		if reps[0].Error == "" {
			t.Fatalf("取消后仍报告成功: %+v", reps[0])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后队列仍未退出：在途请求没有被中断")
	}
}

// TestQueueCtxWithoutCancelStillScans is the positive control: the same setup
// must produce a clean report when nobody cancels, so the test above cannot
// pass just because the queue always fails.
func TestQueueCtxWithoutCancelStillScans(t *testing.T) {
	srv := fakeNVR(t)
	defer srv.Close()

	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.Workers = 2

	reps := r.QueueCtx(context.Background(), []model.Device{serverDevice(t, srv)}, opt, nil, nil)
	if len(reps) != 1 {
		t.Fatalf("报告数 = %d, 期望 1", len(reps))
	}
	if reps[0].Error != "" {
		t.Fatalf("未取消时应巡检成功: %s", reps[0].Error)
	}
	if reps[0].Info["型号"] != "DS-TEST" {
		t.Fatalf("型号 = %q", reps[0].Info["型号"])
	}
}

// TestRunCtxCancelledFailsFast covers the single-device entry point.
func TestRunCtxCancelledFailsFast(t *testing.T) {
	srv := fakeNVR(t)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := NewRunner(Options{Quiet: true, Tools: noTools})
	rep := r.RunCtx(ctx, serverDevice(t, srv), model.DefaultScanOptions(), "NVR-T", nil)
	if rep.Error == "" {
		t.Fatal("ctx 已取消时应返回错误报告")
	}
}

// TestQueueCtxCancelWithConcurrentDevices: 4 台设备并行时取消，必须同时打断所有
// 在途请求，而不是等某台设备超时。真实场景就是 4 台 NVR 一起巡检、用户按了取消。
func TestQueueCtxCancelWithConcurrentDevices(t *testing.T) {
	const devices = 4
	devs := make([]model.Device, 0, devices)
	reached := make([]<-chan struct{}, 0, devices)
	for i := 0; i < devices; i++ {
		srv, hit := blackHoleNVR(t)
		devs = append(devs, serverDevice(t, srv))
		reached = append(reached, hit)
	}

	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.NoSearch = true
	opt.DeviceWorkers = devices

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan []model.Report, 1)
	go func() { done <- r.QueueCtx(ctx, devs, opt, nil, nil) }()

	// 等到至少两台设备都已发出请求，确保取消发生在并发在途状态。
	started := 0
	deadline := time.After(5 * time.Second)
	for started < 2 {
		select {
		case <-reached[started]:
			started++
		case <-deadline:
			t.Fatalf("只有 %d 台设备发出请求，测试前提不成立", started)
		}
	}

	cancel()

	select {
	case reps := <-done:
		if len(reps) != devices {
			t.Fatalf("报告数 = %d, 期望 %d", len(reps), devices)
		}
		for i, rep := range reps {
			if rep.Error == "" {
				t.Fatalf("第 %d 台取消后仍报告成功: %+v", i+1, rep)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("并发取消后队列未退出：仍有请求挂在设备上")
	}
}
