// Package report renders scan results as terminal text. It mirrors the
// summary-first / exception-first layout of cli_report.py without requiring
// a rich-text dependency.
package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"nvrstatus/internal/model"
)

const ruleWidth = 78

// RenderAll renders the site summary and every device report.
func RenderAll(w io.Writer, reports []model.Report, verbose bool) {
	if len(reports) == 0 {
		fmt.Fprintln(w, "无巡检结果")
		return
	}
	RenderSite(w, reports)
	for i := range reports {
		RenderDevice(w, &reports[i], verbose)
	}
	fail := 0
	for _, r := range reports {
		if r.Error != "" {
			fail++
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, strings.Repeat("─", ruleWidth))
	if fail > 0 {
		fmt.Fprintf(w, "完成: %d/%d 台成功, 失败 %d 台\n", len(reports)-fail, len(reports), fail)
	} else {
		fmt.Fprintf(w, "完成: %d/%d 台设备查询成功\n", len(reports), len(reports))
	}
	if !verbose {
		fmt.Fprintln(w, "提示: 加 --verbose 可输出全部通道与设备详情")
	}
	fmt.Fprintln(w)
}

// RenderSite renders the multi-device summary panel.
func RenderSite(w io.Writer, reports []model.Report) {
	now := time.Now().Format("2006-01-02 15:04:05")
	okN := 0
	totalCh, onlineCh, totalDisks := 0, 0, 0
	totalCap, totalUsed := 0.0, 0.0
	worst := model.StatusOK
	var warnings []string
	for _, r := range reports {
		if r.Error != "" {
			worst = "严重"
			warnings = append(warnings, fmt.Sprintf("%s: 连接失败", nameOf(r)))
			continue
		}
		okN++
		if rank(r.Health.Status) > rank(worst) {
			worst = r.Health.Status
		}
		totalCh += r.Health.Stats.CameraTotal
		onlineCh += r.Health.Stats.Online
		totalDisks += len(r.Drives)
		c, u := sumDrives(r.Drives)
		totalCap += c
		totalUsed += u
		for _, warn := range r.Health.Warnings {
			warnings = append(warnings, nameOf(r)+": "+warn)
		}
	}
	fail := len(reports) - okN
	fmt.Fprintln(w)
	fmt.Fprintf(w, "╔══ NVR 站点巡检 ═══════════════════════════════════════\n")
	fmt.Fprintf(w, "  巡检时间   %s\n", now)
	fmt.Fprintf(w, "  整体状态   %s\n", badge(worst))
	fmt.Fprintf(w, "  设备       %d/%d 台成功", okN, len(reports))
	if fail > 0 {
		fmt.Fprintf(w, "  ·  失败 %d", fail)
	}
	fmt.Fprintln(w)
	if totalCh > 0 {
		fmt.Fprintf(w, "  通道       %d/%d 在线\n", onlineCh, totalCh)
	}
	if totalDisks > 0 {
		avg := 0.0
		if totalCap > 0 {
			avg = totalUsed / totalCap * 100
		}
		fmt.Fprintf(w, "  硬盘       %d 块 · %.1f/%.1f TB · 均用 %s\n", totalDisks, totalUsed, totalCap, usageBar(avg, 12))
	}
	if len(warnings) > 0 {
		preview := warnings
		if len(preview) > 4 {
			preview = preview[:4]
		}
		fmt.Fprintf(w, "  主要风险   %s\n", strings.Join(preview, "\n             • "))
		if len(warnings) > len(preview) {
			fmt.Fprintf(w, "             • …另有 %d 条\n", len(warnings)-len(preview))
		}
	}
	fmt.Fprintf(w, "╚══════════════════════════════════════════════════════\n")

	if len(reports) > 1 {
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "#\t名称\t型号\t状态\t通道\t硬盘\t预警")
		for i, r := range reports {
			if r.Error != "" {
				fmt.Fprintf(tw, "%d\t%s\t—\t失败\t—\t—\t%s\n", i+1, nameOf(r), truncate(r.Error, 40))
				continue
			}
			capTB, usedTB := sumDrives(r.Drives)
			avg := 0.0
			if capTB > 0 {
				avg = usedTB / capTB * 100
			}
			warn := "—"
			if len(r.Health.Warnings) > 0 {
				warn = r.Health.Warnings[0]
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d/%d\t%d盘 %s\t%s\n",
				i+1, nameOf(r), emptyDash(r.Info["型号"]), r.Health.Status,
				r.Health.Stats.Online, r.Health.Stats.CameraTotal, len(r.Drives),
				strings.TrimSpace(usageBar(avg, 8)), truncate(warn, 36))
		}
		tw.Flush()
	}
}

