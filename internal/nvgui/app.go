package nvgui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nvrstatus/internal/config"
	"nvrstatus/internal/history"
	"nvrstatus/internal/model"
	"nvrstatus/internal/scan"
	"nvrstatus/internal/util"
)

// logLine is one line of the scan log.
type logLine struct {
	Text  string
	Level string // info | step | ok | warn | error
}

type profileDialogKind int

const (
	profileDialogNone profileDialogKind = iota
	profileDialogNew
	profileDialogSaveAs
	profileDialogRename
)

// numberFieldState is a numeric setting with its own text buffer and focus
// handle, used by the custom number field with in-field steppers.
type numberFieldState struct {
	Value  float64
	Text   string
	Handle ui.Handle
}

func (n *numberFieldState) set(v float64) {
	n.Value = v
	n.Text = formatSettingNumber(v, 1)
}

type confirmKind int

const (
	confirmNone confirmKind = iota
	confirmDeleteDevice
	confirmDeleteProfile
)

// Settings is the small UI state persisted beside profiles.json.
type Settings struct {
	Theme        string             `json:"theme"` // 跟随系统 | 浅色 | 深色
	SideWidth    float32            `json:"side_width"`
	ColumnsOrder []string           `json:"columns_order,omitempty"`
	ColumnsWidth map[string]float32 `json:"columns_widths,omitempty"`
	LastTarget   string             `json:"last_target,omitempty"`
}

// App is the window state; the view is rebuilt from it every frame.
type App struct {
	win     *mygo.Window
	stateMu sync.Mutex
	runner  *scan.Runner
	store   *config.Store

	startupErr string
	settings   Settings

	// profiles
	profiles      []string
	profile       string
	profileChoice string

	// devices
	devices       []model.Device
	target        string
	selectedDev   int
	deviceList    ui.ListState
	deviceForm    bool
	editingDevice int
	draftName     string
	draftIP       string
	draftPort     string
	draftUser     string
	draftPass     string
	draftSSL      bool
	deviceErr     string

	// scan form
	opt           model.ScanOptions
	lookbackValue numberFieldState
	lookbackUnit  string
	workers       numberFieldState
	deviceWorkers numberFieldState
	avSeconds     numberFieldState
	avWorkers     numberFieldState
	avLimit       numberFieldState
	silenceDB     numberFieldState
	busyStart     numberFieldState
	busyEnd       numberFieldState
	busyDaysAgo   numberFieldState
	saveRoot      string
	settingsOpen  bool

	// scanning
	scanning      bool
	singleCheck   bool
	cancelScan    context.CancelFunc
	currentNVR    *scan.NVR
	progress      float64
	progressLabel string
	scanDone      chan struct{}
	logs          []logLine
	logExpanded   bool
	logMu         sync.Mutex

	// results
	report       *model.Report
	reports      []model.Report
	resultDevice string
	diff         *history.Diff
	diffs        map[string]*history.Diff
	selectedRow  int
	table        ui.ListState
	onlyProblem  bool
	detailOpen   bool
	detailRecord model.Record
	warnExpanded bool

	// history
	lastHealth       map[string]string // device IP -> last scan verdict
	historyOpen      bool
	historyList      []history.ReportMeta
	historyListState ui.ListState
	historySel       int

	// profile dialog / confirmation
	profileDialog     profileDialogKind
	profileDraft      string
	profileErr        string
	confirm           confirmKind
	confirmText       string
	pendingDevice     int
	pendingProfile    string
	themeMode         string
	sideWidth         float32
	lastProgressNanos atomic.Int64
}

// New loads profiles and settings and returns the app state.
func New() *App {
	a := &App{
		selectedDev:      -1,
		editingDevice:    -1,
		selectedRow:      -1,
		historySel:       -1,
		themeMode:        "跟随系统",
		sideWidth:        340,
		logExpanded:      true,
		target:           allDevices,
		lastHealth:       map[string]string{},
		diffs:            map[string]*history.Diff{},
		table:            ui.ListState{Selected: new(int)},
		deviceList:       ui.ListState{Selected: new(int)},
		historyListState: ui.ListState{Selected: new(int)},
	}
	*a.table.Selected = -1
	*a.deviceList.Selected = -1
	*a.historyListState.Selected = -1
	a.loadSettings()
	a.store = config.New("")
	if err := a.store.Load(); err != nil {
		a.startupErr = "读取配置失败: " + err.Error()
	}
	a.reloadProfiles()
	name := ""
	if a.store != nil {
		name = a.store.ActiveName()
	}
	a.applyProfile(name)
	a.runner = scan.NewRunner(scan.Options{
		DefaultSaveRoot: filepath.Join(config.AppDataDir(), "av_samples"),
		Logger:          func(msg string) { a.logFromWorker(msg, "warn") },
	})
	return a
}

