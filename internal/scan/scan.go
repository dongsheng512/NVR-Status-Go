// Package scan is the shared orchestration layer (the Go equivalent of
// nvr_core/scan_runner.py): build an NVR client from device+options, run a
// full inspection, or queue several devices.
package scan

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"nvrstatus/internal/avprobe"
	"nvrstatus/internal/health"
	"nvrstatus/internal/isapi"
	"nvrstatus/internal/limit"
	"nvrstatus/internal/model"
	"nvrstatus/internal/recording"
	"nvrstatus/internal/storage"
	"nvrstatus/internal/util"
)

// Options configures a Runner.
type Options struct {
	Quiet           bool
	DefaultSaveRoot string
	Logger          func(string)
	// Tools overrides util.Tools (tests inject fake ffmpeg/ffprobe paths).
	Tools func() map[string]string
	// GlobalAV caps ffmpeg processes across all devices; nil creates a default
	// budget in NewRunner.
	GlobalAV *limit.Limiter
}

// Runner builds and runs inspections.
type Runner struct {
	Options Options
}

// NewRunner returns a Runner.
func NewRunner(o Options) *Runner {
	if o.GlobalAV == nil {
		o.GlobalAV = limit.New(6)
	}
	return &Runner{Options: o}
}

// NVR bundles the protocol services for one device.
type NVR struct {
	Device model.Device
	Opt    model.ScanOptions

	Client    *isapi.Client
	Recording *recording.Service
	Storage   *storage.Service
	AV        *avprobe.Service
}

// Build constructs an NVR from a device and scan options, applying the same
// ffmpeg degradation and lookback expansion as the Python version.
func (r *Runner) Build(device model.Device, opt model.ScanOptions) *NVR {
	quiet := r.Options.Quiet
	logf := func(msg string) {
		if r.Options.Logger != nil {
			r.Options.Logger(msg)
		} else if !quiet {
			fmt.Fprintln(os.Stderr, msg)
		}
	}

	wantDeep := opt.DeepAVCheck || opt.AVSave
	toolsFn := r.Options.Tools
	if toolsFn == nil {
		toolsFn = util.Tools
	}
	tools := toolsFn()
	hasFF := tools["ffmpeg"] != "" && tools["ffprobe"] != ""
	if wantDeep && !hasFF {
		logf("未找到 ffmpeg/ffprobe，已跳过深度抽检，仅做状态与近期录像检查。请安装: brew install ffmpeg")
		wantDeep = false
		opt.AVSave = false
		opt.DeepAVCheck = false
	}
	wantSave := opt.AVSave && hasFF && wantDeep

	lookback := opt.Lookback
	if lookback <= 0 {
		lookback = 60
	}
	if lookback > 30*24*60 {
		lookback = 30 * 24 * 60
	}
	daysAgo := opt.BusyDaysAgo
	if daysAgo < 0 {
		daysAgo = 0
	}
	if daysAgo > 30 {
		daysAgo = 30
	}
	if wantDeep && daysAgo > 0 && lookback < (daysAgo+1)*24*60 {
		lookback = (daysAgo + 1) * 24 * 60
		logf(fmt.Sprintf("已按抽检日自动扩大落盘检查窗口至近 %d 天", daysAgo+1))
	}

	saveRoot := opt.AVSaveRoot
	if saveRoot == "" {
		saveRoot = r.Options.DefaultSaveRoot
	}
	if saveRoot == "" {
		saveRoot = "av_samples"
	}

	// Persist the effective values (ffmpeg degradation, expanded lookback,
	// clamped ranges) so the report says what actually ran.
	opt.DeepAVCheck = wantDeep
	opt.AVSave = wantSave
	opt.Lookback = lookback
	opt.BusyDaysAgo = daysAgo
	opt.AVSeconds = clamp(opt.AVSeconds, 3, 12)
	opt.AVWorkers = clamp(opt.AVWorkers, 1, 3)
	opt.AVSaveRoot = saveRoot

	client := isapi.New(isapi.Config{
		IP:       device.IP,
		Port:     device.Port,
		Username: device.Username,
		Password: device.Password,
		UseSSL:   device.SSL,
		Quiet:    quiet,
		Logger:   logf,
	})

	searchWorkers := clamp(opt.Workers, 1, 16)
	deviceWorkers := clamp(opt.DeviceWorkers, 1, 4)
	if deviceWorkers > 1 {
		searchWorkers = (searchWorkers + deviceWorkers - 1) / deviceWorkers
		if searchWorkers < 1 {
			searchWorkers = 1
		}
	}
	rec := &recording.Service{
		F:                  client,
		LookbackMinutes:    lookback,
		SearchWorkers:      searchWorkers,
		DeepAVCheck:        wantDeep,
		CheckDiskRecording: !opt.NoSearch,
		BusyStartHour:      clamp(opt.BusyStart, 0, 23),
		BusyEndHour:        clamp(opt.BusyEnd, 1, 24),
		BusyDaysAgo:        daysAgo,
		AVAt:               opt.AVAt,
		Log:                logf,
	}
	if rec.BusyEndHour <= rec.BusyStartHour {
		rec.BusyStartHour, rec.BusyEndHour = 10, 18
	}

	store := &storage.Service{F: client, Log: logf}

	av := &avprobe.Service{
		Rec:        rec,
		FFmpeg:     tools["ffmpeg"],
		FFprobe:    tools["ffprobe"],
		AVSeconds:  opt.AVSeconds,
		AVWorkers:  opt.AVWorkers,
		AVLimit:    opt.AVLimit,
		AVChannels: opt.AVChannels,
		AVAt:       opt.AVAt,
		SilenceDB:  opt.SilenceDB,
		AVSave:     wantSave,
		AVSaveRoot: saveRoot,
		Username:   device.Username,
		Password:   device.Password,
		// Location is resolved lazily in NVR.Run: Build must not touch the network.
		Log:         logf,
		CheckCancel: client.CheckCancel,
	}

	rec.DeepCheck = av.Run
	rec.Progress = nil

	return &NVR{Device: device, Opt: opt, Client: client, Recording: rec, Storage: store, AV: av}
}

