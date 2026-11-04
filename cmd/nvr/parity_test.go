package main

// CLI ↔ ScanOptions 的一致性保障。
//
// cmd/nvr 与 internal/nvgui 共用 model.ScanOptions。业务逻辑改在 internal/ 下时
// 两边自动同步；真正会漂移的是「新增一个 ScanOptions 字段，GUI 的表单接了、CLI 忘了加
// 开关」。这几条测试让这种遗漏变成编译后立刻失败的用例，而不是等某天发现 CLI 少做了一件事。

import (
	"flag"
	"reflect"
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

// cliExempt 记录故意不提供命令行开关的字段。新增字段时必须二选一：
// 在 newFlagSet 里加开关并在 cliDistinctValues 里给出区分值，或在这里写明原因。
var cliExempt = map[string]string{
	"lookback_unit": "GUI 的时间单位下拉，仅影响界面显示；CLI 的时间窗口固定按分钟传",
}

// cliDistinctValues 给出每个开关一个与默认值不同的取值，用于验证它确实写进了 options。
var cliDistinctValues = map[string]string{
	"lookback":       "90",
	"no_search":      "true",
	"workers":        "5",
	"device_workers": "3",
	"deep_av_check":  "true",
	"av_seconds":     "9",
	"av_workers":     "4",
	"av_limit":       "7",
	"silence_db":     "-55",
	"busy_start":     "9",
	"busy_end":       "17",
	"busy_days_ago":  "2",
	"av_save":        "true",
	"av_save_root":   "/tmp/nvrstatus-av-samples",
	"av_channels":    "31,64",
	"av_at":          "09:30",
}

// scanOptionFields 返回 ScanOptions 的 json 字段名（下划线形式）。
func scanOptionFields(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(model.ScanOptions{})
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			t.Fatalf("model.ScanOptions.%s 缺少 json tag，无法与 CLI 开关对应", typ.Field(i).Name)
		}
		names = append(names, name)
	}
	return names
}

func flagNameFor(jsonName string) string { return strings.ReplaceAll(jsonName, "_", "-") }

func registeredFlags() map[string]bool {
	a := &cliArgs{}
	fs := newFlagSet(a)
	out := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { out[f.Name] = true })
	return out
}

// 每个 ScanOptions 字段要么有自己的开关，要么在豁免表里；豁免表也不能留过期条目
// （字段被删/改名后仍挂在表里，会让后来人以为它还被刻意排除）。
func TestEveryScanOptionHasACLIFlagOrIsExempt(t *testing.T) {
	fields := scanOptionFields(t)
	flags := registeredFlags()
	present := make(map[string]bool, len(fields))

	for _, name := range fields {
		present[name] = true
		if reason, ok := cliExempt[name]; ok {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s 在 cliExempt 中但没写原因", name)
			}
			continue
		}
		if !flags[flagNameFor(name)] {
			t.Errorf("model.ScanOptions 有字段 %q，但 CLI 没有 --%s 开关。\n"+
				"要么在 newFlagSet 里加开关（并在 cliDistinctValues 补一个区分值），"+
				"要么在 cliExempt 里登记不提供的理由。", name, flagNameFor(name))
		}
	}

	for name, reason := range cliExempt {
		if !present[name] {
			t.Errorf("cliExempt 里的 %q 已不存在于 model.ScanOptions（原因: %s），请删除该条目", name, reason)
		}
	}
	for name := range cliDistinctValues {
		if !present[name] {
			t.Errorf("cliDistinctValues 里的 %q 已不存在于 model.ScanOptions，请删除该条目", name)
		}
	}
}

// 开关存在还不够 —— 还要真的写进 options。漏掉 buildOptions 里的一行赋值，
// 上面那条测试是绿的，但用户传了参数毫无效果。
func TestEveryScanOptionFlagReachesOptions(t *testing.T) {
	def := model.DefaultScanOptions()

	argv := make([]string, 0, len(cliDistinctValues))
	for name, value := range cliDistinctValues {
		argv = append(argv, "--"+flagNameFor(name)+"="+value)
	}

	a, err := parseArgs(argv)
	if err != nil {
		t.Fatalf("parseArgs(%v): %v", argv, err)
	}

	var warnings strings.Builder
	got := buildOptions(a, &warnings)
	if warnings.Len() > 0 {
		t.Errorf("全套合法参数不应产生警告，实际: %s", warnings.String())
	}

	dv := reflect.ValueOf(got)
	df := reflect.ValueOf(def)
	typ := dv.Type()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		jsonName := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if _, exempt := cliExempt[jsonName]; exempt {
			continue
		}
		if reflect.DeepEqual(dv.Field(i).Interface(), df.Field(i).Interface()) {
			t.Errorf("传了 --%s 但 ScanOptions.%s 仍是默认值 %v：buildOptions 里漏了赋值",
				flagNameFor(jsonName), name, df.Field(i).Interface())
		}
	}
}

// 未启用深度抽检时，--av-channels/--av-at 必须给出提示而不是静默丢弃
// （静默丢弃会让人以为查过某几路，其实查的是全部）。
func TestAVFiltersWarnWhenDeepCheckIsOff(t *testing.T) {
	a, err := parseArgs([]string{"--av-channels=31,64", "--av-at=09:30"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	var warnings strings.Builder
	opts := buildOptions(a, &warnings)

	if opts.DeepAVCheck {
		t.Fatal("未传 --deep-av-check/--av-save，DeepAVCheck 应为 false")
	}
	if opts.AVChannels != nil || opts.AVAt != nil {
		t.Errorf("深度抽检关闭时不应设置过滤条件，得到 %v / %v", opts.AVChannels, opts.AVAt)
	}
	out := warnings.String()
	if !strings.Contains(out, "--av-channels 已忽略") || !strings.Contains(out, "--av-at 已忽略") {
		t.Errorf("缺少忽略提示，实际输出: %q", out)
	}
}

// 非法通道号必须报错并退回全部通道，不能静默降级成空列表。
func TestInvalidAVChannelsFallsBackWithWarning(t *testing.T) {
	a, err := parseArgs([]string{"--deep-av-check", "--av-channels=abc"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	var warnings strings.Builder
	opts := buildOptions(a, &warnings)

	if opts.AVChannels != nil {
		t.Errorf("非法通道号不应写入 AVChannels，得到 %v", opts.AVChannels)
	}
	if !strings.Contains(warnings.String(), "抽检通道过滤无效") {
		t.Errorf("缺少无效通道提示，实际输出: %q", warnings.String())
	}
}
