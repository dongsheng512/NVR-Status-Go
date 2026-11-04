// Command nvr is the Go rewrite of the cam-gui CLI.
//
// Usage:
//
//	nvr [all|<index>] [flags]      # devices from the active profile
//	nvr -i <ip> -w <password> ...  # single device, no config file
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"nvrstatus/internal/config"
	"nvrstatus/internal/model"
	"nvrstatus/internal/report"
	"nvrstatus/internal/scan"
	"nvrstatus/internal/util"
)

const version = "3.0.0-dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

type cliArgs struct {
	selector string
	ip       string
	port     int
	username string
	password string
	ssl      bool
	verbose  bool

	lookback      int
	noSearch      bool
	workers       int
	deviceWorkers int
	deepAV        bool
	avSeconds     int
	avWorkers     int
	avLimit       int
	avChannels    string
	avAt          string
	silenceDB     float64
	busyStart     int
	busyEnd       int
	busyDaysAgo   int
	avSave        bool
	avSaveRoot    string

	configPath string
	profile    string
	showVer    bool
}

func parseArgs(argv []string) (*cliArgs, error) {
	a := &cliArgs{selector: "all", port: 80, username: "admin", lookback: 60, workers: 8,
		avSeconds: 6, avWorkers: 2, silenceDB: -80, busyStart: 10, busyEnd: 18}
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") {
		a.selector = argv[0]
		argv = argv[1:]
	}
	fs := flag.NewFlagSet("nvr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&a.ip, "i", "", "NVR/DVR 的 IP 地址（单机直连时必填）")
	fs.StringVar(&a.ip, "ip", "", "NVR/DVR 的 IP 地址（同 -i）")
	fs.IntVar(&a.port, "p", 80, "端口号")
	fs.IntVar(&a.port, "port", 80, "端口号（同 -p）")
	fs.StringVar(&a.username, "u", "admin", "用户名")
	fs.StringVar(&a.username, "username", "admin", "用户名（同 -u）")
	fs.StringVar(&a.password, "w", "", "密码")
	fs.StringVar(&a.password, "password", "", "密码（同 -w）")
	fs.BoolVar(&a.ssl, "s", false, "使用 HTTPS")
	fs.BoolVar(&a.ssl, "ssl", false, "使用 HTTPS（同 -s）")
	fs.BoolVar(&a.verbose, "v", false, "输出全部通道明细与设备详情")
	fs.BoolVar(&a.verbose, "verbose", false, "输出全部通道明细与设备详情（同 -v）")
	fs.IntVar(&a.lookback, "lookback", 60, "检查近期录像落盘的时间窗口(分钟)")
	fs.BoolVar(&a.noSearch, "no-search", false, "跳过 CMSearch 落盘检查")
	fs.IntVar(&a.workers, "workers", 8, "落盘检查并发数")
	fs.IntVar(&a.deviceWorkers, "device-workers", 2, "多设备巡检时的设备并发数(1-4)")
	fs.BoolVar(&a.deepAV, "deep-av-check", false, "启用深度音视频抽检")
	fs.IntVar(&a.avSeconds, "av-seconds", 6, "每路抽检时长(秒)")
	fs.IntVar(&a.avWorkers, "av-workers", 2, "抽检并发数")
	fs.IntVar(&a.avLimit, "av-limit", 0, "最多抽检通道数(0=全部)")
	fs.StringVar(&a.avChannels, "av-channels", "", "只抽检指定物理通道号(逗号分隔)")
	fs.StringVar(&a.avAt, "av-at", "", "定点抽检时刻(HH:MM,逗号分隔)")
	fs.Float64Var(&a.silenceDB, "silence-db", -80, "音频静音阈值(dB)")
	fs.IntVar(&a.busyStart, "busy-start", 10, "深度抽检优先时段开始(小时)")
	fs.IntVar(&a.busyEnd, "busy-end", 18, "深度抽检优先时段结束(小时)")
	fs.IntVar(&a.busyDaysAgo, "busy-days-ago", 0, "优先抽检 N 天前的繁忙时段")
	fs.BoolVar(&a.avSave, "av-save", false, "保存抽检片段(自动开启深度抽检)")
	fs.StringVar(&a.avSaveRoot, "av-save-root", "", "抽检片段保存根目录")
	fs.StringVar(&a.configPath, "config", "", "指定 nvr_config.json（默认使用 profiles.json 当前档案）")
	fs.StringVar(&a.profile, "profile", "", "指定 profiles.json 中的档案名")
	fs.BoolVar(&a.showVer, "version", false, "显示版本")
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	return a, nil
}