// RenderDevice renders one device.
func RenderDevice(w io.Writer, r *model.Report, verbose bool) {
	if r.Error != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "┌─ %s · 连接失败 ─────────────────────────────\n", nameOf(*r))
		fmt.Fprintf(w, "│ %s\n", r.Error)
		fmt.Fprintln(w, "└─────────────────────────────────────────────")
		return
	}
	info := r.Info
	deep := r.DeepAVCheck
	label := nameOf(*r)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "┌─ %s ───────────────────────────────────────\n", label)
	fmt.Fprintf(w, "│ 整体状态   %s\n", badge(r.Health.Status))
	fmt.Fprintf(w, "│ 型号/固件  %s · %s\n", emptyDash(info["型号"]), emptyDash(info["固件版本"]))
	fmt.Fprintf(w, "│ 地址       %s\n", emptyDash(r.IP))
	if v := r.SysStatus["运行时长"]; v != "" {
		fmt.Fprintf(w, "│ 运行时长   %s\n", v)
	}
	if v := r.SysStatus["内存使用率"]; v != "" {
		fmt.Fprintf(w, "│ 内存       %s / 可用 %s (%s)\n", emptyDash(r.SysStatus["内存使用"]), emptyDash(r.SysStatus["内存可用"]), v)
	}
	if v := r.SysStatus["当前时间"]; v != "" {
		fmt.Fprintf(w, "│ 设备时间   %s\n", v)
	}
	fmt.Fprintln(w, "└─────────────────────────────────────────────")

	st := r.Health.Stats
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "检查项\t结果\t明细")
	camDetail := fmt.Sprintf("在线 %d / 离线 %d", st.Online, st.Offline)
	if st.DetectBad > 0 {
		camDetail += fmt.Sprintf(" / 通道检测异常 %d", st.DetectBad)
	}
	if st.Unconfirmed > 0 {
		camDetail += fmt.Sprintf(" / 未确认 %d", st.Unconfirmed)
	}
	camDetail += fmt.Sprintf(" · 共 %d", st.CameraTotal)
	camVerdict := model.StatusUnknown
	if st.CameraTotal > 0 {
		if st.Offline == 0 {
			camVerdict = model.StatusOK
		} else {
			camVerdict = model.StatusBad
		}
	}
	fmt.Fprintf(tw, "摄像头\t%s\t%s\n", camVerdict, camDetail)
	planVerdict := model.StatusOK
	if st.PlanMissing > 0 {
		planVerdict = model.StatusBad
	}
	fmt.Fprintf(tw, "录像计划\t%s\t已配置 %d / 未配置 %d\n", planVerdict, st.PlanConfigured, st.PlanMissing)
	recVerdict := model.StatusOK
	if st.RecordingBad > 0 {
		recVerdict = model.StatusBad
	} else if st.RecordingUnknown > 0 {
		recVerdict = model.StatusWarn
	}
	fmt.Fprintf(tw, "录像/近期录像\t%s\t正常 %d / 异常 %d / 未知 %d\n", recVerdict, st.RecordingOK, st.RecordingBad, st.RecordingUnknown)
	audioVerdict := model.StatusOK
	if st.NoAudio > 0 {
		audioVerdict = model.StatusWarn
	}
	fmt.Fprintf(tw, "音频配置\t%s\t含音频 %d / 不含 %d\n", audioVerdict, st.HasAudio, st.NoAudio)
	if deep {
		av := model.StatusOK
		if st.VideoProbeBad > 0 || st.AudioProbeBad > 0 {
			av = model.StatusBad
		} else if st.AudioProbeWarn > 0 || st.AudioProbeUnknown > 0 {
			av = model.StatusWarn
		}
		scope := ""
		if len(r.AVChannels) > 0 {
			parts := make([]string, len(r.AVChannels))
			for i, c := range r.AVChannels {
				parts[i] = strconv.Itoa(c)
			}
			scope = " · 仅通道 " + strings.Join(parts, "、")
		}
		fmt.Fprintf(tw, "音视频抽检\t%s\t视频 %d/%d · 音频 正常%d/异常%d/警告%d/未确认%d · %ds RTSP%s\n",
			av, st.VideoProbeOK, st.VideoProbeOK+st.VideoProbeBad, st.AudioProbeOK, st.AudioProbeBad,
			st.AudioProbeWarn, st.AudioProbeUnknown, r.AVSeconds, scope)
	}
	if len(r.Drives) > 0 {
		cap, used := sumDrives(r.Drives)
		avg := 0.0
		if cap > 0 {
			avg = used / cap * 100
		}
		diskVerdict := model.StatusOK
		if avg > 95 {
			diskVerdict = model.StatusWarn
		}
		for _, d := range r.Drives {
			if d.Status != "ok" && d.Status != "sleep" && d.Status != "idle" {
				diskVerdict = model.StatusBad
			}
		}
		fmt.Fprintf(tw, "硬盘\t%s\t%d 块 · %.1f/%.1f TB · 均用 %s\n", diskVerdict, len(r.Drives), used, cap, usageBar(avg, 12))
	}
	alarmVerdict := model.StatusOK
	if len(r.Alarms) > 0 {
		alarmVerdict = model.StatusWarn
	}
	fmt.Fprintf(tw, "报警\t%s\t%s\n", alarmVerdict, alarmText(len(r.Alarms)))
	tw.Flush()

	fmt.Fprintln(w)
	if len(r.Health.Warnings) == 0 && len(r.Alarms) == 0 {
		fmt.Fprintln(w, "  预警: 无")
	} else {
		for _, warn := range r.Health.Warnings {
			fmt.Fprintf(w, "  • %s\n", warn)
		}
		for _, a := range r.Alarms {
			fmt.Fprintf(w, "  • [报警] %s: %s - %s\n", a.Type, a.Disk, a.Description)
		}
	}

	renderChannels(w, r, verbose)
	renderDrives(w, r)
	if verbose {
		renderDetails(w, r)
	}
}

