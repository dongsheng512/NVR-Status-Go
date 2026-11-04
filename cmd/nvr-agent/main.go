// Command nvr-agent runs scheduled NVR scans headlessly and exposes the
// latest results as /healthz and Prometheus /metrics.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"nvrstatus/internal/config"
	"nvrstatus/internal/model"
	"nvrstatus/internal/monitor"
	"nvrstatus/internal/report"
	"nvrstatus/internal/scan"
)

func main() {
	var (
		profile       = flag.String("profile", "", "profiles.json 档案名（默认当前档案）")
		configPath    = flag.String("config", "", "legacy nvr_config.json 路径")
		listen        = flag.String("listen", "127.0.0.1:9101", "HTTP 监听地址")
		interval      = flag.Duration("interval", 30*time.Minute, "巡检间隔")
		once          = flag.Bool("once", false, "只巡检一次后退出")
		webhook       = flag.String("webhook", "", "巡检完成后的 Webhook URL（POST JSON）")
		noSearch      = flag.Bool("no-search", false, "跳过 CMSearch 落盘检查")
		deep          = flag.Bool("deep-av-check", false, "启用深度音视频抽检")
		deviceWorkers = flag.Int("device-workers", 2, "设备并发数(1-4)")
		workers       = flag.Int("workers", 8, "单设备落盘检查并发数")
		lookback      = flag.Int("lookback", 60, "落盘检查窗口(分钟)")
		avSeconds     = flag.Int("av-seconds", 6, "每路抽检时长(秒)")
		avWorkers     = flag.Int("av-workers", 2, "每设备抽检并发数")
	)
	flag.Parse()

	devices, name, err := loadDevices(*configPath, *profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取配置失败:", err)
		os.Exit(1)
	}
	if len(devices) == 0 {
		fmt.Fprintln(os.Stderr, "没有可巡检设备")
		os.Exit(1)
	}
	opts := model.DefaultScanOptions()
	opts.NoSearch = *noSearch
	opts.DeepAVCheck = *deep
	opts.DeviceWorkers = *deviceWorkers
	opts.Workers = *workers
	opts.Lookback = *lookback
	opts.AVSeconds = *avSeconds
	opts.AVWorkers = *avWorkers

	runner := scan.NewRunner(scan.Options{
		Quiet:           false,
		DefaultSaveRoot: filepath.Join(config.AppDataDir(), "av_samples"),
	})
	mon := monitor.New()
	server := &http.Server{Addr: *listen, Handler: monitor.Handler(mon)}

	runOnce := func() {
		start := time.Now()
		fmt.Printf("[%s] 巡检 %s（%d 台，设备并发 %d）…\n",
			start.Format("15:04:05"), name, len(devices), opts.DeviceWorkers)
		reps := runner.Queue(devices, opts, func(p model.Progress) {
			if p.Message != "" {
				fmt.Fprintln(os.Stderr, "  ·", p.Message)
			}
		}, nil)
		dur := time.Since(start)
		mon.Update(reps, dur)
		ok := 0
		for _, r := range reps {
			if r.Error == "" {
				ok++
			}
		}
		fmt.Printf("[%s] 完成：%d/%d 台成功，耗时 %s\n", time.Now().Format("15:04:05"), ok, len(reps), dur.Round(time.Millisecond))
		if *once {
			report.RenderAll(os.Stdout, reps, false)
		}
		if *webhook != "" {
			go postWebhook(*webhook, mon.Snapshot())
		}
	}

	if *once {
		runOnce()
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		fmt.Printf("NVR agent 监听 %s，间隔 %s\n", *listen, *interval)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "HTTP 服务失败:", err)
		}
	}()
	runOnce()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
			fmt.Println("已停止")
			return
		case <-ticker.C:
			runOnce()
		}
	}
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

func postWebhook(url string, snap monitor.Snapshot) {
	body, err := json.Marshal(map[string]any{
		"scanned_at": snap.ScannedAt,
		"healthy":    snap.Healthy(),
		"reports":    snap.Reports,
	})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Webhook 失败:", err)
		return
	}
	resp.Body.Close()
	fmt.Printf("Webhook 已发送：HTTP %d\n", resp.StatusCode)
}
