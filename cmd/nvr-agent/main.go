// Command nvr-agent runs scheduled NVR scans headlessly and exposes the
// latest results as /healthz and Prometheus /metrics.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nvrstatus/internal/config"
	"nvrstatus/internal/model"
	"nvrstatus/internal/monitor"
	"nvrstatus/internal/report"
	"nvrstatus/internal/scan"
	"nvrstatus/internal/util"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type agentArgs struct {
	profile       string
	configPath    string
	listen        string
	token         string
	interval      time.Duration
	once          bool
	webhook       string
	noSearch      bool
	deep          bool
	deviceWorkers int
	workers       int
	lookback      int
	avSeconds     int
	avWorkers     int
}

func parseArgs(argv []string) (*agentArgs, error) {
	a := &agentArgs{
		listen:        "127.0.0.1:9101",
		interval:      30 * time.Minute,
		deviceWorkers: 2,
		workers:       8,
		lookback:      60,
		avSeconds:     6,
		avWorkers:     2,
	}
	fs := flag.NewFlagSet("nvr-agent", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&a.profile, "profile", "", "profiles.json 档案名（默认当前档案）")
	fs.StringVar(&a.configPath, "config", "", "legacy nvr_config.json 路径")
	fs.StringVar(&a.listen, "listen", "127.0.0.1:9101", "HTTP 监听地址")
	fs.StringVar(&a.token, "token", "", "HTTP 访问令牌（监听非回环地址时留空会自动生成）")
	fs.DurationVar(&a.interval, "interval", 30*time.Minute, "巡检间隔")
	fs.BoolVar(&a.once, "once", false, "只巡检一次后退出")
	fs.StringVar(&a.webhook, "webhook", "", "巡检完成后的 Webhook URL（POST JSON）")
	fs.BoolVar(&a.noSearch, "no-search", false, "跳过 CMSearch 落盘检查")
	fs.BoolVar(&a.deep, "deep-av-check", false, "启用深度音视频抽检")
	fs.IntVar(&a.deviceWorkers, "device-workers", 2, "设备并发数(1-4)")
	fs.IntVar(&a.workers, "workers", 8, "单设备落盘检查并发数")
	fs.IntVar(&a.lookback, "lookback", 60, "落盘检查窗口(分钟)")
	fs.IntVar(&a.avSeconds, "av-seconds", 6, "每路抽检时长(秒)")
	fs.IntVar(&a.avWorkers, "av-workers", 2, "每设备抽检并发数")
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	return a, nil
}

func run(argv []string) int {
	a, err := parseArgs(argv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	devices, name, err := loadDevices(a.configPath, a.profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取配置失败:", err)
		return 1
	}
	if len(devices) == 0 {
		fmt.Fprintln(os.Stderr, "没有可巡检设备")
		return 1
	}
	opts := model.DefaultScanOptions()
	opts.NoSearch = a.noSearch
	opts.DeepAVCheck = a.deep
	opts.DeviceWorkers = a.deviceWorkers
	opts.Workers = a.workers
	opts.Lookback = a.lookback
	opts.AVSeconds = a.avSeconds
	opts.AVWorkers = a.avWorkers

	// SIGTERM/SIGINT must abort the running round instead of waiting for it:
	// the context reaches every ISAPI request and kills ffmpeg children.
	ctx, signalExit, stopSignals := util.SignalContext()
	defer stopSignals()

	mon := monitor.New()
	agent := &agentRun{
		runner: scan.NewRunner(scan.Options{
			Quiet:           false,
			DefaultSaveRoot: filepath.Join(config.AppDataDir(), "av_samples"),
		}),
		mon:     mon,
		devices: devices,
		opts:    opts,
		name:    name,
		webhook: a.webhook,
		render:  a.once,
		out:     os.Stdout,
		errOut:  os.Stderr,
	}

	if a.once {
		agent.scanOnce(ctx)
		return signalExit()
	}

	token, err := resolveToken(os.Stdout, a.listen, a.token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "监听地址无效:", err)
		return 1
	}

	server := &http.Server{
		Addr:              a.listen,
		Handler:           monitor.HandlerWith(mon, monitor.Options{Token: token}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		fmt.Printf("NVR agent 监听 %s，间隔 %s\n", a.listen, a.interval)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	agent.scanOnce(ctx)
	if a.interval <= 0 {
		a.interval = 30 * time.Minute
	}
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdown)
			cancel()
			fmt.Println("已停止")
			return signalExit()
		case err := <-serveErr:
			fmt.Fprintln(os.Stderr, "HTTP 服务失败:", err)
			return 1
		case <-ticker.C:
			agent.scanOnce(ctx)
		}
	}
}

// agentRun is one agent process worth of state, split out from main so the
// round logic (metrics update, webhook ordering, cancellation) can be tested.
type agentRun struct {
	runner  *scan.Runner
	mon     *monitor.Monitor
	devices []model.Device
	opts    model.ScanOptions
	name    string
	webhook string
	render  bool
	out     io.Writer
	errOut  io.Writer

	// scan and notify are injection seams for tests; nil means the real thing.
	scan   func(ctx context.Context) []model.Report
	notify func(snap monitor.Snapshot)
}

