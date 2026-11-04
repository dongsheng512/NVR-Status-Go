package util

import (
	"reflect"
	"testing"
	"time"
)

// ParseChannelList 只接受 string。原先的 int/[]int/bool/float 分支是为了兼容
// Python 的鸭子类型（None/str/list 都可能），已删除 —— 现在传错类型是编译错误，
// 不需要运行时用例。
func TestParseChannelList(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []int
		wantErr bool
	}{
		{"empty", "", nil, false},
		{"spaces", "   ", nil, false},
		{"comma", "31,64", []int{31, 64}, false},
		{"space", "31 64", []int{31, 64}, false},
		{"ideographic comma", "31、64", []int{31, 64}, false},
		{"fullwidth comma", "31，64", []int{31, 64}, false},
		{"semicolon", "31;64", []int{31, 64}, false},
		{"dedupe", "31,31,64", []int{31, 64}, false},
		{"bad", "abc", nil, true},
		{"partial bad", "31,abc", nil, true},
		{"zero", "0", nil, true},
		{"negative", "-3", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChannelList(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

// ParseInstantList 同样只接受 string。已经持有 []time.Time 的调用方
// （model.ScanOptions.AVAt）直接赋值，不需要经过解析器。
func TestParseInstantList(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.Local)
	tests := []struct {
		name    string
		in      string
		daysAgo int
		want    []string
		wantErr bool
	}{
		{"empty", "", 0, nil, false},
		{"one", "09:05", 0, []string{"09:05"}, false},
		{"comma", "09:05,08:30", 0, []string{"09:05", "08:30"}, false},
		{"ideographic", "09:05、08:30", 0, []string{"09:05", "08:30"}, false},
		{"dedupe keeps order", "09:05,09:05,08:30", 0, []string{"09:05", "08:30"}, false},
		{"small hour", "9:05", 0, []string{"09:05"}, false},
		{"days ago", "19:10", 1, []string{"19:10"}, false},
		{"future", "23:00", 0, nil, true},
		{"bad minute", "09:5x", 0, nil, true},
		{"out of range", "25:00", 0, nil, true},
		{"bad format", "0905", 0, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInstantList(tt.in, tt.daysAgo, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			var labels []string
			for _, tm := range got {
				labels = append(labels, tm.In(time.Local).Format("15:04"))
			}
			if !reflect.DeepEqual(labels, tt.want) {
				t.Fatalf("got %v want %v", labels, tt.want)
			}
			if tt.daysAgo > 0 && len(got) > 0 {
				wantDate := now.AddDate(0, 0, -tt.daysAgo).Format("2006-01-02")
				if d := got[0].In(time.Local).Format("2006-01-02"); d != wantDate {
					t.Fatalf("date got %s want %s", d, wantDate)
				}
			}
		})
	}
}

// 同一个 now 下重复解析必须给出完全相同的结果（纯函数，不依赖调用次数或
// 进程状态）。原先这里靠「把上次的 []time.Time 再喂回去」验证，那是鸭子类型
// 才需要的路径；现在直接重复解析同一串输入即可。
func TestParseInstantListIdempotent(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.Local)
	first, err := ParseInstantList("09:05,08:30", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseInstantList("09:05,08:30", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("len got %d want %d", len(second), len(first))
	}
	for i := range first {
		if !first[i].Equal(second[i]) {
			t.Fatalf("at %d got %v want %v", i, second[i], first[i])
		}
	}
}

func TestLatestAVSampleInstant(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	got := LatestAVSampleInstant(now)
	want := now.Add(-10 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAlternateClipTimes(t *testing.T) {
	// 4-hour busy window, avoid at 14:00, cutoff well after.
	winS := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	winE := time.Date(2026, 9, 11, 18, 0, 0, 0, time.UTC)
	avoid := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 9, 11, 23, 0, 0, 0, time.UTC)
	s, e, ok := AlternateClipTimes(winS, winE, 6, avoid, cutoff)
	if !ok {
		t.Fatal("expected an alternate window")
	}
	if e.Sub(s) != 6*time.Second {
		t.Fatalf("duration %v", e.Sub(s))
	}
	gap := s.Sub(avoid)
	if gap < 0 {
		gap = -gap
	}
	if gap < 30*time.Minute {
		t.Fatalf("gap %v < 30m", gap)
	}
	if s.Before(winS) || e.After(winE) {
		t.Fatalf("window out of range: %v..%v", s, e)
	}

	// Too-short window: no alternate.
	shortE := winS.Add(10 * time.Minute)
	if _, _, ok := AlternateClipTimes(winS, shortE, 6, winS, cutoff); ok {
		t.Fatal("expected no alternate for short window")
	}
}

func TestParseHikTime(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	z := ParseHikTime("2026-09-11T10:00:00Z", loc)
	if z == nil || !z.Equal(time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("Z parse: %v", z)
	}
	local := ParseHikTime("2026-09-11T10:00:00", loc)
	want := time.Date(2026, 9, 11, 2, 0, 0, 0, time.UTC)
	if local == nil || !local.Equal(want) {
		t.Fatalf("local parse: got %v want %v", local, want)
	}
	offset := ParseHikTime("2026-09-11T10:00:00+08:00", loc)
	if offset == nil || !offset.Equal(want) {
		t.Fatalf("offset parse: got %v want %v", offset, want)
	}
	if ParseHikTime("", loc) != nil {
		t.Fatal("empty should be nil")
	}
}

func TestSafeFilename(t *testing.T) {
	tests := map[string]string{
		"a/b:c*d?":        "a_b_c_d",
		"  hello  world ": "hello_world",
		"":                "unknown",
		"___":             "unknown",
	}
	for in, want := range tests {
		if got := SafeFilename(in, 60); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
	long := SafeFilename("abcdef", 3)
	if long != "abc" {
		t.Fatalf("long: %q", long)
	}
}

func TestParsePercent(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"98.0%", 98, true},
		{"53%", 53, true},
		{" 45 % ", 45, true},
		{"0", 0, true},
		{"  ", 0, false},
		{"", 0, false},
		{"未知", 0, false},
		{"-", 0, false},
		// 中间的 % 说明文本本身是坏的；删掉它会变成看似合理的数字
		{"9%8", 0, false},
	}
	for _, c := range cases {
		got, ok := ParsePercent(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParsePercent(%q) = (%v, %v), 期望 (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