// SetWindow is called by main once the window exists.
func (a *App) SetWindow(w *mygo.Window) { a.win = w }

const allDevices = "全部设备"

// View builds one frame.
func (a *App) View(c *ui.Context) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if !a.scanning && c.Shortcut(ui.Cmd, ui.KeyR) {
		a.startScan()
	}
	switch a.themeMode {
	case "浅色":
		c.SetTheme(ui.LightTheme())
	case "深色":
		c.SetTheme(ui.DarkTheme())
	}
	t := c.Theme()
	if t == nil {
		t = ui.LightTheme()
	}
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		if a.startupErr != "" {
			ui.Row(c).Padding(10, 14).Background(t.Warning).Children(func() {
				ui.Text(c, a.startupErr).FontSize(12)
				ui.Spacer(c)
				if ui.Button(c, "知道了").Clicked() {
					a.startupErr = ""
				}
			})
		}
		ui.Split(c, &a.sideWidth, func() {
			a.sidebar(c, t)
		}, func() {
			a.content(c, t)
		}).Grow(1)
	})
	a.dialogs(c, t)
}

// sidebar is implemented in sidebar.go.
// content is implemented in results.go.

// options converts the form values into scan options.
func (a *App) options() model.ScanOptions {
	o := model.DefaultScanOptions()
	o.Lookback = lookbackMinutes(a.lookbackValue.Value, a.lookbackUnit)
	o.LookbackUnit = unitKey(a.lookbackUnit)
	o.Workers = int(a.workers.Value)
	o.DeviceWorkers = int(a.deviceWorkers.Value)
	o.NoSearch = a.opt.NoSearch
	o.DeepAVCheck = a.opt.DeepAVCheck
	o.AVSeconds = int(a.avSeconds.Value)
	o.AVWorkers = int(a.avWorkers.Value)
	o.AVLimit = int(a.avLimit.Value)
	o.SilenceDB = a.silenceDB.Value
	o.BusyStart = int(a.busyStart.Value)
	o.BusyEnd = int(a.busyEnd.Value)
	o.BusyDaysAgo = int(a.busyDaysAgo.Value)
	o.AVSave = a.opt.AVSave
	o.AVSaveRoot = strings.TrimSpace(a.saveRoot)
	return o
}

func (a *App) syncFormFromOptions() {
	o := a.opt
	a.lookbackUnit = "分钟"
	value := float64(o.Lookback)
	if o.LookbackUnit == "hour" && o.Lookback%60 == 0 {
		a.lookbackUnit = "小时"
		value = float64(o.Lookback / 60)
	} else if o.LookbackUnit == "day" && o.Lookback%(24*60) == 0 {
		a.lookbackUnit = "天"
		value = float64(o.Lookback / (24 * 60))
	}
	a.lookbackValue.set(value)
	a.workers.set(float64(o.Workers))
	if o.DeviceWorkers <= 0 {
		o.DeviceWorkers = 2
	}
	a.deviceWorkers.set(float64(o.DeviceWorkers))
	a.avSeconds.set(float64(o.AVSeconds))
	a.avWorkers.set(float64(o.AVWorkers))
	a.avLimit.set(float64(o.AVLimit))
	a.silenceDB.set(o.SilenceDB)
	a.busyStart.set(float64(o.BusyStart))
	a.busyEnd.set(float64(o.BusyEnd))
	a.busyDaysAgo.set(float64(o.BusyDaysAgo))
	a.saveRoot = o.AVSaveRoot
}

func (a *App) reloadProfiles() {
	if a.store == nil {
		return
	}
	a.profiles = a.store.ListProfiles()
	sort.Strings(a.profiles)
}

