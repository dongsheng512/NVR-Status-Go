package report

import (
	"bytes"
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

func sampleReport() model.Report {
	yes := true
	no := false
	return model.Report{
		DeviceName: "NVR-A",
		IP:         "192.0.2.10",
		Info:       map[string]string{"型号": "DS-7608", "固件版本": "4.0", "设备名称": "NVR-A"},
		SysStatus:  map[string]string{"运行时长": "3 天", "内存使用率": "40%"},
		Health: model.HealthSummary{
			Status:   model.StatusWarn,
			Warnings: []string{"通道 3 近期无录像"},
			Stats: model.HealthStats{
				ChannelTotal: 2, Online: 1, Offline: 1, CameraTotal: 2,
				PlanConfigured: 2, RecordingOK: 1, RecordingBad: 1,
				DiskOK: 1, DiskChecked: true,
			},
		},
		Cameras: []model.Camera{{ID: "1", Name: "前门", Online: "true"}, {ID: "2", Name: "后门", Online: "false"}},
		Records: []model.Record{
			{Channel: "1", Name: "前门", Online: "true", RecordingEnabled: &yes, HasAudio: &yes,
				RecordingMode: "连续录像", DiskStatus: model.StatusOK, DiskDetail: "近期有录像", RecordingOK: model.StatusOK},
			{Channel: "2", Name: "后门", Online: "false", RecordingEnabled: &yes, HasAudio: &no,
				RecordingMode: "移动侦测", DiskStatus: model.StatusBad, DiskDetail: "近60分钟无录像片段", RecordingOK: model.StatusBad},
		},
		Drives:          []model.Drive{{Name: "hdd1", CapacityTB: "10.0", UsedTB: "9.8", FreeTB: "0.2", Usage: "98.0%", Status: "ok"}},
		DiskOverwrite:   model.DiskOverwrite{Label: "已开启"},
		LookbackMinutes: 60,
		AVSeconds:       6,
		AVWorkers:       2,
	}
}

func TestRenderDevice(t *testing.T) {
	var buf bytes.Buffer
	r := sampleReport()
	RenderDevice(&buf, &r, true)
	out := buf.String()
	for _, want := range []string{"NVR-A", "警告", "DS-7608", "前门", "后门", "近期无录像", "98.0%", "循环覆盖: 已开启"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestRenderSite(t *testing.T) {
	var buf bytes.Buffer
	reports := []model.Report{sampleReport()}
	RenderAll(&buf, reports, false)
	out := buf.String()
	for _, want := range []string{"NVR 站点巡检", "1/1 台成功", "完成: 1/1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestRenderErrorReport(t *testing.T) {
	var buf bytes.Buffer
	r := model.Report{DeviceName: "bad", Error: "无法获取设备信息"}
	RenderDevice(&buf, &r, false)
	if !strings.Contains(buf.String(), "连接失败") || !strings.Contains(buf.String(), "无法获取设备信息") {
		t.Fatalf("unexpected: %s", buf.String())
	}
}

func TestRenderDiskUsageUnknownIsNotGreen(t *testing.T) {
	var buf bytes.Buffer
	r := sampleReport()
	r.Health.Stats.DiskUsageUnknown = 1
	r.Drives[0].Usage = "未知"
	RenderDevice(&buf, &r, false)
	out := buf.String()
	if !strings.Contains(out, "使用率未确认 1 块") {
		t.Fatalf("missing unknown usage note:\n%s", out)
	}
	if strings.Contains(out, "硬盘\t正常") {
		t.Fatalf("disk row says 正常 while usage is unknown:\n%s", out)
	}
}