// scanOnce runs one inspection round. It returns false when ctx was cancelled,
// leaving metrics and the webhook untouched so a half-finished round cannot
// masquerade as a real result.
func (a *agentRun) scanOnce(ctx context.Context) bool {
	start := time.Now()
	fmt.Fprintf(a.out, "[%s] 巡检 %s（%d 台，设备并发 %d）…\n",
		start.Format("15:04:05"), a.name, len(a.devices), a.opts.DeviceWorkers)

	reps := a.runScan(ctx)
	if ctx.Err() != nil {
		fmt.Fprintf(a.errOut, "[%s] 巡检已中断，本轮不更新指标、不发送 Webhook。\n",
			time.Now().Format("15:04:05"))
		return false
	}

	dur := time.Since(start)
	a.mon.Update(reps, dur)
	ok := 0
	for _, r := range reps {
		if r.Error == "" {
			ok++
		}
	}
	fmt.Fprintf(a.out, "[%s] 完成：%d/%d 台成功，耗时 %s\n", time.Now().Format("15:04:05"), ok, len(reps), dur.Round(time.Millisecond))
	if a.render {
		report.RenderAll(a.out, reps, false)
	}
	if a.webhook != "" {
		// Deliberately synchronous: --once returns right after this call, so a
		// detached goroutine would be killed before the POST goes out.
		a.send(a.mon.Snapshot())
	}
	return true
}

func (a *agentRun) runScan(ctx context.Context) []model.Report {
	if a.scan != nil {
		return a.scan(ctx)
	}
	return a.runner.QueueCtx(ctx, a.devices, a.opts, func(p model.Progress) {
		if p.Message != "" {
			fmt.Fprintln(a.errOut, "  ·", p.Message)
		}
	}, nil)
}

func (a *agentRun) send(snap monitor.Snapshot) {
	if a.notify != nil {
		a.notify(snap)
		return
	}
	status, err := postWebhook(a.webhook, snap)
	if err != nil {
		fmt.Fprintln(a.errOut, "Webhook 失败:", err)
		return
	}
	fmt.Fprintf(a.out, "Webhook 已发送：HTTP %d\n", status)
}

// resolveToken decides the HTTP token. Binding to a non-loopback address
// exposes device names, IPs and channel names to anyone who can reach the port,
// so an explicit token is used when given and a throwaway one is generated
// otherwise. Loopback keeps working without a token.
func resolveToken(out io.Writer, listen, token string) (string, error) {
	loop, err := isLoopbackAddr(listen)
	if err != nil {
		return "", err
	}
	if token != "" || loop {
		return token, nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token = hex.EncodeToString(buf)
	fmt.Fprintf(out, "⚠️ 监听地址 %s 不是回环地址，已生成本次启动有效的访问令牌：\n"+
		"    %s\n"+
		"    访问 /healthz、/metrics、/api/reports 需带 Authorization: Bearer <令牌>；\n"+
		"    如需固定令牌，下次用 --token 指定。\n", listen, token)
	return token, nil
}

// isLoopbackAddr reports whether addr only accepts connections from this host.
// An empty host (":9101") means all interfaces and is therefore not loopback.
func isLoopbackAddr(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	if host == "" {
		return false, nil
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, fmt.Errorf("无法解析主机 %q", host)
	}
	return ip.IsLoopback(), nil
}

func loadDevices(configPath, profile string) ([]model.Device, string, error) {
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, "", err
		}
		var legacy struct {
			Devices []model.Device `json:"devices"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, "", err
		}
		return legacy.Devices, filepath.Base(configPath), nil
	}
	st := config.New("")
	if err := st.Load(); err != nil {
		return nil, "", err
	}
	name := profile
	if name == "" {
		name = st.ActiveName()
	}
	devices, err := st.ResolveDevices(name)
	return devices, name, err
}

func webhookPayload(snap monitor.Snapshot) ([]byte, error) {
	return json.Marshal(map[string]any{
		"scanned_at": snap.ScannedAt,
		"healthy":    snap.Healthy(),
		"reports":    snap.Reports,
	})
}

// webhookBackoff is how long to wait before each retry: 3 attempts total.
// Overridable in tests.
var webhookBackoff = []time.Duration{time.Second, 2 * time.Second}

func postWebhook(url string, snap monitor.Snapshot) (int, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return postWebhookWith(client, url, snap, webhookBackoff)
}

// postWebhookWith POSTs the payload, retrying on transport errors and on
// 5xx/429. A 4xx means the receiver rejected the payload itself, so retrying
// would only repeat the same rejection.
func postWebhookWith(client *http.Client, url string, snap monitor.Snapshot, backoff []time.Duration) (int, error) {
	u, err := neturl.Parse(url)
	if err != nil {
		return 0, fmt.Errorf("Webhook URL 无效: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return 0, fmt.Errorf("Webhook URL 必须是 http(s) 地址: %s", url)
	}
	body, err := webhookPayload(snap)
	if err != nil {
		return 0, err
	}
	var lastStatus int
	var lastErr error
	for attempt := 0; attempt <= len(backoff); attempt++ {
		if attempt > 0 {
			time.Sleep(backoff[attempt-1])
		}
		status, err := postOnce(client, url, body)
		lastStatus, lastErr = status, err
		if err == nil {
			if status < 400 {
				return status, nil
			}
			if status < 500 && status != http.StatusTooManyRequests {
				// The receiver rejected the payload itself; retrying would just
				// repeat the same rejection.
				return status, fmt.Errorf("Webhook 返回 HTTP %d（不重试）", status)
			}
		}
	}
	if lastErr != nil {
		return lastStatus, lastErr
	}
	return lastStatus, fmt.Errorf("Webhook 返回 HTTP %d", lastStatus)
}

func postOnce(client *http.Client, url string, body []byte) (int, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	// Drain so the connection can be reused, then close.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
