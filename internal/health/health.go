// Package health aggregates the device health verdict.
//
// It is a pure port of nvr_core/health.py: the caller passes the already
// collected cameras / records / drives / loop-overwrite verdict, and
// Summarize returns the same warnings and counters as the Python version.
package health

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"nvrstatus/internal/model"
)

// Health verdict strings. Severity order: 良好 < 警告 < 严重.
const (
	statusGood    = "良好"
	statusWarning = "警告"
	statusSevere  = "严重"

	detectOnline = "在线"
)

// chanDetectResult whitelists. The firmware values are not standardized, so
// only values we positively recognize participate in the verdict. Anything
// else is "未知" and never treated as online/offline (better a miss than a
// false alarm).
var detectOnlineValues = map[string]struct{}{
	"connect":   {},
	"connected": {},
	"normal":    {},
	"ok":        {},
	"online":    {},
	"已配置":       {},
}

var detectAbnormalValues = map[string]struct{}{
	"notexist":          {},
	"disconnect":        {},
	"disconnected":      {},
	"neterror":          {},
	"networkerror":      {},
	"ipconflict":        {},
	"ipaddressconflict": {},
	"userpwerror":       {},
	"passworderror":     {},
	"userlocked":        {},
	"locked":            {},
	"unsupported":       {},
	"notsupport":        {},
}

// detectReasonLabels maps an abnormal detect value to a human-readable reason.
var detectReasonLabels = map[string]string{
	"notexist":          "通道未接入",
	"disconnect":        "连接断开",
	"disconnected":      "连接断开",
	"neterror":          "网络不可达",
	"networkerror":      "网络不可达",
	"ipconflict":        "IP 冲突",
	"ipaddressconflict": "IP 冲突",
	"userpwerror":       "用户名或密码错误",
	"passworderror":     "用户名或密码错误",
	"userlocked":        "用户被锁定",
	"locked":            "用户被锁定",
	"unsupported":       "不支持",
	"notsupport":        "不支持",
}

// fullDriveUsagePct is the usage percentage at which a drive is reported as
// full / nearly full.
const fullDriveUsagePct = 95.0

// normalizeDetectKey lower-cases and strips whitespace/separators so that
// values such as "notExist", "NOT_EXIST" and "not exist" compare equal.
func normalizeDetectKey(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '_', '-', '/', '\\':
			continue
		}
		if unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ClassifyDetectState normalizes a chanDetectResult value to "在线", "异常" or
// "未知". Values outside the whitelist are "未知" and do not participate in
// the online/offline verdict.
func ClassifyDetectState(raw string) string {
	key := normalizeDetectKey(raw)
	if key == "" {
		return model.StatusUnknown
	}
	if _, ok := detectAbnormalValues[key]; ok {
		return model.StatusBad
	}
	if _, ok := detectOnlineValues[key]; ok {
		return detectOnline
	}
	return model.StatusUnknown
}

// DetectReason returns the Chinese reason for an abnormal detect value. For
// values without a mapping it falls back to the trimmed raw value, matching
// health.py's detect_reason(); an empty raw value yields "".
func DetectReason(raw string) string {
	if label, ok := detectReasonLabels[normalizeDetectKey(raw)]; ok {
		return label
	}
	return strings.TrimSpace(raw)
}

// severityRank orders the three health levels.
func severityRank(level string) int {
	switch level {
	case statusSevere:
		return 2
	case statusWarning:
		return 1
	default:
		return 0
	}
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, "%", ""))
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// driveStatusBad reports a drive status that is neither missing/unknown nor a
// known healthy value. Status matching is case-insensitive.
func driveStatusBad(status string) bool {
	st := strings.ToLower(strings.TrimSpace(status))
	if st == "" {
		return false
	}
	switch st {
	case "ok", "normal", "sleep", "idle", "未知", "unknown":
		return false
	}
	return true
}