func renderChannels(w io.Writer, r *model.Report, verbose bool) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "── 通道 · 共 %d 路 ───────────────────────────\n", len(r.Records))
	var stamps []string
	for _, rec := range r.Records {
		if rec.ProbeWindow != "" && !contains(stamps, rec.ProbeWindow) {
			stamps = append(stamps, rec.ProbeWindow)
		}
	}
	if len(stamps) > 0 {
		shown := stamps
		if len(shown) > 4 {
			shown = shown[:4]
		}
		fmt.Fprintf(w, "  抽检时段: %s\n", strings.Join(shown, "  |  "))
	}
	if len(r.Records) == 0 {
		fmt.Fprintln(w, "  无通道数据")
		return
	}
	var problems []model.Record
	for _, rec := range r.Records {
		if isProblem(rec, r.DeepAVCheck) {
			problems = append(problems, rec)
		}
	}
	if !verbose && len(problems) == 0 {
		fmt.Fprintf(w, "  ✓ 全部 %d 路正常（录像/在线/音频", len(r.Records))
		if r.DeepAVCheck {
			fmt.Fprint(w, "/抽检")
		}
		fmt.Fprintln(w, "）。使用 --verbose 查看全表。")
		return
	}
	show := r.Records
	if !verbose {
		show = problems
		fmt.Fprintf(w, "  仅显示异常/警告 %d 路（--verbose 查看全部）\n", len(problems))
	}
	ipMap := map[string]string{}
	for _, c := range r.Cameras {
		ipMap[c.ID] = c.IP
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	head := "#\t名称\t在线\t模式\t音频\t录像"
	if r.DeepAVCheck {
		head += "\t视频\t音频抽检"
	}
	if verbose {
		head += "\tIP"
	}
	fmt.Fprintln(tw, head)
	var details []string
	for _, rec := range show {
		on := onlineText(rec.Online)
		au := boolText(rec.HasAudio)
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", rec.Channel, emptyDash(rec.Name), on, emptyDash(rec.RecordingMode), au, statusOrStr(rec.RecordingOK, model.StatusUnknown))
		if r.DeepAVCheck {
			row += fmt.Sprintf("\t%s\t%s", statusOrStr(rec.VideoProbe, model.StatusSkip), statusOrStr(rec.AudioProbe, model.StatusSkip))
		}
		if verbose {
			row += "\t" + emptyDash(ipMap[rec.Channel])
		}
		fmt.Fprintln(tw, row)
		chLabel := "通道 " + rec.Channel
		if rec.Name != "" {
			chLabel += " " + rec.Name
		}
		if rec.DiskDetail != "" && rec.DiskStatus != "" && rec.DiskStatus != model.StatusOK && rec.DiskStatus != model.StatusSkip {
			details = append(details, fmt.Sprintf("%s: 近期录像 %s - %s", chLabel, rec.DiskStatus, rec.DiskDetail))
		}
		if r.DeepAVCheck && rec.ProbeDetail != "" {
			bad := false
			if rec.VideoProbe != "" && rec.VideoProbe != model.StatusOK && rec.VideoProbe != model.StatusSkip {
				bad = true
			}
			if rec.AudioProbe != "" && rec.AudioProbe != model.StatusOK && rec.AudioProbe != model.StatusSkip {
				bad = true
			}
			if bad {
				details = append(details, fmt.Sprintf("%s: 抽检 %s", chLabel, rec.ProbeDetail))
			}
		}
	}
	tw.Flush()
	for _, d := range details {
		fmt.Fprintf(w, "  ↳ %s\n", d)
	}
}

