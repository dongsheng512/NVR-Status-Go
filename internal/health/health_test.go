package health

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"nvrstatus/internal/model"
)

// ── helpers ──────────────────────────────────────────────────────────

func boolPtr(v bool) *bool { return &v }

func overwriteEnabled() model.DiskOverwrite {
	return model.DiskOverwrite{Enabled: boolPtr(true), Label: "已开启"}
}

func overwriteDisabled() model.DiskOverwrite {
	return model.DiskOverwrite{Enabled: boolPtr(false), Label: "未开启"}
}

func overwriteUnknown() model.DiskOverwrite {
	return model.DiskOverwrite{Label: "未知"}
}

func camera(ch int, name, online, detect string) model.Camera {
	if name == "" {
		name = fmt.Sprintf("通道%d", ch)
	}
	return model.Camera{
		ID:          strconv.Itoa(ch),
		Name:        name,
		IP:          "192.168.1.10",
		Model:       "DS-2CD",
		Online:      online,
		DetectState: detect,
	}
}

// record builds a "healthy unless the field under test says otherwise"
// recording row, mirroring tests/test_health_unknown_policy.py's _rec().
func record(ch int, disk string) model.Record {
	r := model.Record{
		Channel:          strconv.Itoa(ch),
		Name:             fmt.Sprintf("通道%d", ch),
		RecordingEnabled: boolPtr(true),
		RecordingOK:      model.StatusOK,
		HasAudio:         boolPtr(true),
		DiskStatus:       disk,
		VideoProbe:       model.StatusSkip,
		AudioProbe:       model.StatusSkip,
	}
	if disk == model.StatusSkip {
		// No disk search was run for this track: recording is "跳过" too.
		r.RecordingOK = model.StatusSkip
	}
	return r
}

func drive(name, status, usage string) model.Drive {
	return model.Drive{Name: name, Status: status, Usage: usage}
}

