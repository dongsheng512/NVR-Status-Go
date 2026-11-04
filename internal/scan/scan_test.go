package scan

import (
	"testing"

	"nvrstatus/internal/model"
)

func testDevice() model.Device {
	return model.Device{Name: "NVR-A", IP: "192.0.2.10", Port: 80, Username: "admin", Password: "x"}
}

func fakeTools() map[string]string {
	return map[string]string{"ffmpeg": "/bin/true", "ffprobe": "/bin/true"}
}

func noTools() map[string]string {
	return map[string]string{"ffmpeg": "", "ffprobe": ""}
}

func TestBuildDegradesWithoutFFmpeg(t *testing.T) {
	r := NewRunner(Options{Quiet: true, Tools: noTools})
	opt := model.DefaultScanOptions()
	opt.DeepAVCheck = true
	opt.AVSave = true
	n := r.Build(testDevice(), opt)
	defer n.Client.Close()
	if n.Opt.DeepAVCheck || n.Opt.AVSave {
		t.Fatalf("deep/save should be disabled without ffmpeg: %+v", n.Opt)
	}
	if n.AV.FFmpeg != "" {
		t.Fatalf("ffmpeg = %q", n.AV.FFmpeg)
	}
}

func TestBuildExpandsLookbackForAVAtDay(t *testing.T) {
	r := NewRunner(Options{Quiet: true, Tools: fakeTools, DefaultSaveRoot: "/tmp/av"})
	opt := model.DefaultScanOptions()
	opt.DeepAVCheck = true
	opt.BusyDaysAgo = 1
	opt.Lookback = 60
	n := r.Build(testDevice(), opt)
	defer n.Client.Close()
	if n.Opt.Lookback != 2*24*60 {
		t.Fatalf("lookback = %d, want 2880", n.Opt.Lookback)
	}
	if n.Recording.LookbackMinutes != n.Opt.Lookback {
		t.Fatalf("recording lookback = %d", n.Recording.LookbackMinutes)
	}
	if n.AV.AVSaveRoot != "/tmp/av" {
		t.Fatalf("save root = %q", n.AV.AVSaveRoot)
	}
}

func TestBuildClampsAndDefaults(t *testing.T) {
	r := NewRunner(Options{Quiet: true, Tools: fakeTools})
	opt := model.DefaultScanOptions()
	opt.DeepAVCheck = true
	opt.AVSeconds = 99
	opt.AVWorkers = 99
	opt.Workers = 99
	opt.BusyStart = 18
	opt.BusyEnd = 10
	n := r.Build(testDevice(), opt)
	defer n.Client.Close()
	if n.Opt.AVSeconds != 12 || n.Opt.AVWorkers != 3 {
		t.Fatalf("av clamps = %d/%d", n.Opt.AVSeconds, n.Opt.AVWorkers)
	}
	// Default DeviceWorkers=2 divides the 16-search budget across two devices.
	if n.Recording.SearchWorkers != 8 {
		t.Fatalf("search workers = %d, want 8", n.Recording.SearchWorkers)
	}
	if n.Recording.BusyStartHour != 10 || n.Recording.BusyEndHour != 18 {
		t.Fatalf("busy hours = %d-%d", n.Recording.BusyStartHour, n.Recording.BusyEndHour)
	}
}

func TestErrorReportCarriesOptions(t *testing.T) {
	r := NewRunner(Options{Quiet: true, Tools: fakeTools})
	opt := model.DefaultScanOptions()
	opt.DeepAVCheck = true
	opt.AVSeconds = 5
	n := r.Build(testDevice(), opt)
	defer n.Client.Close()
	rep := errorReport(n, "NVR-A", "boom")
	if rep.Error != "boom" || rep.DeviceName != "NVR-A" || rep.AVSeconds != 5 {
		t.Fatalf("report = %+v", rep)
	}
}