// Run performs a full inspection and returns the unified report.
func (r *Runner) Run(device model.Device, opt model.ScanOptions, name string, progress func(model.Progress)) model.Report {
	return r.RunWith(device, opt, name, progress, nil)
}

// RunWith is Run with a hook to the built NVR (the GUI uses it to cancel).
func (r *Runner) RunWith(device model.Device, opt model.ScanOptions, name string, progress func(model.Progress), onNVR func(*NVR)) model.Report {
	return r.runWith(device, opt, name, progress, onNVR, 1)
}

// runWith builds one device. parallel is the site-wide device concurrency, used
// to divide the per-device CMSearch worker budget so the total stays bounded.
func (r *Runner) runWith(device model.Device, opt model.ScanOptions, name string, progress func(model.Progress), onNVR func(*NVR), parallel int) model.Report {
	opt.DeviceWorkers = parallel
	n := r.Build(device, opt)
	defer n.Client.Close()
	n.Recording.Progress = progress
	n.AV.Progress = progress
	if onNVR != nil {
		onNVR(n)
	}
	report := n.Run(progress)
	if report.DeviceName == "" {
		report.DeviceName = name
	}
	return report
}

// Run executes against an already-built NVR.
func (n *NVR) Run(progress func(model.Progress)) model.Report {
	emit := func(msg, phase string, overall float64) {
		if progress != nil {
			progress(model.Progress{Message: msg, Phase: phase, Overall: overall})
		}
	}
	name := n.Device.Name
	if name == "" {
		name = n.Device.IP
	}
	emit(fmt.Sprintf("连接 %s...", name), "connect", 0.03)
	info, err := n.Client.DeviceInfo()
	if err != nil || len(info) == 0 {
		return errorReport(n, name, "无法获取设备信息，请检查 IP/账号/网络")
	}
	if dn := info["设备名称"]; dn != "" && n.Device.Name == "" {
		name = dn
	}
	emit(fmt.Sprintf("设备: %s 固件 %s", info["型号"], info["固件版本"]), "device", 0.10)
	emit("读取系统状态…", "sys", 0.14)
	sys, _ := n.Client.SystemStatus()
	// Resolve the device wall-clock timezone now that the client is connected.
	n.AV.Location = n.Client.DeviceLocation()
	emit("获取摄像头列表…", "cameras", 0.18)
	cameras, _ := n.Client.Cameras()
	emit(fmt.Sprintf("摄像头 %d 路，开始录像/近期录像检查...", len(cameras)), "recording", 0.26)
	records, recErr := n.Recording.RecordingStatus()
	if recErr != nil {
		if recErr == util.ErrCancelled {
			return errorReport(n, name, "巡检已取消")
		}
		return errorReport(n, name, "录像检查失败: "+recErr.Error())
	}
	emit("读取硬盘状态与循环覆盖…", "storage", 0.97)
	drives, _ := n.Storage.StorageStatus()
	overwrite := n.Storage.DiskOverwriteStatus(drives)
	emit("汇总健康状态…", "health", 0.99)
	summary := health.Summarize(cameras, records, drives, overwrite, n.Opt.DeepAVCheck)
	alarms, _ := n.Client.Alarms()
	emit("巡检完成", "done", 1.0)
	return model.Report{
		DeviceName:         name,
		IP:                 n.Device.IP,
		Info:               info,
		SysStatus:          sys,
		Health:             summary,
		Alarms:             alarms,
		Cameras:            cameras,
		Records:            records,
		Drives:             drives,
		DiskOverwrite:      overwrite,
		LookbackMinutes:    n.Recording.LookbackMinutes,
		DeepAVCheck:        n.Opt.DeepAVCheck,
		AVSeconds:          n.Opt.AVSeconds,
		AVWorkers:          n.Opt.AVWorkers,
		AVChannels:         n.Opt.AVChannels,
		BusyStartHour:      n.Opt.BusyStart,
		BusyEndHour:        n.Opt.BusyEnd,
		AVSave:             n.Opt.AVSave,
		AVSaveDir:          n.AV.AVSaveDir(),
		CheckDiskRecording: !n.Opt.NoSearch,
	}
}

