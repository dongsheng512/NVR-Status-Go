package scan

import (
	"net/http/httptest"
	"sync"
	"testing"

	"nvrstatus/internal/model"
)

// TestQueueConcurrentSoak runs many rounds of 4-device parallel scans against
// fake NVRs. It is the local stand-in for the "4 devices, 30 rounds" long run
// in the review: it exercises the shared progress aggregator, the per-device
// worker pool and the result slicing, and is meant to be run under -race.
func TestQueueConcurrentSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过并发长跑")
	}
	const (
		deviceCount = 4
		rounds      = 20
	)

	devs := make([]model.Device, 0, deviceCount)
	servers := make([]*httptest.Server, 0, deviceCount)
	for i := 0; i < deviceCount; i++ {
		srv := fakeNVR(t)
		servers = append(servers, srv)
		devs = append(devs, serverDevice(t, srv))
	}
	defer func() {
		for _, srv := range servers {
			srv.Close()
		}
	}()

	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.DeviceWorkers = deviceCount
	opt.Workers = 2

	for round := 0; round < rounds; round++ {
		var mu sync.Mutex
		var max float64
		bad := ""
		reports := r.Queue(devs, opt, func(p model.Progress) {
			mu.Lock()
			defer mu.Unlock()
			if p.Overall < 0 || p.Overall > 1 {
				bad = "聚合进度越界"
			}
			if p.Overall > max {
				max = p.Overall
			}
		}, nil)

		if len(reports) != deviceCount {
			t.Fatalf("第 %d 轮报告数 = %d, 期望 %d", round+1, len(reports), deviceCount)
		}
		for i, rep := range reports {
			if rep.Error != "" {
				t.Fatalf("第 %d 轮第 %d 台失败: %s", round+1, i+1, rep.Error)
			}
			if rep.DeviceName != "NVR-T" {
				t.Fatalf("第 %d 轮第 %d 台设备名 = %q", round+1, i+1, rep.DeviceName)
			}
		}
		if bad != "" {
			t.Fatalf("第 %d 轮：%s", round+1, bad)
		}
		if max < 0.99 {
			t.Fatalf("第 %d 轮聚合进度最大只有 %.3f，应到 1", round+1, max)
		}
	}
}