func containsWarning(warnings []string, sub string) bool {
	for _, w := range warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// ── classify / detect reason ────────────────────────────────────────

func TestClassifyDetectStateWhitelist(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"connect", "connect", detectOnline},
		{"Connect", "Connect", detectOnline},
		{"connected", "connected", detectOnline},
		{"normal", "normal", detectOnline},
		{"ok", "ok", detectOnline},
		{"online", "online", detectOnline},
		{"已配置", "已配置", detectOnline},
		{"notExist", "notExist", model.StatusBad},
		{"NOTEXIST", "NOTEXIST", model.StatusBad},
		{"not_exist separator", "not_exist", model.StatusBad},
		{"not-exist separator", "not-exist", model.StatusBad},
		{"space separator", "not exist", model.StatusBad},
		{"disconnect", "disconnect", model.StatusBad},
		{"neterror", "netError", model.StatusBad},
		{"networkerror", "networkError", model.StatusBad},
		{"ipConflict", "ipConflict", model.StatusBad},
		{"ip address conflict", "ip_address_conflict", model.StatusBad},
		{"userPwError", "userPwError", model.StatusBad},
		{"passwordError", "passwordError", model.StatusBad},
		{"userLocked", "userLocked", model.StatusBad},
		{"locked", "locked", model.StatusBad},
		{"unsupported", "unsupported", model.StatusBad},
		{"notSupport", "notSupport", model.StatusBad},
		{"unknown", "unknown", model.StatusUnknown},
		{"empty", "", model.StatusUnknown},
		{"spaces", "   ", model.StatusUnknown},
		{"unseen firmware value", "someNewFirmwareValue", model.StatusUnknown},
		{"chinese unseen", "某个没见过的新取值", model.StatusUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyDetectState(tt.raw); got != tt.want {
				t.Fatalf("ClassifyDetectState(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestDetectReasonMapping(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"notExist", "notExist", "通道未接入"},
		{"not_exist", "not_exist", "通道未接入"},
		{"disconnect", "disconnect", "连接断开"},
		{"neterror", "netError", "网络不可达"},
		{"ipConflict", "ipConflict", "IP 冲突"},
		{"userPwError", "userPwError", "用户名或密码错误"},
		{"userLocked", "userLocked", "用户被锁定"},
		{"unsupported", "unsupported", "不支持"},
		{"unknown value falls back to raw", "weirdValue", "weirdValue"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectReason(tt.raw); got != tt.want {
				t.Fatalf("DetectReason(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// ── camera online / offline rules ───────────────────────────────────

func TestCameraOnlineVerdicts(t *testing.T) {
	tests := []struct {
		name          string
		cameras       []model.Camera
		wantStatus    string
		wantOnline    int
		wantOffline   int
		wantUnconf    int
		wantDetectBad int
		wantWarnings  []string
		wantNoWarning bool
	}{
		{
			name:         "online missing detect notExist counts offline",
			cameras:      []model.Camera{camera(1, "前门", "", "notExist")},
			wantStatus:   statusSevere,
			wantOffline:  1,
			wantWarnings: []string{"1个摄像头离线", "通道未接入 1"},
		},
		{
			name:          "online missing detect connect counts online",
			cameras:       []model.Camera{camera(1, "", "", "connect")},
			wantStatus:    statusGood,
			wantOnline:    1,
			wantNoWarning: true,
		},
		{
			name:          "unknown detect value is not judged",
			cameras:       []model.Camera{camera(1, "", "", "someNewFirmwareValue")},
			wantStatus:    statusGood,
			wantUnconf:    1,
			wantNoWarning: true,
		},
		{
			name:         "online false takes precedence over connect",
			cameras:      []model.Camera{camera(1, "", "false", "connect")},
			wantStatus:   statusSevere,
			wantOffline:  1,
			wantWarnings: []string{"1个摄像头离线(通道1)"},
		},
		{
			name:         "online false counts without detect",
			cameras:      []model.Camera{camera(1, "", "false", "unknown")},
			wantStatus:   statusSevere,
			wantOffline:  1,
			wantWarnings: []string{"1个摄像头离线"},
		},
		{
			name:          "online true with abnormal detect adds warning",
			cameras:       []model.Camera{camera(1, "后门", "true", "netError")},
			wantStatus:    statusWarning,
			wantOnline:    1,
			wantDetectBad: 1,
			wantWarnings:  []string{"1个在线通道检测状态异常", "IP 冲突 / 网络不可达等"},
		},
		{
			name:          "online true with normal detect no warning",
			cameras:       []model.Camera{camera(1, "", "true", "connect")},
			wantStatus:    statusGood,
			wantOnline:    1,
			wantNoWarning: true,
		},
		{
			name: "offline warning breaks down reasons",
			cameras: []model.Camera{
				camera(1, "通道1", "false", "notExist"),
				camera(2, "通道2", "false", "notExist"),
				camera(3, "通道3", "false", "netError"),
			},
			wantStatus:   statusSevere,
			wantOffline:  3,
			wantWarnings: []string{"3个摄像头离线：通道未接入 2、网络不可达 1"},
		},
		{
			name:         "offline without detect falls back to names",
			cameras:      []model.Camera{camera(1, "前门", "false", "unknown")},
			wantStatus:   statusSevere,
			wantOffline:  1,
			wantWarnings: []string{"1个摄像头离线(前门)"},
		},
		{
			name: "detect state alone does not invent cameras",
			cameras: []model.Camera{
				camera(1, "", "true", "connect"),
				camera(2, "", "false", "notExist"),
				camera(3, "", "", "connect"),
				camera(4, "", "", "notExist"),
				camera(5, "", "", "mystery"),
			},
			wantStatus:   statusSevere,
			wantOnline:   2,
			wantOffline:  2,
			wantUnconf:   1,
			wantWarnings: []string{"2个摄像头离线：通道未接入 2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := Summarize(tt.cameras, nil, nil, overwriteEnabled(), false)
			if summary.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (warnings=%v)", summary.Status, tt.wantStatus, summary.Warnings)
			}
			stats := summary.Stats
			if stats.Online != tt.wantOnline || stats.Offline != tt.wantOffline ||
				stats.Unconfirmed != tt.wantUnconf || stats.DetectBad != tt.wantDetectBad {
				t.Fatalf("online/offline/unconfirmed/detectBad = %d/%d/%d/%d, want %d/%d/%d/%d",
					stats.Online, stats.Offline, stats.Unconfirmed, stats.DetectBad,
					tt.wantOnline, tt.wantOffline, tt.wantUnconf, tt.wantDetectBad)
			}
			if stats.CameraTotal != len(tt.cameras) {
				t.Fatalf("CameraTotal = %d, want %d", stats.CameraTotal, len(tt.cameras))
			}
			if stats.Online+stats.Offline+stats.Unconfirmed != stats.CameraTotal {
				t.Fatalf("online+offline+unconfirmed = %d, want CameraTotal %d",
					stats.Online+stats.Offline+stats.Unconfirmed, stats.CameraTotal)
			}
			for _, want := range tt.wantWarnings {
				if !containsWarning(summary.Warnings, want) {
					t.Fatalf("warnings %v do not contain %q", summary.Warnings, want)
				}
			}
			if tt.wantNoWarning && len(summary.Warnings) != 0 {
				t.Fatalf("warnings = %v, want none", summary.Warnings)
			}
		})
	}
}

// ── drive health ────────────────────────────────────────────────────

func TestDriveChecks(t *testing.T) {
	tests := []struct {
		name          string
		drives        []model.Drive
		overwrite     model.DiskOverwrite
		wantStatus    string
		wantWarnings  []string
		wantNoWarning bool
	}{
		{
			name:         "bad status is severe",
			drives:       []model.Drive{drive("hdd1", "error", "20%")},
			overwrite:    overwriteEnabled(),
			wantStatus:   statusSevere,
			wantWarnings: []string{"1块硬盘状态异常"},
		},
		{
			name:         "two bad statuses counted once",
			drives:       []model.Drive{drive("hdd1", "error", "20%"), drive("hdd2", "abnormal", "30%")},
			overwrite:    overwriteEnabled(),
			wantStatus:   statusSevere,
			wantWarnings: []string{"2块硬盘状态异常"},
		},
		{
			name:          "unknown status is not bad",
			drives:        []model.Drive{drive("hdd1", "unknown", "20%")},
			overwrite:     overwriteEnabled(),
			wantStatus:    statusGood,
			wantNoWarning: true,
		},
		{
			name:          "chinese unknown status is not bad",
			drives:        []model.Drive{drive("hdd1", "未知", "20%")},
			overwrite:     overwriteEnabled(),
			wantStatus:    statusGood,
			wantNoWarning: true,
		},
		{
			name:          "missing status is not bad",
			drives:        []model.Drive{drive("hdd1", "", "20%")},
			overwrite:     overwriteEnabled(),
			wantStatus:    statusGood,
			wantNoWarning: true,
		},
		{
			name:          "status comparison is case insensitive",
			drives:        []model.Drive{drive("hdd1", "OK", "20%")},
			overwrite:     overwriteEnabled(),
			wantStatus:    statusGood,
			wantNoWarning: true,
		},
		{
			name:         "sleeping drive is reported but does not degrade",
			drives:       []model.Drive{drive("hdd1", "sleep", "10%")},
			overwrite:    overwriteEnabled(),
			wantStatus:   statusGood,
			wantWarnings: []string{"1块硬盘处于休眠状态"},
		},
		{
			name:         "usage 95 with overwrite enabled is full warning",
			drives:       []model.Drive{drive("hdd1", "ok", "95.0%")},
			overwrite:    overwriteEnabled(),
			wantStatus:   statusWarning,
			wantWarnings: []string{"1块硬盘空间已满/即将用尽（循环覆盖已开启，将覆盖旧录像继续录）"},
		},
		{
			name:          "usage 94.9 is not full",
			drives:        []model.Drive{drive("hdd1", "ok", "94.9%")},
			overwrite:     overwriteEnabled(),
			wantStatus:    statusGood,
			wantNoWarning: true,
		},
		{
			name:          "unknown usage is not full",
			drives:        []model.Drive{drive("hdd1", "ok", "未知")},
			overwrite:     overwriteEnabled(),
			wantStatus:    statusGood,
			wantNoWarning: true,
		},
		{
			name:          "missing usage is not full",
			drives:        []model.Drive{drive("hdd1", "ok", "")},
			overwrite:     overwriteEnabled(),
			wantStatus:    statusGood,
			wantNoWarning: true,
		},
		{
			name:         "full disk without overwrite is severe",
			drives:       []model.Drive{drive("hdd1", "ok", "100.0%")},
			overwrite:    overwriteDisabled(),
			wantStatus:   statusSevere,
			wantWarnings: []string{"1块硬盘空间已满/即将用尽，且循环覆盖未开启，满盘后可能停止录像"},
		},
		{
			name:         "full disk with unknown overwrite asks for confirmation",
			drives:       []model.Drive{drive("hdd1", "ok", "99%")},
			overwrite:    overwriteUnknown(),
			wantStatus:   statusWarning,
			wantWarnings: []string{"1块硬盘空间已满/即将用尽（循环覆盖状态未知，请人工确认）"},
		},
		{
			name:         "not full disk without overwrite warns",
			drives:       []model.Drive{drive("hdd1", "ok", "50%")},
			overwrite:    overwriteDisabled(),
			wantStatus:   statusWarning,
			wantWarnings: []string{"循环覆盖未开启：硬盘写满后将停止录像，建议在 NVR 存储设置中开启"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := Summarize(nil, nil, tt.drives, tt.overwrite, false)
			if summary.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (warnings=%v)", summary.Status, tt.wantStatus, summary.Warnings)
			}
			for _, want := range tt.wantWarnings {
				if !containsWarning(summary.Warnings, want) {
					t.Fatalf("warnings %v do not contain %q", summary.Warnings, want)
				}
			}
			if tt.wantNoWarning && len(summary.Warnings) != 0 {
				t.Fatalf("warnings = %v, want none", summary.Warnings)
			}
		})
	}
}

// ── record counters (metric denominators) ───────────────────────────

func TestMetricDenominatorFollowsRecordPopulation(t *testing.T) {
	cameras := []model.Camera{
		camera(1, "cam1", "true", "connect"),
		camera(2, "cam2", "true", "connect"),
		camera(3, "cam3", "true", "connect"),
		camera(4, "cam4", "true", "connect"),
	}
	records := make([]model.Record, 0, 6)
	for i := 1; i <= 6; i++ {
		r := record(i, model.StatusOK)
		if i == 6 {
			r.RecordingOK = model.StatusBad
		}
		records = append(records, r)
	}

	summary := Summarize(cameras, records, nil, overwriteEnabled(), false)
	stats := summary.Stats
	if stats.CameraTotal != 4 || stats.Online != 4 {
		t.Fatalf("camera counters = total %d / online %d, want 4/4", stats.CameraTotal, stats.Online)
	}
	if stats.ChannelTotal != 6 {
		t.Fatalf("ChannelTotal = %d, want 6 (record population)", stats.ChannelTotal)
	}
	if stats.RecordingOK != 5 || stats.RecordingBad != 1 {
		t.Fatalf("recording counters = ok %d / bad %d, want 5/1", stats.RecordingOK, stats.RecordingBad)
	}
	if stats.DiskOK != 6 || stats.HasAudio != 6 {
		t.Fatalf("disk/audio counters = disk ok %d / has audio %d, want 6/6", stats.DiskOK, stats.HasAudio)
	}
	if summary.Status != statusGood {
		t.Fatalf("status = %q, want %q (warnings=%v)", summary.Status, statusGood, summary.Warnings)
	}
}

func TestRecordCounterBreakdown(t *testing.T) {
	recA := record(1, model.StatusSkip) // skip: no disk search
	recA.RecordingOK = model.StatusSkip

	recB := record(2, model.StatusUnknown)
	recB.RecordingOK = model.StatusUnknown
	recB.HasAudio = boolPtr(false)
	recB.VideoProbe = model.StatusOK
	recB.AudioProbe = model.StatusUnknown

	recC := model.Record{ // plan missing
		Channel:          "3",
		Name:             "通道3",
		RecordingEnabled: boolPtr(false),
		RecordingOK:      "未配置",
		HasAudio:         nil,
		DiskStatus:       model.StatusSkip,
	}
	recD := record(4, model.StatusBad)
	recD.RecordingOK = model.StatusBad
	recD.VideoProbe = model.StatusBad
	recD.AudioProbe = model.StatusBad
	recE := record(5, model.StatusOK)
	recE.HasAudio = nil
	recE.VideoProbe = model.StatusOK
	recE.AudioProbe = model.StatusWarn

	summary := Summarize(nil, []model.Record{recA, recB, recC, recD, recE}, nil, overwriteEnabled(), false)
	want := model.HealthStats{
		ChannelTotal:      5,
		PlanConfigured:    4,
		PlanMissing:       1,
		RecordingOK:       1,
		RecordingBad:      2,
		RecordingUnknown:  1,
		RecordingSkip:     1,
		HasAudio:          2,
		NoAudio:           1,
		AudioUnknown:      2,
		DiskOK:            1,
		DiskBad:           1,
		DiskUnknown:       1,
		DiskSkip:          2,
		DiskChecked:       true,
		RecordingChecked:  true,
		VideoProbeOK:      2,
		VideoProbeBad:     1,
		AudioProbeOK:      0,
		AudioProbeBad:     1,
		AudioProbeWarn:    1,
		AudioProbeUnknown: 1,
		CameraTotal:       0,
		DeepAV:            false,
		OverwriteLabel:    "已开启",
		OverwriteEnabled:  boolPtr(true),
	}
	if !reflect.DeepEqual(summary.Stats, want) {
		t.Fatalf("stats mismatch:\n got: %+v\nwant: %+v", summary.Stats, want)
	}
	if summary.Status != statusSevere {
		t.Fatalf("status = %q, want %q", summary.Status, statusSevere)
	}
}

func TestUncheckedDiskSearchFlags(t *testing.T) {
	records := []model.Record{record(1, model.StatusSkip), record(2, model.StatusSkip)}
	summary := Summarize(nil, records, nil, overwriteEnabled(), false)
	if summary.Stats.DiskChecked || summary.Stats.RecordingChecked {
		t.Fatalf("disk/recording checked = %v/%v, want false/false",
			summary.Stats.DiskChecked, summary.Stats.RecordingChecked)
	}
	if summary.Stats.DiskSkip != 2 || summary.Stats.RecordingSkip != 2 {
		t.Fatalf("disk/recording skip = %d/%d, want 2/2",
			summary.Stats.DiskSkip, summary.Stats.RecordingSkip)
	}
	if summary.Status != statusGood {
		t.Fatalf("status = %q, want %q", summary.Status, statusGood)
	}

	// Deep AV forces a disk search, so the checked flag is set even when all
	// rows carry 跳过 placeholders.
	deep := Summarize(nil, records, nil, overwriteEnabled(), true)
	if !deep.Stats.DiskChecked || !deep.Stats.RecordingChecked {
		t.Fatalf("deep disk/recording checked = %v/%v, want true/true",
			deep.Stats.DiskChecked, deep.Stats.RecordingChecked)
	}
}

func TestEmptyInputsAreHealthy(t *testing.T) {
	summary := Summarize(nil, nil, nil, model.DiskOverwrite{}, false)
	if summary.Status != statusGood {
		t.Fatalf("status = %q, want %q", summary.Status, statusGood)
	}
	if len(summary.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", summary.Warnings)
	}
	if summary.Stats.OverwriteLabel != "未知" {
		t.Fatalf("OverwriteLabel = %q, want 未知", summary.Stats.OverwriteLabel)
	}
	if summary.Stats.ChannelTotal != 0 || summary.Stats.CameraTotal != 0 {
		t.Fatalf("totals = channel %d / camera %d, want 0/0",
			summary.Stats.ChannelTotal, summary.Stats.CameraTotal)
	}
}

// ── "unknown" policy: 落盘未知 / 音频抽检未知 ─────────────────────

func TestUnknownPolicy(t *testing.T) {
	tests := []struct {
		name             string
		records          []model.Record
		deep             bool
		wantStatus       string
		wantDiskUnknown  int
		wantAudioUnknown int
		wantWarning      string
		wantNoWarning    string
	}{
		{
			name: "partial disk unknown does not degrade health",
			records: []model.Record{
				record(1, model.StatusOK),
				record(2, model.StatusOK),
				record(3, model.StatusUnknown),
			},
			wantStatus:      statusGood,
			wantDiskUnknown: 1,
			wantNoWarning:   "整体检索失败",
		},
		{
			name: "all disk unknown escalates to warning",
			records: []model.Record{
				record(1, model.StatusUnknown),
				record(2, model.StatusUnknown),
			},
			wantStatus:      statusWarning,
			wantDiskUnknown: 2,
			wantWarning:     "整体检索失败",
		},
		{
			name: "disk unknown does not warn when not checked",
			records: []model.Record{
				record(1, model.StatusSkip),
			},
			wantStatus:    statusGood,
			wantNoWarning: "整体检索失败",
		},
		{
			name: "disk all ok stays healthy",
			records: []model.Record{
				record(1, model.StatusOK),
				record(2, model.StatusOK),
			},
			wantStatus: statusGood,
		},
		{
			name: "partial audio unknown does not degrade health",
			records: []model.Record{
				func() model.Record {
					r := record(1, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusOK
					return r
				}(),
				func() model.Record {
					r := record(2, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusUnknown
					return r
				}(),
			},
			deep:             true,
			wantStatus:       statusGood,
			wantAudioUnknown: 1,
			wantNoWarning:    "整体拉流失败",
		},
		{
			name: "all audio unknown escalates to warning",
			records: []model.Record{
				func() model.Record {
					r := record(1, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusUnknown
					return r
				}(),
				func() model.Record {
					r := record(2, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusUnknown
					return r
				}(),
			},
			deep:             true,
			wantStatus:       statusWarning,
			wantAudioUnknown: 2,
			wantWarning:      "整体拉流失败",
		},
		{
			name: "audio skipped channels do not count in denominator",
			records: []model.Record{
				func() model.Record {
					r := record(1, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusOK
					return r
				}(),
				func() model.Record {
					r := record(2, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusSkip
					return r
				}(),
				func() model.Record {
					r := record(3, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusUnknown
					return r
				}(),
			},
			deep:             true,
			wantStatus:       statusGood,
			wantAudioUnknown: 1,
			wantNoWarning:    "整体拉流失败",
		},
		{
			name: "audio unknown not judged in non-deep mode",
			records: []model.Record{
				record(1, model.StatusOK),
			},
			deep:             false,
			wantStatus:       statusGood,
			wantAudioUnknown: 0,
			wantNoWarning:    "整体拉流失败",
		},
		{
			name: "both columns share the same partial policy",
			records: []model.Record{
				func() model.Record {
					r := record(1, model.StatusOK)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusOK
					return r
				}(),
				func() model.Record {
					r := record(2, model.StatusUnknown)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusUnknown
					return r
				}(),
			},
			deep:             true,
			wantStatus:       statusGood,
			wantDiskUnknown:  1,
			wantAudioUnknown: 1,
			wantNoWarning:    "整体",
		},
		{
			name: "both columns share the same all-unknown policy",
			records: []model.Record{
				func() model.Record {
					r := record(1, model.StatusUnknown)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusUnknown
					return r
				}(),
				func() model.Record {
					r := record(2, model.StatusUnknown)
					r.VideoProbe = model.StatusOK
					r.AudioProbe = model.StatusUnknown
					return r
				}(),
			},
			deep:             true,
			wantStatus:       statusWarning,
			wantDiskUnknown:  2,
			wantAudioUnknown: 2,
			wantWarning:      "整体",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := Summarize(nil, tt.records, nil, overwriteEnabled(), tt.deep)
			if summary.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (warnings=%v)", summary.Status, tt.wantStatus, summary.Warnings)
			}
			if summary.Stats.DiskUnknown != tt.wantDiskUnknown {
				t.Fatalf("DiskUnknown = %d, want %d", summary.Stats.DiskUnknown, tt.wantDiskUnknown)
			}
			if summary.Stats.AudioProbeUnknown != tt.wantAudioUnknown {
				t.Fatalf("AudioProbeUnknown = %d, want %d", summary.Stats.AudioProbeUnknown, tt.wantAudioUnknown)
			}
			if tt.wantWarning != "" && !containsWarning(summary.Warnings, tt.wantWarning) {
				t.Fatalf("warnings %v do not contain %q", summary.Warnings, tt.wantWarning)
			}
			if tt.wantNoWarning != "" && containsWarning(summary.Warnings, tt.wantNoWarning) {
				t.Fatalf("warnings %v unexpectedly contain %q", summary.Warnings, tt.wantNoWarning)
			}
		})
	}
}

// ── remaining health warnings ──────────────────────────────────────

func TestRecordingPlanAndAudioWarnings(t *testing.T) {
	recPlanMissing := record(1, model.StatusOK)
	recPlanMissing.RecordingEnabled = boolPtr(false)
	recPlanMissing.RecordingOK = "未配置"
	recNoAudio := record(2, model.StatusOK)
	recNoAudio.HasAudio = boolPtr(false)

	summary := Summarize(nil, []model.Record{recPlanMissing, recNoAudio}, nil, overwriteEnabled(), false)
	if summary.Status != statusSevere {
		t.Fatalf("status = %q, want %q", summary.Status, statusSevere)
	}
	for _, want := range []string{
		"1个通道未配置录像计划",
		"1个通道未开启录像音频(SaveAudio=false)",
	} {
		if !containsWarning(summary.Warnings, want) {
			t.Fatalf("warnings %v do not contain %q", summary.Warnings, want)
		}
	}
}

func TestDiskBadWarningSample(t *testing.T) {
	records := make([]model.Record, 0, 6)
	for i := 1; i <= 6; i++ {
		r := record(i, model.StatusBad)
		records = append(records, r)
	}
	summary := Summarize(nil, records, nil, overwriteEnabled(), false)
	if !containsWarning(summary.Warnings, "6个通道近期无录像") {
		t.Fatalf("warnings %v missing disk-bad warning", summary.Warnings)
	}

	few := []model.Record{
		record(1, model.StatusBad),
		record(2, model.StatusBad),
	}
	summary = Summarize(nil, few, nil, overwriteEnabled(), false)
	if !containsWarning(summary.Warnings, "2个通道近期无录像: 1(通道1)、2(通道2)") {
		t.Fatalf("warnings %v missing expected sample detail", summary.Warnings)
	}
}

func TestDeepProbeWarnings(t *testing.T) {
	recVideo := record(1, model.StatusOK)
	recVideo.VideoProbe = model.StatusBad
	recVideo.AudioProbe = model.StatusOK

	recAudioBad := record(2, model.StatusOK)
	recAudioBad.VideoProbe = model.StatusOK
	recAudioBad.AudioProbe = model.StatusBad

	recAudioWarn := record(3, model.StatusOK)
	recAudioWarn.VideoProbe = model.StatusOK
	recAudioWarn.AudioProbe = model.StatusWarn

	summary := Summarize(nil, []model.Record{recVideo, recAudioBad, recAudioWarn}, nil, overwriteEnabled(), true)
	if summary.Status != statusSevere {
		t.Fatalf("status = %q, want %q", summary.Status, statusSevere)
	}
	for _, want := range []string{
		"1个通道短时视频抽检异常",
		"1个通道短时音频抽检异常(无音轨)",
		"1个通道音频疑似静音/电平过低",
	} {
		if !containsWarning(summary.Warnings, want) {
			t.Fatalf("warnings %v do not contain %q", summary.Warnings, want)
		}
	}
}