// Queue scans devices with the configured device concurrency and preserves
// the input order in the returned reports.
func (r *Runner) Queue(devices []model.Device, opt model.ScanOptions, progress func(model.Progress), onDevice func(model.Report)) []model.Report {
	return r.QueueWith(devices, opt, progress, onDevice, nil)
}

// QueueWith is Queue with a per-device NVR hook (used by the GUI for cancel).
func (r *Runner) QueueWith(devices []model.Device, opt model.ScanOptions, progress func(model.Progress), onDevice func(model.Report), onNVR func(*NVR)) []model.Report {
	workers := clamp(opt.DeviceWorkers, 1, 4)
	return r.queueConcurrent(devices, opt, progress, onDevice, onNVR, workers)
}

// QueueCtx is Queue bound to ctx: cancelling ctx aborts in-flight ISAPI
// requests and kills running ffmpeg children, and devices not yet started fail
// fast instead of scanning. Callers should check ctx.Err() afterwards to tell
// "cancelled" apart from "device broken".
func (r *Runner) QueueCtx(ctx context.Context, devices []model.Device, opt model.ScanOptions, progress func(model.Progress), onDevice func(model.Report)) []model.Report {
	return r.QueueWith(devices, opt, progress, onDevice, bindContext(ctx))
}

// RunCtx is Run bound to ctx (single device).
func (r *Runner) RunCtx(ctx context.Context, device model.Device, opt model.ScanOptions, name string, progress func(model.Progress)) model.Report {
	return r.RunWith(device, opt, name, progress, bindContext(ctx))
}

// bindContext returns the onNVR hook that installs ctx on the freshly built
// client. Build happens before the hook runs, so every request in NVR.Run —
// including the recording and ffmpeg layers that borrow CheckCancel — sees it.
func bindContext(ctx context.Context) func(*NVR) {
	if ctx == nil {
		return nil
	}
	return func(n *NVR) { n.Client.SetContext(ctx) }
}

