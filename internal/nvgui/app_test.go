package nvgui

import (
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"nvrstatus/internal/history"
	"nvrstatus/internal/model"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("NVRSTATUS_DATA_DIR", t.TempDir())
	return New()
}

func chooseTarget(t *testing.T, app *App, tt *ui.Tester, item string) {
	t.Helper()
	if err := tt.Click(app.currentTargetLabel()); err != nil {
		t.Fatalf("open device menu: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem(item); err != nil {
		t.Fatalf("choose %q (menu=%v): %v", item, tt.Menu(), err)
	}
	tt.Frame()
}

func TestEmptyState(t *testing.T) {
	app := newTestApp(t)
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("NVR Status") {
		t.Fatal("missing title")
	}
	if !tt.HasText("还没有巡检结果") {
		t.Fatal("missing empty state")
	}
}

func TestTableShowsRecords(t *testing.T) {
	yes := true
	app := newTestApp(t)
	app.report = &model.Report{
		DeviceName: "NVR-A",
		Health: model.HealthSummary{Status: model.StatusWarn, Stats: model.HealthStats{
			ChannelTotal: 1, Online: 1, CameraTotal: 1, RecordingOK: 1,
		}},
		Records: []model.Record{{
			Channel: "1", Name: "前门", Online: "true", RecordingEnabled: &yes,
			RecordingMode: "连续录像", DiskStatus: model.StatusOK, RecordingOK: model.StatusOK,
			VideoProbe: model.StatusOK, AudioProbe: model.StatusOK,
		}},
	}
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("前门") {
		t.Fatalf("table missing record name; texts=%v", tt.Texts())
	}
	if !tt.HasText("整体状态") {
		t.Fatalf("missing metrics; texts=%v", tt.Texts())
	}
}

func TestAddDevicePersists(t *testing.T) {
	app := newTestApp(t)
	before := len(app.devices)
	app.beginAddDevice()
	app.draftName = "NVR-Test"
	app.draftIP = "192.0.2.50"
	app.draftPort = "8000"
	app.draftUser = "admin"
	app.draftPass = "secret"
	app.commitDeviceForm()
	if len(app.devices) != before+1 {
		t.Fatalf("devices = %d", len(app.devices))
	}
	last := app.devices[len(app.devices)-1]
	if last.Name != "NVR-Test" || last.IP != "192.0.2.50" || last.Port != 8000 {
		t.Fatalf("device = %+v", last)
	}
}

// TestNoSearchOptionIsAppliedAndPersisted guards the "跳过落盘检索" checkbox.
// It is bound to a.opt; if options() forgets to copy it, both the scan and the
// saved profile silently ignore the setting.
func TestNoSearchOptionIsAppliedAndPersisted(t *testing.T) {
	app := newTestApp(t)
	if app.profile == "" {
		t.Fatal("no active profile")
	}
	app.opt.NoSearch = true
	if !app.options().NoSearch {
		t.Fatal("options() dropped NoSearch: scan would still run CMSearch")
	}
	app.saveProfile()
	prof, err := app.store.GetProfile(app.profile)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if !prof.ScanOptions.NoSearch {
		t.Fatal("saved profile dropped NoSearch: setting is lost on restart")
	}

	// 反向：取消勾选也必须能写回，避免只测了单向。
	app.opt.NoSearch = false
	app.saveProfile()
	prof, err = app.store.GetProfile(app.profile)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if prof.ScanOptions.NoSearch {
		t.Fatal("saved profile kept NoSearch after unchecking")
	}
}

func TestProfileCreateAndSwitch(t *testing.T) {
	app := newTestApp(t)
	app.beginNewProfile()
	app.profileDraft = "测试档案"
	app.commitProfileDialog()
	if app.profile != "测试档案" {
		t.Fatalf("profile = %q", app.profile)
	}
	if len(app.profiles) < 2 {
		t.Fatalf("profiles = %v", app.profiles)
	}
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("NVR Status") {
		t.Fatal("view did not render after profile create")
	}
}

func TestHistoryEmptyState(t *testing.T) {
	app := newTestApp(t)
	app.openHistory()
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("历史报告") {
		t.Fatalf("missing history dialog; texts=%v", tt.Texts())
	}
	if !tt.HasText("暂无历史报告") {
		t.Fatalf("missing empty history; texts=%v", tt.Texts())
	}
}

func TestThemeValuesRender(t *testing.T) {
	for _, mode := range []string{"跟随系统", "浅色", "深色"} {
		app := newTestApp(t)
		app.themeMode = mode
		tt := ui.NewTester(app.View, 1280, 820)
		if !tt.HasText("NVR Status") {
			t.Fatalf("%s: view failed", mode)
		}
	}
}

func TestSettingsPanelRenders(t *testing.T) {
	app := newTestApp(t)
	app.settingsOpen = true
	app.opt.DeepAVCheck = true
	app.opt.AVSave = true
	tt := ui.NewTester(app.View, 1000, 700)
	if !tt.HasText("扫描设置") {
		t.Fatal("missing settings toggle")
	}
	if !tt.HasText("回溯窗口") {
		t.Fatalf("missing settings form; texts=%v", tt.Texts())
	}
}

func TestDeviceFormRenders(t *testing.T) {
	app := newTestApp(t)
	app.beginAddDevice()
	app.draftIP = "192.0.2.99"
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("添加设备") || !tt.HasText("IP 地址") {
		t.Fatalf("device form missing; texts=%v", tt.Texts())
	}
}

func TestDetailModalRenders(t *testing.T) {
	app := newTestApp(t)
	yes := true
	rec := model.Record{Channel: "1", Name: "前门", Online: "true", RecordingEnabled: &yes,
		RecordingMode: "连续录像", DiskStatus: model.StatusOK, RecordingOK: model.StatusOK,
		VideoProbe: model.StatusOK, AudioProbe: model.StatusOK, ProbeDetail: "视频正常 / 音频正常",
		Resolution: "1920x1080", VideoCodec: "h264", AudioCodec: "aac"}
	app.report = &model.Report{DeviceName: "NVR-A", Records: []model.Record{rec},
		Health: model.HealthSummary{Status: model.StatusOK, Stats: model.HealthStats{ChannelTotal: 1, Online: 1, CameraTotal: 1, RecordingOK: 1}}}
	app.openDetail(0)
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("通道 1 · 前门") || !tt.HasText("1920x1080") {
		t.Fatalf("detail missing; texts=%v", tt.Texts())
	}
}

func TestHistoryListsSavedReport(t *testing.T) {
	app := newTestApp(t)
	yes := true
	rep := model.Report{DeviceName: "NVR-H", IP: "192.0.2.77",
		Health:  model.HealthSummary{Status: model.StatusOK, Stats: model.HealthStats{ChannelTotal: 1, CameraTotal: 1, Online: 1}},
		Records: []model.Record{{Channel: "1", Name: "前门", RecordingEnabled: &yes, RecordingOK: model.StatusOK}}}
	if _, err := history.SaveReport(rep); err != nil {
		t.Fatal(err)
	}
	app.openHistory()
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("NVR-H") {
		t.Fatalf("history entry missing; texts=%v", tt.Texts())
	}
	if !tt.HasText("历史报告") {
		t.Fatal("history dialog missing")
	}
}

func TestClickSettingsToggle(t *testing.T) {
	app := newTestApp(t)
	tt := ui.NewTester(app.View, 1280, 820)
	if err := tt.Click(app.settingsButtonLabel()); err != nil {
		t.Fatalf("click: %v", err)
	}
	tt.Frame()
	if !app.settingsOpen {
		t.Fatal("settings did not open")
	}
	if !tt.HasText("回溯窗口") {
		t.Fatalf("settings missing; texts=%v", tt.Texts())
	}
}

func TestClickAddDevice(t *testing.T) {
	app := newTestApp(t)
	tt := ui.NewTester(app.View, 1280, 820)
	if err := tt.Click("添加"); err != nil {
		t.Fatalf("click: %v", err)
	}
	tt.Frame()
	if !app.deviceForm {
		t.Fatal("device form did not open")
	}
	if !tt.HasText("添加设备") {
		t.Fatalf("modal missing; texts=%v", tt.Texts())
	}
}

func TestClickStartScan(t *testing.T) {
	srv := fakeNVR(t)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	app := newTestApp(t)
	app.devices = []model.Device{{Name: "NVR-GUI", IP: u.Hostname(), Port: port, Username: "admin", Password: "pw"}}
	app.target = "NVR-GUI"
	app.opt = model.DefaultScanOptions()
	app.syncFormFromOptions()
	tt := ui.NewTester(app.View, 1280, 820)
	if err := tt.Click("开始巡检"); err != nil {
		t.Fatalf("click: %v", err)
	}
	done := app.ScanDone()
	if done == nil {
		t.Fatal("scan did not start")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scan timed out")
	}
	if app.report == nil || app.report.Error != "" {
		t.Fatalf("scan failed: %+v logs=%v", app.report, app.logs)
	}
}

func TestClickDeviceRowSelectsTarget(t *testing.T) {
	app := newTestApp(t)
	app.devices = append(app.devices, model.Device{Name: "NVR2", IP: "192.0.2.2", Port: 80, Username: "admin"})
	app.rebuildTargets()
	tt := ui.NewTester(app.View, 1280, 820)
	chooseTarget(t, app, tt, "NVR2  ·  192.0.2.2")
	if app.target != "NVR2" || app.selectedDev != 1 {
		t.Fatalf("target=%q selectedDev=%d", app.target, app.selectedDev)
	}
	tt.Frame()
	if err := tt.Click("管理设备"); err != nil {
		t.Fatalf("manage click: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("编辑当前设备…"); err != nil {
		t.Fatalf("edit menu: %v", err)
	}
	tt.Frame()
	if !app.deviceForm {
		t.Fatal("edit form did not open")
	}
}

func TestDiffSectionRenders(t *testing.T) {
	app := newTestApp(t)
	app.seedDemoReport()
	app.diff = &history.Diff{HealthBefore: "良好", HealthAfter: "警告",
		NewIssues: []string{"通道 3 仓库: 录像 异常"}}
	tt := ui.NewTester(app.View, 1280, 820)
	if !tt.HasText("与上次相比") || !tt.HasText("新增异常 1") {
		t.Fatalf("diff missing; texts=%v", tt.Texts())
	}
}

func TestClickTableRowSelects(t *testing.T) {
	app := newTestApp(t)
	app.seedDemoReport()
	tt := ui.NewTester(app.View, 1280, 820)
	if err := tt.Click("前门"); err != nil {
		t.Fatalf("click: %v", err)
	}
	tt.Frame()
	if app.selectedRow != 0 {
		t.Fatalf("selectedRow = %d, want 0", app.selectedRow)
	}
}

func TestClickHistoryRowSelects(t *testing.T) {
	app := newTestApp(t)
	rep := model.Report{DeviceName: "NVR-H2", IP: "192.0.2.78",
		Health:  model.HealthSummary{Status: model.StatusOK, Stats: model.HealthStats{ChannelTotal: 1}},
		Records: []model.Record{{Channel: "1", Name: "前门"}}}
	if _, err := history.SaveReport(rep); err != nil {
		t.Fatal(err)
	}
	app.openHistory()
	tt := ui.NewTester(app.View, 1280, 820)
	if err := tt.Click("NVR-H2"); err != nil {
		t.Fatalf("click: %v", err)
	}
	tt.Frame()
	if app.historySel != 0 {
		t.Fatalf("historySel = %d, want 0", app.historySel)
	}
}

func TestClickAllDevicesRow(t *testing.T) {
	app := newTestApp(t)
	tt := ui.NewTester(app.View, 1280, 820)
	chooseTarget(t, app, tt, "全部设备（1 台）")
	if app.target != allDevices || app.selectedDev != -1 {
		t.Fatalf("target=%q selectedDev=%d", app.target, app.selectedDev)
	}
}

func TestSelectDeviceAfterAllMode(t *testing.T) {
	app := newTestApp(t)
	tt := ui.NewTester(app.View, 1280, 820)
	chooseTarget(t, app, tt, "全部设备（1 台）")
	if app.target != allDevices {
		t.Fatalf("target after all = %q", app.target)
	}
	chooseTarget(t, app, tt, "NVR1  ·  192.168.1.100")
	if app.target != "NVR1" || app.selectedDev != 0 {
		t.Fatalf("target=%q selectedDev=%d", app.target, app.selectedDev)
	}
}

func TestDeviceTargetSticksAcrossFrames(t *testing.T) {
	app := newTestApp(t)
	app.devices = append(app.devices, model.Device{Name: "NVR2", IP: "192.0.2.2", Port: 80, Username: "admin"})
	app.rebuildTargets()
	tt := ui.NewTester(app.View, 1280, 820)
	chooseTarget(t, app, tt, "NVR2  ·  192.0.2.2")
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if app.target != "NVR2" || app.selectedDev != 1 {
		t.Fatalf("target=%q selectedDev=%d", app.target, app.selectedDev)
	}
}

func TestQuickDeepToggle(t *testing.T) {
	app := newTestApp(t)
	tt := ui.NewTester(app.View, 1280, 820)
	if err := tt.Click("深度抽检"); err != nil {
		t.Fatalf("click deep: %v", err)
	}
	tt.Frame()
	if !app.opt.DeepAVCheck {
		t.Fatal("deep check did not turn on")
	}
	tt.Frame()
	if err := tt.Click("深度抽检"); err != nil {
		t.Fatalf("click deep off: %v", err)
	}
	tt.Frame()
	if app.opt.DeepAVCheck {
		t.Fatal("deep check did not turn off")
	}
}

// TestScanDiffUsesPreviousArchive: 「与上次相比」必须拿上一份归档做基准，而不是
// 巡检刚写进去的这一份。后者会让每次巡检都显示「无变化」——历史对比等于失效。
// 这个用例同时覆盖「对比在后台算完并通过 post 发布」这条路径。
func TestScanDiffUsesPreviousArchive(t *testing.T) {
	srv := fakeNVR(t)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	ip := u.Hostname()

	app := newTestApp(t)
	if _, err := history.SaveReport(model.Report{
		DeviceName: "NVR-GUI",
		IP:         ip,
		Health:     model.HealthSummary{Status: "严重"},
	}); err != nil {
		t.Fatal(err)
	}

	app.devices = []model.Device{{Name: "NVR-GUI", IP: ip, Port: port, Username: "admin", Password: "pw"}}
	app.target = "NVR-GUI"
	app.opt = model.DefaultScanOptions()
	app.syncFormFromOptions()

	tt := ui.NewTester(app.View, 1280, 820)
	if err := tt.Click("开始巡检"); err != nil {
		t.Fatalf("click: %v", err)
	}
	done := app.ScanDone()
	if done == nil {
		t.Fatal("scan did not start")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scan timed out")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if d := app.DiffFor(ip); d != nil {
			if d.HealthBefore != "严重" {
				t.Fatalf("对比基准应为归档的上一份（严重），实际 HealthBefore=%q", d.HealthBefore)
			}
			if d.HealthAfter == "" {
				t.Fatal("HealthAfter 为空")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("后台历史对比没有发布结果")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