// applyProfile loads a profile into the form state.
func (a *App) applyProfile(name string) {
	if a.store == nil {
		return
	}
	if name == "" {
		name = a.store.ActiveName()
	}
	prof, err := a.store.GetProfile(name)
	if err != nil {
		a.startupErr = "读取档案失败: " + err.Error()
		return
	}
	// ResolveDevices fills passwords from the OS credential store. Using the
	// raw GetProfile copy here was the reason GUI scans authenticated with an
	// empty password while the CLI worked.
	devices, err := a.store.ResolveDevices(name)
	if err != nil {
		a.startupErr = "读取设备凭证失败: " + err.Error()
		return
	}
	a.profile = name
	a.profileChoice = name
	a.devices = append([]model.Device(nil), devices...)
	a.opt = prof.ScanOptions
	if a.opt.Lookback <= 0 {
		a.opt.Lookback = 60
	}
	a.syncFormFromOptions()
	a.rebuildTargets()
	a.selectedDev = -1
	if a.deviceList.Selected != nil {
		*a.deviceList.Selected = -1
	}
	// Start on the first device: scanning "all" would serialize unreachable
	// devices before the user sees any result. "全部设备" stays one click away.
	if len(a.devices) > 0 {
		a.selectedDev = 0
		a.target = deviceName(a.devices[0], 0)
		if a.settings.LastTarget == allDevices {
			a.selectedDev = -1
			a.target = allDevices
		} else if a.settings.LastTarget != "" {
			for i, d := range a.devices {
				if deviceName(d, i) == a.settings.LastTarget {
					a.selectedDev = i
					a.target = a.settings.LastTarget
					break
				}
			}
		}
	}
	if a.deviceList.Selected != nil {
		if a.selectedDev >= 0 {
			*a.deviceList.Selected = a.selectedDev
		} else {
			*a.deviceList.Selected = -1
		}
	}
	a.report = nil
	a.reports = nil
	a.diff = nil
	a.diffs = map[string]*history.Diff{}
	if a.table.Selected != nil {
		*a.table.Selected = -1
	}
	a.store.SetActive(name)
	a.saveSettings()
}

// rebuildTargets refreshes the scan-target choices after device edits.
func (a *App) rebuildTargets() {
	names := []string{allDevices}
	for i, d := range a.devices {
		n := d.Name
		if n == "" {
			n = fmt.Sprintf("设备%d", i+1)
		}
		names = append(names, n)
	}
	found := false
	for _, n := range names {
		if n == a.target {
			found = true
		}
	}
	if !found {
		a.target = allDevices
		a.selectedDev = -1
		if a.deviceList.Selected != nil {
			*a.deviceList.Selected = -1
		}
	}
}

func (a *App) targetDevices() []model.Device {
	if a.target == "" || a.target == allDevices {
		return a.devices
	}
	for i, d := range a.devices {
		name := d.Name
		if name == "" {
			name = fmt.Sprintf("设备%d", i+1)
		}
		if name == a.target {
			return []model.Device{d}
		}
	}
	return nil
}

func (a *App) activeDevice() (model.Device, bool) {
	devs := a.targetDevices()
	if len(devs) == 1 {
		return devs[0], true
	}
	if a.selectedDev >= 0 && a.selectedDev < len(a.devices) {
		return a.devices[a.selectedDev], true
	}
	if len(a.devices) > 0 {
		return a.devices[0], true
	}
	return model.Device{}, false
}

// saveProfile persists the current devices and options into profiles.json.
func (a *App) saveProfile() {
	if a.store == nil || a.profile == "" {
		return
	}
	prof := &model.Profile{
		Name:        a.profile,
		Devices:     append([]model.Device(nil), a.devices...),
		ScanOptions: a.options(),
	}
	if err := a.store.UpdateProfile(a.profile, prof); err != nil {
		a.logf("保存档案失败: "+err.Error(), "error")
	}
}

// startScan launches an inspection in the background.
func (a *App) startScan() {
	if a.scanning || a.store == nil {
		return
	}
	a.saveProfile()
	devices := a.targetDevices()
	if len(devices) == 0 {
		a.logf("没有可巡检的设备，请先添加设备", "error")
		return
	}
	opts := a.options()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelScan = cancel
	a.scanning = true
	a.singleCheck = false
	a.scanDone = make(chan struct{})
	a.progress = 0
	a.progressLabel = "准备巡检…"
	a.reports = nil
	a.logf(fmt.Sprintf("开始巡检：%s（%d 台设备）", a.target, len(devices)), "step")

	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.post(func() {
					a.scanning = false
					a.singleCheck = false
					a.cancelScan = nil
					a.logf(fmt.Sprintf("巡检异常终止: %v", r), "error")
				})
			}
		}()
		progress := func(p model.Progress) {
			now := time.Now().UnixNano()
			if p.Message != "" {
				a.logFromWorker(p.Message, progressLevel(p.Phase))
			}
			if now-a.lastProgressNanos.Load() > int64(80*time.Millisecond) || p.Overall >= 1 {
				a.lastProgressNanos.Store(now)
				a.post(func() {
					a.progress = p.Overall
					if p.Message != "" {
						a.progressLabel = p.Message
					}
				})
			}
		}
		onNVR := func(n *scan.NVR) {
			n.Client.SetContext(ctx)
			a.post(func() { a.currentNVR = n })
		}
		if len(devices) == 1 {
			rep := a.runner.RunWith(devices[0], opts, deviceName(devices[0], 0), progress, onNVR)
			a.post(func() {
				a.reports = []model.Report{rep}
				a.finishScan(&rep)
			})
			return
		}
		reps := a.runner.QueueWith(devices, opts, progress, nil, onNVR)
		a.post(func() {
			a.reports = reps
			if len(reps) > 0 {
				last := reps[len(reps)-1]
				a.finishScan(&last)
			} else {
				a.finishScan(nil)
			}
		})
	}()
}

