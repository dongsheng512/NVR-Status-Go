package nvgui

import (
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"nvrstatus/internal/history"
	"nvrstatus/internal/model"
)

func TestScreenshot(t *testing.T) {
	if os.Getenv("SHOT") == "" {
		t.Skip("set SHOT=1 to render screenshots")
	}
	for _, tc := range []struct {
		name string
		w, h int
		set  func(*App)
	}{
		{"empty", 1280, 820, func(a *App) {}},
		{"settings", 1280, 820, func(a *App) { a.settingsOpen = true }},
		{"settings-small", 1000, 700, func(a *App) { a.settingsOpen = true; a.opt.DeepAVCheck = true; a.opt.AVSave = true }},
		{"report", 1280, 820, func(a *App) { a.seedDemoReport() }},
		{"report-small", 1000, 700, func(a *App) { a.seedDemoReport() }},
	} {
		app := newTestApp(t)
		app.seedDemoDevices()
		tc.set(app)
		tt := ui.NewTester(app.View, tc.w, tc.h)
		tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 70, Right: 0})
		f, err := os.Create("/tmp/nvgui-" + tc.name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func (a *App) seedDemoDevices() {
	a.devices = []model.Device{
		{Name: "NVR-C", IP: "192.0.2.11", Port: 80, Username: "admin"},
		{Name: "NVR-D", IP: "192.0.2.12", Port: 80, Username: "admin"},
		{Name: "NVR-A", IP: "192.0.2.13", Port: 80, Username: "admin"},
		{Name: "NVR-B", IP: "192.0.2.14", Port: 80, Username: "admin"},
	}
	a.lastHealth = map[string]string{
		"192.0.2.11": "严重", "192.0.2.12": "严重",
		"192.0.2.13": "警告", "192.0.2.14": "警告",
	}
	a.selectedDev = 2
	a.target = "NVR-A"
	a.rebuildTargets()
	if a.deviceList.Selected != nil {
		*a.deviceList.Selected = 2
	}
}

func (a *App) seedDemoReport() {
	yes, no := true, false
	records := []model.Record{
		{Channel: "1", Name: "前门", Online: "true", RecordingEnabled: &yes, RecordingMode: "连续录像", RecordingSchedule: "1/1", HasAudio: &yes, DiskStatus: model.StatusOK, DiskDetail: "近期有录像", VideoProbe: model.StatusOK, AudioProbe: model.StatusOK, RecordingOK: model.StatusOK, Resolution: "1920x1080"},
		{Channel: "2", Name: "后门", Online: "true", RecordingEnabled: &yes, RecordingMode: "移动侦测", RecordingSchedule: "1/1", HasAudio: &no, DiskStatus: model.StatusOK, DiskDetail: "近期有录像", VideoProbe: model.StatusOK, AudioProbe: model.StatusSkip, RecordingOK: model.StatusOK},
		{Channel: "3", Name: "仓库", Online: "false", RecordingEnabled: &yes, RecordingMode: "连续录像", RecordingSchedule: "1/1", HasAudio: &yes, DiskStatus: model.StatusBad, DiskDetail: "近60分钟无录像片段", ProbeDetail: "视频异常 / 音频未确认", VideoProbe: model.StatusBad, AudioProbe: model.StatusUnknown, RecordingOK: model.StatusBad},
		{Channel: "4", Name: "停车场", Online: "true", RecordingEnabled: &yes, RecordingMode: "连续录像", RecordingSchedule: "1/1", HasAudio: &yes, DiskStatus: model.StatusOK, DiskDetail: "近期有录像", VideoProbe: model.StatusOK, AudioProbe: model.StatusWarn, ProbeDetail: "疑似静音(mean -85.0dB)", RecordingOK: model.StatusOK},
	}
	a.diff = &history.Diff{HealthBefore: "良好", HealthAfter: "警告",
		NewIssues: []string{"通道 3 仓库: 录像 异常"}, Resolved: []string{"通道 4 停车场 音频抽检 警告"}}
	a.report = &model.Report{DeviceName: "NVR-A", IP: "192.0.2.13",
		Info: map[string]string{"型号": "DS-7608", "固件版本": "4.0"},
		Health: model.HealthSummary{Status: model.StatusWarn,
			Warnings: []string{"通道 3 近期无录像", "通道 3 视频抽检异常", "通道 4 音频疑似静音"},
			Stats: model.HealthStats{ChannelTotal: 4, CameraTotal: 4, Online: 3, Offline: 1,
				PlanConfigured: 4, RecordingOK: 3, RecordingBad: 1, HasAudio: 3, NoAudio: 1,
				DiskOK: 3, DiskBad: 1, DiskChecked: true, RecordingChecked: true,
				VideoProbeOK: 3, VideoProbeBad: 1, AudioProbeOK: 2, AudioProbeWarn: 1, AudioProbeUnknown: 1}},
		Records:       records,
		Drives:        []model.Drive{{Name: "hdd1", Type: "SATA", Property: "RW", CapacityTB: "10.0", UsedTB: "9.8", FreeTB: "0.2", Usage: "98.0%", Status: "ok"}},
		DiskOverwrite: model.DiskOverwrite{Label: "已开启"}, DeepAVCheck: true, AVSeconds: 6, AVWorkers: 2,
	}
}
