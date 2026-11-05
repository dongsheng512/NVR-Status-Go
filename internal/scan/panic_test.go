package scan

import (
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

// Callbacks come from the caller (GUI progress, streaming results). A panic in
// one used to kill the whole process because the worker goroutines had no
// recover. These cases also cover workers=1, where an unbuffered jobs channel
// would deadlock the producer if a worker ever stopped consuming.
func TestQueueConcurrentSurvivesCallbackPanic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		workers int
		inject  func(string) bool
		point   string
		wantErr string
	}{
		{"onDevice panic, two workers", 2, func(string) bool { return true }, "device", "回调异常"},
		{"onNVR panic, one worker", 1, func(string) bool { return true }, "nvr", "巡检内部错误"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv1 := fakeNVR(t)
			defer srv1.Close()
			srv2 := fakeNVR(t)
			defer srv2.Close()
			d1 := serverDevice(t, srv1)
			d1.Name = "NVR-A"
			d2 := serverDevice(t, srv2)
			d2.Name = "NVR-B"

			r := NewRunner(Options{Quiet: true, Tools: noTools})
			opt := model.DefaultScanOptions()
			opt.NoSearch = true
			opt.DeviceWorkers = tc.workers

			var onDevice func(model.Report)
			var onNVR func(*NVR)
			switch tc.point {
			case "device":
				onDevice = func(rep model.Report) {
					if rep.DeviceName == "NVR-A" && tc.inject(rep.DeviceName) {
						panic("injected onDevice panic")
					}
				}
			case "nvr":
				onNVR = func(n *NVR) {
					if n.Device.Name == "NVR-A" && tc.inject(n.Device.Name) {
						panic("injected onNVR panic")
					}
				}
			}

			reports := r.QueueWith([]model.Device{d1, d2}, opt, nil, onDevice, onNVR)
			if len(reports) != 2 {
				t.Fatalf("reports = %d, want 2", len(reports))
			}
			if reports[0].DeviceName != "NVR-A" || reports[1].DeviceName != "NVR-B" {
				t.Fatalf("order = %q, %q", reports[0].DeviceName, reports[1].DeviceName)
			}
			if !strings.Contains(reports[0].Error, tc.wantErr) {
				t.Fatalf("NVR-A error = %q, want containing %q", reports[0].Error, tc.wantErr)
			}
			if reports[1].Error != "" {
				t.Fatalf("NVR-B error = %q, want empty (other device must still finish)", reports[1].Error)
			}
		})
	}
}