func renderDrives(w io.Writer, r *model.Report) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "── 硬盘 · %d 块 ─────────────────────────────\n", len(r.Drives))
	ow := r.DiskOverwrite
	if ow.Label != "" {
		fmt.Fprintf(w, "  循环覆盖: %s", ow.Label)
		if ow.Detail != "" {
			fmt.Fprintf(w, "  ·  %s", ow.Detail)
		}
		fmt.Fprintln(w)
	}
	if len(r.Drives) == 0 {
		fmt.Fprintln(w, "  未检测到硬盘")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 1, ' ', 0)
	fmt.Fprintln(tw, "盘符\t接口\t属性\t容量\t已用\t剩余\t使用率\t状态")
	for _, d := range r.Drives {
		usage := d.Usage
		if usage == "" {
			usage = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%sTB\t%sTB\t%sTB\t%s\t%s\n",
			d.Name, d.Type, d.Property, emptyDash(d.CapacityTB), emptyDash(d.UsedTB),
			emptyDash(d.FreeTB), usage, driveStatus(d.Status))
	}
	tw.Flush()
	cap, used := sumDrives(r.Drives)
	avg := 0.0
	if cap > 0 {
		avg = used / cap * 100
	}
	fmt.Fprintf(w, "  合计  %.1f/%.1f TB  ·  均用 %s\n", used, cap, usageBar(avg, 12))
}

func renderDetails(w io.Writer, r *model.Report) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "── 设备详情 ─────────────────────────────────")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	keys := sortedKeys(r.Info)
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%s\n", k, r.Info[k])
	}
	if r.AVSaveDir != "" {
		fmt.Fprintf(tw, "抽检片段\t%s\n", r.AVSaveDir)
	}
	if r.LookbackMinutes > 0 {
		fmt.Fprintf(tw, "近期录像窗口\t近 %d 分钟\n", r.LookbackMinutes)
	}
	tw.Flush()
}

// helpers -----------------------------------------------------------------

func nameOf(r model.Report) string {
	if r.DeviceName != "" {
		return r.DeviceName
	}
	if r.Info["设备名称"] != "" {
		return r.Info["设备名称"]
	}
	if r.IP != "" {
		return r.IP
	}
	return "NVR"
}

func rank(status string) int {
	switch status {
	case "良好":
		return 0
	case "警告":
		return 1
	case "严重", "故障", "异常":
		return 2
	default:
		return 1
	}
}

func badge(status string) string {
	switch status {
	case "良好":
		return "✓ 良好"
	case "警告":
		return "! 警告"
	case "严重", "故障", "异常":
		return "× " + status
	default:
		return "? " + emptyDash(status)
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func usageBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(float64(width)*pct/100 + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + fmt.Sprintf(" %5.1f%%", pct)
}

func sumDrives(drives []model.Drive) (cap, used float64) {
	for _, d := range drives {
		cap += parseFloat(d.CapacityTB)
		used += parseFloat(d.UsedTB)
	}
	return
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func isProblem(r model.Record, deep bool) bool {
	ok := r.RecordingOK
	if ok == "" {
		ok = model.StatusUnknown
	}
	if ok != model.StatusOK {
		return true
	}
	if r.Online == "false" {
		return true
	}
	if r.HasAudio != nil && !*r.HasAudio {
		return true
	}
	if deep {
		if r.VideoProbe != "" && r.VideoProbe != model.StatusOK && r.VideoProbe != model.StatusSkip {
			return true
		}
		if r.AudioProbe != "" && r.AudioProbe != model.StatusOK && r.AudioProbe != model.StatusSkip {
			return true
		}
	}
	return false
}

func onlineText(v string) string {
	switch v {
	case "true":
		return "在线"
	case "false":
		return "离线"
	default:
		return "未知"
	}
}

func boolText(v *bool) string {
	if v == nil {
		return "未知"
	}
	if *v {
		return "是"
	}
	return "否"
}

func statusOrStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func alarmText(n int) string {
	if n == 0 {
		return "无报警"
	}
	return fmt.Sprintf("%d 条", n)
}

func driveStatus(s string) string {
	switch s {
	case "ok":
		return "正常"
	case "sleep", "idle":
		return "休眠"
	default:
		return "异常"
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