// Summarize builds the health summary from already-collected data.
//
// deepAV controls the deep audio/video probe rules. Disk-recording checks are
// inferred from the records: a record whose DiskStatus is "跳过" means the
// disk search did not run, while any 正常/异常/未知 status means it did.
func Summarize(cameras []model.Camera, records []model.Record, drives []model.Drive, overwrite model.DiskOverwrite, deepAV bool) model.HealthSummary {
	summary := model.HealthSummary{
		Status:   statusGood,
		Warnings: make([]string, 0),
	}
	worst := statusGood

	raise := func(level string) {
		if severityRank(level) > severityRank(worst) {
			worst = level
		}
	}
	warn := func(format string, args ...any) {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(format, args...))
	}

	owEnabled := overwrite.Enabled
	owLabel := overwrite.Label
	if owLabel == "" {
		owLabel = "未知"
	}

	// ── 硬盘状态 + 循环覆盖 ──────────────────────────────────
	badDrives := 0
	fullDrives := 0
	sleepingDrives := 0
	for _, d := range drives {
		if driveStatusBad(d.Status) {
			badDrives++
		}
		if parsePercent(d.Usage) >= fullDriveUsagePct {
			fullDrives++
		}
		switch strings.ToLower(strings.TrimSpace(d.Status)) {
		case "sleep", "idle":
			sleepingDrives++
		}
	}
	if badDrives > 0 {
		raise(statusSevere)
		warn("%d块硬盘状态异常", badDrives)
	}
	if fullDrives > 0 {
		switch {
		case owEnabled != nil && !*owEnabled:
			// 未开循环覆盖：满盘将停录，升级严重
			raise(statusSevere)
			warn("%d块硬盘空间已满/即将用尽，且循环覆盖%s，满盘后可能停止录像", fullDrives, owLabel)
		case owEnabled != nil && *owEnabled:
			raise(statusWarning)
			warn("%d块硬盘空间已满/即将用尽（循环覆盖%s，将覆盖旧录像继续录）", fullDrives, owLabel)
		default:
			raise(statusWarning)
			warn("%d块硬盘空间已满/即将用尽（循环覆盖状态%s，请人工确认）", fullDrives, owLabel)
		}
	} else if owEnabled != nil && !*owEnabled {
		// 非满盘：未开启则预警；已开启/未知仅记入统计与结果区展示
		raise(statusWarning)
		warn("循环覆盖%s：硬盘写满后将停止录像，建议在 NVR 存储设置中开启", owLabel)
	}
	if sleepingDrives > 0 {
		warn("%d块硬盘处于休眠状态", sleepingDrives)
	}

	// ── 摄像头在线检查 ──────────────────────────────────────
	// online=false 权威离线；online=true 时若检测状态明确异常只另记警告；
	// online 缺失时用检测状态补判；两者都不明确 → 未确认（不误报离线）。
	offline := make([]model.Camera, 0, len(cameras))
	detectBad := 0
	unconfirmed := 0
	onlineN := 0
	for _, c := range cameras {
		rawOnline := strings.ToLower(strings.TrimSpace(c.Online))
		det := ClassifyDetectState(c.DetectState)
		switch {
		case rawOnline == "false":
			offline = append(offline, c)
		case rawOnline == "true":
			onlineN++
			if det == model.StatusBad {
				detectBad++
			}
		case det == detectOnline:
			onlineN++
		case det == model.StatusBad:
			offline = append(offline, c)
		default:
			unconfirmed++
		}
	}

	if len(offline) > 0 {
		raise(statusSevere)
		names := make([]string, 0, len(offline))
		for _, c := range offline {
			if c.Name != "未知" {
				names = append(names, c.Name)
			}
		}
		// 原因细分：优先用通道检测状态解释离线原因（未接入 / 网络不可达 …）
		type reasonGroup struct {
			label string
			count int
		}
		index := make(map[string]int, len(offline))
		groups := make([]reasonGroup, 0, len(offline))
		for _, c := range offline {
			if ClassifyDetectState(c.DetectState) != model.StatusBad {
				continue
			}
			label := DetectReason(c.DetectState)
			if i, ok := index[label]; ok {
				groups[i].count++
			} else {
				index[label] = len(groups)
				groups = append(groups, reasonGroup{label: label, count: 1})
			}
		}
		sort.SliceStable(groups, func(i, j int) bool { return groups[i].count > groups[j].count })

		msg := fmt.Sprintf("%d个摄像头离线", len(offline))
		if len(groups) > 0 {
			parts := make([]string, len(groups))
			for i, g := range groups {
				parts[i] = fmt.Sprintf("%s %d", g.label, g.count)
			}
			msg += "：" + strings.Join(parts, "、")
		} else if len(names) > 0 {
			msg += "(" + strings.Join(names, "、") + ")"
		}
		warn("%s", msg)
	}

	if detectBad > 0 {
		raise(statusWarning)
		warn("%d个在线通道检测状态异常(IP 冲突 / 网络不可达等，建议核对通道)", detectBad)
	}

	// ── 录像：计划 / 音频 / 落盘 ────────────────────────────
	diskChecked := deepAV
	if !diskChecked {
		for _, r := range records {
			st := strings.TrimSpace(r.DiskStatus)
			if st != "" && st != model.StatusSkip {
				diskChecked = true
				break
			}
		}
	}

	stats := model.HealthStats{
		ChannelTotal:     len(records),
		Online:           onlineN,
		Offline:          len(offline),
		Unconfirmed:      unconfirmed,
		DetectBad:        detectBad,
		CameraTotal:      len(cameras),
		DeepAV:           deepAV,
		OverwriteLabel:   owLabel,
		OverwriteEnabled: owEnabled,
		DiskChecked:      diskChecked,
		RecordingChecked: diskChecked,
	}

	if len(records) > 0 {
		for _, r := range records {
			if r.RecordingEnabled != nil && *r.RecordingEnabled {
				stats.PlanConfigured++
			} else {
				stats.PlanMissing++
			}

			switch strings.TrimSpace(r.RecordingOK) {
			case model.StatusSkip:
				stats.RecordingSkip++
			case model.StatusOK:
				stats.RecordingOK++
			case model.StatusBad, "未配置":
				stats.RecordingBad++
			default:
				stats.RecordingUnknown++
			}

			switch {
			case r.HasAudio == nil:
				stats.AudioUnknown++
			case *r.HasAudio:
				stats.HasAudio++
			default:
				stats.NoAudio++
			}

			switch strings.TrimSpace(r.DiskStatus) {
			case model.StatusSkip:
				stats.DiskSkip++
			case model.StatusOK:
				stats.DiskOK++
			case model.StatusBad:
				stats.DiskBad++
			default:
				stats.DiskUnknown++
			}

			switch strings.TrimSpace(r.VideoProbe) {
			case model.StatusOK:
				stats.VideoProbeOK++
			case model.StatusBad:
				stats.VideoProbeBad++
			}
			switch strings.TrimSpace(r.AudioProbe) {
			case model.StatusOK:
				stats.AudioProbeOK++
			case model.StatusBad:
				stats.AudioProbeBad++
			case model.StatusWarn:
				stats.AudioProbeWarn++
			case model.StatusUnknown:
				stats.AudioProbeUnknown++
			}
		}

		// 计划/音频来自配置查询，快速模式仍可预警
		if stats.PlanMissing > 0 {
			raise(statusSevere)
			warn("%d个通道未配置录像计划", stats.PlanMissing)
		}
		if stats.NoAudio > 0 {
			raise(statusWarning)
			warn("%d个通道未开启录像音频(SaveAudio=false)", stats.NoAudio)
		}

		// 落盘 / 录像综合结论：仅在实际查询后写入预警
		if diskChecked {
			if stats.DiskBad > 0 {
				raise(statusSevere)
				bad := make([]model.Record, 0, stats.DiskBad)
				for _, r := range records {
					if strings.TrimSpace(r.DiskStatus) == model.StatusBad {
						bad = append(bad, r)
					}
				}
				limit := len(bad)
				if limit > 5 {
					limit = 5
				}
				samples := make([]string, 0, limit)
				for _, r := range bad[:limit] {
					label := r.Channel
					if r.Name != "" && r.Name != "未知" {
						label += "(" + r.Name + ")"
					}
					samples = append(samples, label)
				}
				sample := strings.Join(samples, "、")
				if len(bad) <= 5 && sample != "" {
					warn("%d个通道近期无录像: %s", stats.DiskBad, sample)
				} else {
					more := ""
					if len(bad) > 5 {
						more = fmt.Sprintf(" 等%d路", len(bad))
					}
					warn("%d个通道近期无录像%s", stats.DiskBad, more)
				}
			}

			// 「落盘未知」口径（与「音频抽检未知」一致）：部分未知不降级，
			// 仅当实际检索到的通道全部未知（整体检索失败）才升级为警告。
			diskAttempted := stats.DiskOK + stats.DiskBad + stats.DiskUnknown
			if stats.DiskUnknown > 0 && diskAttempted > 0 && stats.DiskUnknown >= diskAttempted {
				raise(statusWarning)
				warn("%d个通道近期录像状态未知(整体检索失败)", stats.DiskUnknown)
			}
		}

		if deepAV {
			if stats.VideoProbeBad > 0 {
				raise(statusSevere)
				warn("%d个通道短时视频抽检异常", stats.VideoProbeBad)
			}
			if stats.AudioProbeBad > 0 {
				raise(statusSevere)
				warn("%d个通道短时音频抽检异常(无音轨)", stats.AudioProbeBad)
			}
			if stats.AudioProbeWarn > 0 {
				raise(statusWarning)
				warn("%d个通道音频疑似静音/电平过低", stats.AudioProbeWarn)
			}
			audioAttempted := stats.AudioProbeOK + stats.AudioProbeBad + stats.AudioProbeWarn + stats.AudioProbeUnknown
			if stats.AudioProbeUnknown > 0 && audioAttempted > 0 && stats.AudioProbeUnknown >= audioAttempted {
				raise(statusWarning)
				warn("%d个通道音频抽检未确认(整体拉流失败)", stats.AudioProbeUnknown)
			}
		}
	}

	summary.Stats = stats
	summary.Status = worst
	return summary
}