func (a *App) finishScan(rep *model.Report) {
	a.scanning = false
	a.cancelScan = nil
	a.currentNVR = nil
	a.progress = 1
	if rep == nil {
		a.progressLabel = "巡检结束"
		return
	}
	a.report = rep
	a.resultDevice = rep.DeviceName
	a.selectedRow = -1
	if len(rep.Records) > 0 {
		a.selectedRow = 0
		*a.table.Selected = 0
	}
	if rep.Error != "" {
		a.progressLabel = "巡检失败"
		a.logf(rep.Error, "error")
	} else {
		a.progressLabel = "巡检完成"
		a.logf(fmt.Sprintf("巡检完成：%s（健康 %s）", rep.DeviceName, rep.Health.Status), levelForHealth(rep.Health.Status))
	}
	a.diffs = map[string]*history.Diff{}
	if rep.Error == "" {
		reports := a.reports
		if len(reports) == 0 {
			reports = []model.Report{*rep}
		}
		for _, r := range reports {
			if prev, err := history.LatestForDevice(r.IP); err == nil && prev != nil {
				d := history.Compare(*prev, r)
				a.diffs[r.IP] = &d
			}
		}
		a.diff = a.diffs[rep.IP]
	} else {
		a.diff = nil
	}
	a.saveHistory(rep)
	a.recordLastHealth()
	a.saveSettings()
	if a.scanDone != nil {
		close(a.scanDone)
	}
}

// ScanDone is closed when the current scan finishes (tests use it to wait).
func (a *App) ScanDone() <-chan struct{} { return a.scanDone }

// recordLastHealth keeps a per-device verdict for the sidebar list.
func (a *App) recordLastHealth() {
	if a.lastHealth == nil {
		a.lastHealth = map[string]string{}
	}
	reps := a.reports
	if len(reps) == 0 && a.report != nil {
		reps = []model.Report{*a.report}
	}
	for _, r := range reps {
		if r.IP == "" {
			continue
		}
		status := r.Health.Status
		if r.Error != "" {
			status = "严重"
		}
		if status == "" {
			status = "未知"
		}
		a.lastHealth[r.IP] = status
	}
}

// cancelRunning cancels the current scan.
func (a *App) cancelRunning() {
	if a.cancelScan != nil {
		a.cancelScan()
		a.logf("已请求取消巡检…", "warn")
	}
	// The NVR client already received the cancelable context in onNVR;
	// canceling it is enough for recording/AV checks to stop.

}

// runSingleCheck re-probes the selected table rows.
func (a *App) runSingleCheck() {
	if a.scanning || a.singleCheck || a.report == nil {
		return
	}
	var recs []model.Record
	if a.selectedRow >= 0 && a.selectedRow < len(a.report.Records) {
		recs = []model.Record{a.report.Records[a.selectedRow]}
	}
	if len(recs) == 0 {
		a.logf("请先在通道表中选择一行", "warn")
		return
	}
	dev, ok := a.deviceForReport(*a.report)
	if !ok {
		a.logf("当前结果对应的设备不在档案中", "error")
		return
	}
	opts := a.options()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelScan = cancel
	a.singleCheck = true
	a.logf("开始单路抽检："+recs[0].Name, "step")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.post(func() {
					a.scanning = false
					a.singleCheck = false
					a.cancelScan = nil
					a.logf(fmt.Sprintf("巡检异常终止: %v", r), "error")
				})
			}
		}()
		progress := func(p model.Progress) {
			if p.Message != "" {
				a.logFromWorker(p.Message, "info")
			}
			a.post(func() {
				a.progress = p.Overall
				a.progressLabel = p.Message
			})
		}
		onNVR := func(n *scan.NVR) {
			n.Client.SetContext(ctx)
			a.post(func() { a.currentNVR = n })
		}
		updated, err := a.runner.RunSingleAVWith(recs, dev, opts, progress, onNVR)
		a.post(func() {
			a.singleCheck = false
			a.cancelScan = nil
			a.currentNVR = nil
			if err != nil {
				a.logf("单路抽检失败: "+err.Error(), "error")
				return
			}
			for _, u := range updated {
				for i := range a.report.Records {
					if a.report.Records[i].Channel == u.Channel {
						a.report.Records[i] = u
					}
				}
			}
			a.logf("单路抽检完成："+recs[0].Name, "ok")
		})
	}()
}