func run(argv []string) int {
	a, err := parseArgs(argv)
	if err != nil {
		return 1
	}
	if a.showVer {
		fmt.Println(version)
		return 0
	}

	deep := a.deepAV || a.avSave
	if a.avChannels != "" && !deep {
		fmt.Println("警告: --av-channels 已忽略: 未启用深度抽检(需 --deep-av-check 或 --av-save)")
		a.avChannels = ""
	}
	if a.avAt != "" && !deep {
		fmt.Println("警告: --av-at 已忽略: 未启用深度抽检(需 --deep-av-check 或 --av-save)")
		a.avAt = ""
	}

	opts := model.DefaultScanOptions()
	opts.Lookback = a.lookback
	opts.NoSearch = a.noSearch
	opts.Workers = a.workers
	opts.DeviceWorkers = a.deviceWorkers
	opts.DeepAVCheck = deep
	opts.AVSeconds = a.avSeconds
	opts.AVWorkers = a.avWorkers
	opts.AVLimit = a.avLimit
	opts.SilenceDB = a.silenceDB
	opts.BusyStart = a.busyStart
	opts.BusyEnd = a.busyEnd
	opts.BusyDaysAgo = a.busyDaysAgo
	opts.AVSave = a.avSave
	opts.AVSaveRoot = a.avSaveRoot
	if a.avChannels != "" {
		channels, err := util.ParseChannelList(a.avChannels)
		if err != nil {
			fmt.Printf("警告: 抽检通道过滤无效(%v)，改为抽检全部候选通道\n", err)
		} else {
			opts.AVChannels = channels
		}
	}
	if a.avAt != "" {
		instants, err := util.ParseInstantList(a.avAt, a.busyDaysAgo, nowFunc())
		if err != nil {
			fmt.Printf("警告: 定点抽检时刻无效(%v)，改为繁忙时段逻辑选点\n", err)
		} else {
			opts.AVAt = instants
		}
	}

	var devices []model.Device
	if a.ip != "" || a.password != "" {
		if a.ip == "" || a.password == "" {
			fmt.Fprintln(os.Stderr, "单机模式需要 -i/--ip 与 -w/--password；多设备请使用 ./nvr")
			return 1
		}
		devices = []model.Device{{IP: a.ip, Port: a.port, Username: a.username, Password: a.password, SSL: a.ssl}}
	} else {
		devices, err = loadDevices(a)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取配置失败: %v\n", err)
			return 1
		}
	}

	selected, err := selectDevices(devices, a.selector)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Ctrl-C / SIGTERM must abort the scan, not just the process: the context
	// reaches every ISAPI request and kills running ffmpeg children.
	ctx, signalExit, stopSignals := util.SignalContext()
	defer stopSignals()

	runner := scan.NewRunner(scan.Options{Quiet: false, DefaultSaveRoot: defaultSaveRoot()})
	reports := runner.QueueCtx(ctx, selected, opts, func(p model.Progress) {
		if p.Message != "" {
			fmt.Fprintf(os.Stderr, "  · %s\n", p.Message)
		}
	}, nil)
	if code := signalExit(); code != 0 {
		fmt.Fprintln(os.Stderr, "巡检已中断，未生成报告（已停止所有请求与 ffmpeg 子进程）。")
		return code
	}
	report.RenderAll(os.Stdout, reports, a.verbose)

	return exitCode(reports)
}

// exitCode maps inspection results to the CLI exit status:
// 0 ok, 1 any device failed, 2 no failures but at least one 严重 verdict.
func exitCode(reports []model.Report) int {
	failed := false
	severe := false
	for _, r := range reports {
		if r.Error != "" {
			failed = true
		}
		if r.Health.Status == "严重" {
			severe = true
		}
	}
	if failed {
		return 1
	}
	if severe {
		return 2
	}
	return 0
}

func loadDevices(a *cliArgs) ([]model.Device, error) {
	if a.configPath != "" {
		data, err := os.ReadFile(a.configPath)
		if err != nil {
			return nil, err
		}
		var legacy struct {
			Devices []model.Device `json:"devices"`
			Default int            `json:"default"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, err
		}
		if len(legacy.Devices) == 0 {
			return nil, fmt.Errorf("%s 中没有 devices", a.configPath)
		}
		return legacy.Devices, nil
	}
	st := config.New("")
	if err := st.Load(); err != nil {
		return nil, err
	}
	name := a.profile
	if name == "" {
		name = st.ActiveName()
	}
	devices, err := st.ResolveDevices(name)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("档案 %q 中没有设备", name)
	}
	return devices, nil
}

func selectDevices(devices []model.Device, selector string) ([]model.Device, error) {
	if selector == "" || strings.EqualFold(selector, "all") || selector == "*" {
		return devices, nil
	}
	n, err := strconv.Atoi(selector)
	if err != nil || n < 1 || n > len(devices) {
		return nil, fmt.Errorf("错误: 设备编号 %s 不存在\n\n可用设备:\n%s", selector, deviceList(devices))
	}
	return []model.Device{devices[n-1]}, nil
}

func deviceList(devices []model.Device) string {
	var b strings.Builder
	for i, d := range devices {
		name := d.Name
		if name == "" {
			name = fmt.Sprintf("设备%d", i+1)
		}
		fmt.Fprintf(&b, "  %d. %s - %s\n", i+1, name, d.IP)
	}
	return b.String()
}

func defaultSaveRoot() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return dir + "/NVRStatus/av_samples"
	}
	return "av_samples"
}

// nowFunc is a variable so tests can freeze time if the CLI is exercised.
var nowFunc = func() time.Time { return time.Now() }
