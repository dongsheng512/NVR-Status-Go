package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

func TestParseArgsDefaults(t *testing.T) {
	a, err := parseArgs(nil)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.selector != "all" {
		t.Errorf("selector = %q, 期望 all", a.selector)
	}
	if a.port != 80 || a.username != "admin" {
		t.Errorf("port/username = %d/%q", a.port, a.username)
	}
	if a.lookback != 60 || a.workers != 8 {
		t.Errorf("lookback/workers = %d/%d", a.lookback, a.workers)
	}
	if a.avSeconds != 6 || a.avWorkers != 2 || a.avLimit != 0 {
		t.Errorf("av = %d/%d/%d", a.avSeconds, a.avWorkers, a.avLimit)
	}
	if a.silenceDB != -80 || a.busyStart != 10 || a.busyEnd != 18 || a.busyDaysAgo != 0 {
		t.Errorf("silence/busy = %v/%d/%d/%d", a.silenceDB, a.busyStart, a.busyEnd, a.busyDaysAgo)
	}
	if a.deepAV || a.avSave || a.noSearch || a.verbose || a.ssl || a.showVer {
		t.Error("布尔开关默认应为 false")
	}
}

func TestParseArgsSelectorAndAliases(t *testing.T) {
	a, err := parseArgs([]string{"2", "-ip", "192.0.2.10", "-w", "pw", "-p", "8000", "-u", "ops", "-s", "-v"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.selector != "2" {
		t.Errorf("selector = %q, 期望 2", a.selector)
	}
	if a.ip != "192.0.2.10" || a.password != "pw" || a.port != 8000 || a.username != "ops" {
		t.Errorf("单机参数未落到结构体: %+v", a)
	}
	if !a.ssl || !a.verbose {
		t.Error("-s / -v 未生效")
	}
}

func TestParseArgsLongAliasesMatchShort(t *testing.T) {
	short, err := parseArgs([]string{"-i", "192.0.2.10", "-w", "pw"})
	if err != nil {
		t.Fatal(err)
	}
	long, err := parseArgs([]string{"--ip", "192.0.2.10", "--password", "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if short.ip != long.ip || short.password != long.password {
		t.Fatalf("-i/--ip 或 -w/--password 不等价: %+v vs %+v", short, long)
	}
}

func TestParseArgsDeepAndAVOptions(t *testing.T) {
	a, err := parseArgs([]string{
		"--deep-av-check", "--av-seconds", "8", "--av-workers", "4", "--av-limit", "6",
		"--av-channels", "1,2,64", "--av-at", "14:00,15:30", "--silence-db", "-60",
		"--busy-start", "9", "--busy-end", "20", "--busy-days-ago", "2",
		"--no-search", "--lookback", "120", "--device-workers", "3",
	})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !a.deepAV || !a.noSearch {
		t.Error("布尔开关未生效")
	}
	if a.avSeconds != 8 || a.avWorkers != 4 || a.avLimit != 6 {
		t.Errorf("av = %d/%d/%d", a.avSeconds, a.avWorkers, a.avLimit)
	}
	if a.avChannels != "1,2,64" || a.avAt != "14:00,15:30" {
		t.Errorf("channels/at = %q/%q", a.avChannels, a.avAt)
	}
	if a.silenceDB != -60 || a.busyStart != 9 || a.busyEnd != 20 || a.busyDaysAgo != 2 {
		t.Errorf("silence/busy = %v/%d/%d/%d", a.silenceDB, a.busyStart, a.busyEnd, a.busyDaysAgo)
	}
	if a.lookback != 120 || a.deviceWorkers != 3 {
		t.Errorf("lookback/deviceWorkers = %d/%d", a.lookback, a.deviceWorkers)
	}
}

func TestParseArgsRejectsBadValues(t *testing.T) {
	for _, argv := range [][]string{
		{"--workers", "abc"},
		{"--silence-db", "loud"},
		{"--unknown-flag"},
	} {
		if _, err := parseArgs(argv); err == nil {
			t.Errorf("parseArgs(%v) 应报错", argv)
		}
	}
}

func TestSelectDevices(t *testing.T) {
	devices := []model.Device{{Name: "A", IP: "192.0.2.11"}, {Name: "B", IP: "192.0.2.12"}}
	cases := []struct {
		selector string
		want     int // 选中数量；-1 表示应报错
		pick     string
	}{
		{"", 2, ""},
		{"all", 2, ""},
		{"ALL", 2, ""},
		{"*", 2, ""},
		{"1", 1, "192.0.2.11"},
		{"2", 1, "192.0.2.12"},
		{"0", -1, ""},
		{"3", -1, ""},
		{"abc", -1, ""},
		{"-1", -1, ""},
	}
	for _, tc := range cases {
		got, err := selectDevices(devices, tc.selector)
		if tc.want == -1 {
			if err == nil {
				t.Errorf("selectDevices(%q) 应报错", tc.selector)
			}
			continue
		}
		if err != nil {
			t.Errorf("selectDevices(%q): %v", tc.selector, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("selectDevices(%q) 返回 %d 台, 期望 %d", tc.selector, len(got), tc.want)
			continue
		}
		if tc.pick != "" && got[0].IP != tc.pick {
			t.Errorf("selectDevices(%q) 选中 %s, 期望 %s", tc.selector, got[0].IP, tc.pick)
		}
	}
}

func TestSelectDevicesErrorListsChoices(t *testing.T) {
	_, err := selectDevices([]model.Device{{Name: "A", IP: "192.0.2.11"}}, "9")
	if err == nil {
		t.Fatal("越界编号应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "192.0.2.11") || !strings.Contains(msg, "1.") {
		t.Fatalf("错误信息应列出可用设备，实际: %s", msg)
	}
}

// TestExitCode: 0 正常 / 1 设备失败 / 2 无失败但有严重。
// 失败优先于严重——两台设备时一台连不上、另一台严重，用户要先修「连不上」。
func TestExitCode(t *testing.T) {
	cases := []struct {
		name    string
		reports []model.Report
		want    int
	}{
		{"空", nil, 0},
		{"全正常", []model.Report{{Health: model.HealthSummary{Status: "良好"}}}, 0},
		{"警告仍算成功", []model.Report{{Health: model.HealthSummary{Status: "警告"}}}, 0},
		{"严重", []model.Report{{Health: model.HealthSummary{Status: "严重"}}}, 2},
		{"设备失败", []model.Report{{Error: "timeout"}}, 1},
		{"失败优先于严重", []model.Report{
			{Health: model.HealthSummary{Status: "严重"}},
			{Error: "timeout"},
		}, 1},
	}
	for _, tc := range cases {
		if got := exitCode(tc.reports); got != tc.want {
			t.Errorf("%s: exitCode = %d, 期望 %d", tc.name, got, tc.want)
		}
	}
}

func writeLegacyConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nvr_config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDevicesLegacyConfig(t *testing.T) {
	path := writeLegacyConfig(t,
		`{"devices":[{"name":"A","ip":"192.0.2.11","port":80,"username":"admin","password":"x"}]}`)
	devices, err := loadDevices(&cliArgs{configPath: path})
	if err != nil {
		t.Fatalf("loadDevices: %v", err)
	}
	if len(devices) != 1 || devices[0].IP != "192.0.2.11" || devices[0].Name != "A" {
		t.Fatalf("devices = %+v", devices)
	}
}

func TestLoadDevicesRejectsEmptyAndBrokenConfig(t *testing.T) {
	empty := writeLegacyConfig(t, `{"devices":[]}`)
	if _, err := loadDevices(&cliArgs{configPath: empty}); err == nil {
		t.Error("空 devices 应报错")
	}
	broken := writeLegacyConfig(t, `{not json}`)
	if _, err := loadDevices(&cliArgs{configPath: broken}); err == nil {
		t.Error("无法解析的配置应报错")
	}
	if _, err := loadDevices(&cliArgs{configPath: filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Error("文件不存在应报错")
	}
}

func TestRunVersionAndMissingPassword(t *testing.T) {
	if code := run([]string{"--version"}); code != 0 {
		t.Errorf("--version 退出码 = %d, 期望 0", code)
	}
	// 单机模式缺密码：必须在连网之前就拒绝。
	if code := run([]string{"-i", "192.0.2.10"}); code != 1 {
		t.Errorf("缺密码时退出码 = %d, 期望 1", code)
	}
	if code := run([]string{"-w", "pw"}); code != 1 {
		t.Errorf("缺 IP 时退出码 = %d, 期望 1", code)
	}
}