// queueConcurrent runs up to workers devices at once. Progress is aggregated
// across devices so the UI sees one overall bar (device work is I/O bound and
// users care about the whole site, not a single device's 0–100%).
func (r *Runner) queueConcurrent(devices []model.Device, opt model.ScanOptions, progress func(model.Progress), onDevice func(model.Report), onNVR func(*NVR), workers int) []model.Report {
	total := len(devices)
	if total == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > total {
		workers = total
	}
	results := make([]model.Report, total)
	partial := make([]float64, total)
	var mu sync.Mutex
	emit := func(p model.Progress) {
		if progress == nil {
			return
		}
		mu.Lock()
		sum := 0.0
		for _, v := range partial {
			sum += v
		}
		mu.Unlock()
		p.Overall = sum / float64(total)
		progress(p)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				dev := devices[i]
				name := dev.Name
				if name == "" {
					name = fmt.Sprintf("设备%d", i+1)
				}
				emit(model.Progress{Message: fmt.Sprintf("—— [%d/%d] %s ——", i+1, total, name), Phase: "init", Overall: 0})
				rep := r.runWith(dev, opt, name, func(p model.Progress) {
					mu.Lock()
					partial[i] = clampFloat(p.Overall)
					mu.Unlock()
					emit(p)
				}, onNVR, workers)
				mu.Lock()
				partial[i] = 1
				mu.Unlock()
				results[i] = rep
				if onDevice != nil {
					onDevice(rep)
				}
				emit(model.Progress{Message: fmt.Sprintf("完成 [%d/%d] %s", i+1, total, name), Phase: "done"})
			}
		}()
	}
	for i := range devices {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

func clampFloat(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// RunSingleAV re-probes only the selected records (GUI "single channel check").
func (r *Runner) RunSingleAV(records []model.Record, device model.Device, opt model.ScanOptions, progress func(model.Progress)) ([]model.Record, error) {
	return r.RunSingleAVWith(records, device, opt, progress, nil)
}

// RunSingleAVWith is RunSingleAV with an NVR hook for cancellation.
func (r *Runner) RunSingleAVWith(records []model.Record, device model.Device, opt model.ScanOptions, progress func(model.Progress), onNVR func(*NVR)) ([]model.Record, error) {
	work := make([]model.Record, len(records))
	copy(work, records)
	opt.DeepAVCheck = true
	opt.AVSave = false
	opt.AVLimit = 0
	opt.DeviceWorkers = 1
	opt.AVAt = nil
	channels := make([]int, 0, len(work))
	for _, rec := range work {
		parsed, err := util.ParseChannelList(rec.Channel)
		if err != nil || len(parsed) == 0 {
			return nil, fmt.Errorf("选中记录的通道号无效: %q,无法定点抽检", rec.Channel)
		}
		channels = append(channels, parsed...)
	}
	opt.AVChannels = channels
	n := r.Build(device, opt)
	defer n.Client.Close()
	n.AV.Progress = progress
	if onNVR != nil {
		onNVR(n)
	}
	if err := n.AV.Run(work); err != nil {
		return nil, err
	}
	return work, nil
}

func errorReport(n *NVR, name, msg string) model.Report {
	return model.Report{
		DeviceName:      name,
		IP:              n.Device.IP,
		Info:            map[string]string{},
		SysStatus:       map[string]string{},
		Health:          model.HealthSummary{Status: "未知"},
		Cameras:         []model.Camera{},
		Records:         []model.Record{},
		Drives:          []model.Drive{},
		LookbackMinutes: n.Recording.LookbackMinutes,
		DeepAVCheck:     n.Opt.DeepAVCheck,
		AVSeconds:       n.Opt.AVSeconds,
		AVWorkers:       n.Opt.AVWorkers,
		Error:           msg,
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

var _ = time.Now // keep time imported for future timing hooks