func (a *App) deviceForReport(rep model.Report) (model.Device, bool) {
	for _, d := range a.devices {
		if d.IP == rep.IP {
			return d, true
		}
	}
	return model.Device{}, false
}

// saveHistory archives the report.
func (a *App) saveHistory(rep *model.Report) {
	if rep == nil || rep.Error != "" {
		return
	}
	if _, err := history.SaveReport(*rep); err != nil {
		a.logf("归档巡检报告失败: "+err.Error(), "warn")
	}
}

// openHistory loads the archive list and shows the dialog.
func (a *App) openHistory() {
	list, err := history.ListReports()
	if err != nil {
		a.logf("读取历史报告失败: "+err.Error(), "error")
		return
	}
	a.historyList = list
	a.historySel = -1
	a.historyOpen = true
}

// logf appends a log line on the UI thread.
func (a *App) logf(text, level string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.logMu.Lock()
	a.logs = append(a.logs, logLine{Text: text, Level: level})
	if len(a.logs) > 5000 {
		a.logs = a.logs[len(a.logs)-5000:]
	}
	a.logMu.Unlock()
}

// logFromWorker posts a log line from a background goroutine.
func (a *App) logFromWorker(text, level string) {
	a.post(func() { a.logf(text, level) })
}

// post runs fn on the window's main thread.
func (a *App) post(fn func()) {
	if a.win != nil {
		a.win.Update(fn)
		return
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	fn()
}

// settingsFile is where UI settings live.
func settingsFile() string { return filepath.Join(config.AppDataDir(), "ui_settings.json") }

func (a *App) loadSettings() {
	data, err := os.ReadFile(settingsFile())
	if err != nil {
		return
	}
	var s Settings
	if json.Unmarshal(data, &s) == nil {
		a.settings = s
		if s.SideWidth > 200 && s.SideWidth < 700 {
			a.sideWidth = s.SideWidth
		}
		if s.Theme != "" {
			a.themeMode = s.Theme
		}
		if len(s.ColumnsOrder) > 0 || len(s.ColumnsWidth) > 0 {
			a.table.Columns = ui.TableLayout{Order: s.ColumnsOrder, Widths: s.ColumnsWidth}
		}
	}
}

// SaveSettings persists UI state; called from the app's quit hook.
func (a *App) SaveSettings() { a.saveSettings() }

func (a *App) saveSettings() {
	a.settings.Theme = a.themeMode
	a.settings.SideWidth = a.sideWidth
	a.settings.LastTarget = a.target
	if len(a.table.Columns.Order) > 0 {
		a.settings.ColumnsOrder = a.table.Columns.Order
	}
	if len(a.table.Columns.Widths) > 0 {
		a.settings.ColumnsWidth = a.table.Columns.Widths
	}
	data, err := json.MarshalIndent(a.settings, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(settingsFile()), 0o755)
	_ = os.WriteFile(settingsFile(), data, 0o600)
}

// --- small helpers shared by the panels ---

func deviceName(d model.Device, i int) string {
	if strings.TrimSpace(d.Name) != "" {
		return d.Name
	}
	return fmt.Sprintf("设备%d", i+1)
}

func lookbackMinutes(value float64, unit string) int {
	if value <= 0 {
		value = 60
	}
	switch unit {
	case "小时":
		return int(value * 60)
	case "天":
		return int(value * 24 * 60)
	default:
		return int(value)
	}
}

func unitKey(unit string) string {
	switch unit {
	case "小时":
		return "hour"
	case "天":
		return "day"
	default:
		return "minute"
	}
}

func progressLevel(phase string) string {
	switch phase {
	case "disk", "av":
		return "step"
	default:
		return "info"
	}
}

func levelForHealth(status string) string {
	switch status {
	case "良好":
		return "ok"
	case "警告":
		return "warn"
	case "严重":
		return "error"
	default:
		return "info"
	}
}

// ffmpegAvailable reports whether the bundled/PATH ffmpeg tools exist.
func (a *App) ffmpegAvailable() bool {
	t := util.Tools()
	return t["ffmpeg"] != "" && t["ffprobe"] != ""
}

func formatPercent(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 0, 64) + "%"
}
