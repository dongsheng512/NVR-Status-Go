package nvgui

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"nvrstatus/internal/model"
	"nvrstatus/internal/util"
)

func (a *App) content(c *ui.Context, t *ui.Theme) {
	bar := c.TitleBar()
	// The results start near the top edge: the traffic lights live over the
	// sidebar, so the page does not need the full title-bar inset. Keep the
	// inset only where the platform puts window buttons on the right.
	top := float32(10)
	if bar.Right > 0 {
		top = bar.Height + 6
	}
	ui.Column(c).Fill().Padding(top, 18, 18, 18).Gap(8).Background(t.Background).Children(func() {
		a.resultHeader(c, t)
		if a.report == nil {
			a.emptyState(c, t)
			return
		}
		if a.report.Error != "" {
			a.errorState(c, t)
			return
		}
		a.warnSection(c, t)
		a.diffSection(c, t)
		a.metrics(c, t)
		a.channelTable(c, t)
		a.drivesSection(c, t)
	})
	a.detailModal(c, t)
}

func (a *App) resultHeader(c *ui.Context, t *ui.Theme) {
	ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
		title := "NVR 巡检"
		if a.report != nil {
			title = a.report.DeviceName
		}
		ui.Column(c).Gap(1).Grow(1).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(19).Bold()
				if a.report != nil {
					ui.Text(c, "● "+a.report.Health.Status).FontSize(11).TextColor(statusColor(t, a.report.Health.Status))
				}
			})
			sub := "选择设备后开始巡检"
			if a.report != nil {
				if a.report.Error != "" {
					sub = fmt.Sprintf("%s · 连接失败", a.report.IP)
				} else {
					parts := []string{a.report.IP}
					if m := strings.TrimSpace(a.report.Info["型号"]); m != "" {
						parts = append(parts, m)
					}
					if fw := strings.TrimSpace(a.report.Info["固件版本"]); fw != "" {
						parts = append(parts, "固件 "+fw)
					}
					sub = strings.Join(parts, " · ")
				}
				if len(a.reports) > 1 {
					ok := 0
					for _, r := range a.reports {
						if r.Error == "" {
							ok++
						}
					}
					sub = fmt.Sprintf("%d/%d 台成功 · %s", ok, len(a.reports), sub)
				}
			}
			ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted)
		})
		if len(a.reports) > 1 {
			names := make([]string, 0, len(a.reports))
			for _, r := range a.reports {
				names = append(names, r.DeviceName)
			}
			ui.Select(c.Key("result-device"), &a.resultDevice, names).Width(180).OnChange(func() {
				a.selectReport(a.resultDevice)
			})
		}
		if a.report != nil {
			ui.Button(c.Key("export"), "导出").Menu(func(m *ui.Menu) {
				if m.Item("CSV…").Chosen() {
					a.exportCurrentCSV()
				}
				if m.Item("TXT…").Chosen() {
					a.exportCurrentTXT()
				}
			})
			if a.report.AVSaveDir != "" {
				ui.Button(c.Key("open-samples"), "样片目录").OnClick(func() {
					c.Services().OpenURL(fileURL(a.report.AVSaveDir))
				})
			}
		}
	})
}

func (a *App) selectReport(name string) {
	for i := range a.reports {
		if a.reports[i].DeviceName == name {
			a.report = &a.reports[i]
			a.resultDevice = name
			a.diff = a.diffs[a.report.IP]
			a.selectedRow = -1
			*a.table.Selected = -1
			return
		}
	}
}

func (a *App) errorState(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).Center().Children(func() {
		ui.Text(c, "巡检失败").FontSize(18).Bold().TextColor(t.Danger)
		ui.Text(c, a.report.Error).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, "请检查设备 IP、账号密码与网络连通性").FontSize(11).TextColor(t.TextMuted)
		ui.PrimaryButton(c.Key("retry"), "重试").OnClick(a.startScan)
	})
}

func (a *App) emptyState(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).Center().Children(func() {
		ui.Text(c, "还没有巡检结果").FontSize(17).Bold()
		ui.Text(c, "在左侧切换巡检设备并点击「开始巡检」").FontSize(12).TextColor(t.TextMuted)
		if a.ffmpegAvailable() {
			ui.Text(c, "深度抽检可用（已找到 ffmpeg/ffprobe）").FontSize(11).TextColor(t.Success)
		} else {
			ui.Text(c, "未找到 ffmpeg/ffprobe：深度抽检将自动降级").FontSize(11).TextColor(t.Warning)
		}
	})
}

func (a *App) warnSection(c *ui.Context, t *ui.Theme) {
	warns := a.report.Health.Warnings
	if len(warns) == 0 {
		return
	}
	box := ui.Column(c).Padding(10).Gap(4).Radius(8).Background(t.Warning.Alpha(0.12))
	box.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("预警 %d 条", len(warns))).FontSize(12).Bold().TextColor(t.Warning)
			ui.Spacer(c)
			if len(warns) > 2 {
				toggleLabel := fmt.Sprintf("展开全部 (%d)", len(warns))
				if a.warnExpanded {
					toggleLabel = "收起"
				}
				if ui.Button(c.Key("warn-toggle"), toggleLabel).FontSize(10).Clicked() {
					a.warnExpanded = !a.warnExpanded
				}
			}
		})
		shown := warns
		if !a.warnExpanded && len(shown) > 2 {
			shown = shown[:2]
		}
		spans := make([]ui.Span, 0, len(shown)*2)
		for i, w := range shown {
			if i > 0 {
				spans = append(spans, ui.Span{Text: "\n"})
			}
			spans = append(spans, ui.Span{Text: "• " + w, Color: t.Warning})
		}
		ui.RichText(c, spans...).FontSize(11)
		if !a.warnExpanded && len(warns) > 2 {
			ui.Text(c, fmt.Sprintf("…另有 %d 条", len(warns)-2)).FontSize(10).TextColor(t.TextMuted)
		}
	})
}

func (a *App) diffSection(c *ui.Context, t *ui.Theme) {
	if a.diff == nil {
		return
	}
	d := *a.diff
	ui.Column(c).Padding(8, 10).Gap(2).Radius(6).Background(t.Surface).Children(func() {
		ui.Row(c).Gap(6).Children(func() {
			ui.Text(c, "与上次相比").FontSize(10).Bold().TextColor(t.TextMuted)
			ui.Text(c, d.Summary()).FontSize(10).TextColor(statusColor(t, d.HealthAfter))
		})
		if len(d.NewIssues) > 0 {
			ui.Text(c, "新增: "+strings.Join(d.NewIssues, "；")).FontSize(9).TextColor(t.Danger).SingleLine()
		}
		if len(d.Resolved) > 0 {
			ui.Text(c, "恢复: "+strings.Join(d.Resolved, "；")).FontSize(9).TextColor(t.Success).SingleLine()
		}
	})
}

func (a *App) metrics(c *ui.Context, t *ui.Theme) {
	st := a.report.Health.Stats
	ui.Row(c).Gap(8).Children(func() {
		a.metricCard(c, t, "摄像头在线", fmt.Sprintf("%d/%d", st.Online, st.CameraTotal),
			joinNonZero("离线", st.Offline, "未确认", st.Unconfirmed, "检测异常", st.DetectBad), toneForCount(st.Offline))
		a.metricCard(c, t, "录像", fmt.Sprintf("%d/%d", st.RecordingOK, st.ChannelTotal),
			joinNonZero("异常", st.RecordingBad, "未知", st.RecordingUnknown, "跳过", st.RecordingSkip), toneForCount(st.RecordingBad))
		a.metricCard(c, t, "落盘", fmt.Sprintf("%d/%d", st.DiskOK, diskCheckedCount(st)),
			joinNonZero("异常", st.DiskBad, "未知", st.DiskUnknown, "跳过", st.DiskSkip), toneForCount(st.DiskBad))
		driveTip := a.report.DiskOverwrite.Label
		driveTone := overwriteTone(a.report.DiskOverwrite.Enabled)
		if st.DiskUsageUnknown > 0 {
			driveTip = fmt.Sprintf("%s · 使用率未确认 %d 块", driveTip, st.DiskUsageUnknown)
			if st.DiskUsageUnknown >= len(a.report.Drives) {
				driveTone = model.StatusWarn
			} else {
				driveTone = model.StatusUnknown
			}
		}
		a.metricCard(c, t, "硬盘", fmt.Sprintf("%d 块", len(a.report.Drives)),
			driveTip, driveTone)
	})
}

// diskCheckedCount keeps the card denominator readable without a bool in stats.
func diskCheckedCount(s model.HealthStats) int {
	n := s.DiskOK + s.DiskBad + s.DiskUnknown
	if n == 0 {
		n = s.ChannelTotal
	}
	return n
}

func (a *App) metricCard(c *ui.Context, t *ui.Theme, title, value, tip, tone string) {
	ui.Column(c).Grow(1).Padding(10).Gap(2).Radius(8).Background(t.Surface).
		Tooltip(tip).Children(func() {
		ui.Text(c, title).FontSize(10).TextColor(t.TextMuted)
		ui.Text(c, value).FontSize(16).Bold().TextColor(statusColor(t, tone))
		if tip != "" {
			ui.Text(c, tip).FontSize(9).TextColor(t.TextMuted).SingleLine()
		}
	})
}

func joinNonZero(pairs ...any) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		label, _ := pairs[i].(string)
		n, _ := pairs[i+1].(int)
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", label, n))
		}
	}
	if len(parts) == 0 {
		return "全部正常"
	}
	return strings.Join(parts, " · ")
}

func toneForCount(n int) string {
	if n > 0 {
		return "异常"
	}
	return "正常"
}

func overwriteTone(v *bool) string {
	if v == nil {
		return "未知"
	}
	if *v {
		return "正常"
	}
	return "警告"
}

func (a *App) channelTable(c *ui.Context, t *ui.Theme) {
	records := a.visibleRecords()
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		title := fmt.Sprintf("通道 · %d 路", len(a.report.Records))
		if len(records) != len(a.report.Records) {
			title = fmt.Sprintf("通道 · %d/%d 路", len(records), len(a.report.Records))
		}
		ui.Text(c, title).FontSize(12).Bold()
		ui.Spacer(c)
		ui.TextInput(c.Key("channel-filter"), &a.channelFilter).Placeholder("筛选通道/名称…").FontSize(11).Width(140).Height(28)
		ui.Toggle(c.Key("only-problem"), &a.onlyProblem, "仅异常").FontSize(11).Height(28)
		ui.Button(c.Key("single"), "单路抽检").FontSize(11).Height(28).
			Disabled(a.selectedRow < 0 || a.scanning || a.singleCheck).
			Tooltip("对选中的通道执行音视频抽检").
			OnClick(a.runSingleCheck)
		if ui.Button(c.Key("copy-row"), "复制").FontSize(11).Height(28).
			Disabled(a.selectedRow < 0).Clicked() {
			if a.selectedRow >= 0 && a.selectedRow < len(a.report.Records) {
				r := a.report.Records[a.selectedRow]
				c.Services().WriteClipboard(copyLine(r))
				c.Toast("已复制通道信息")
			}
		}
	})
	if len(records) == 0 {
		a.selectedRow = -1
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "没有符合筛选的通道").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}
	cols := []ui.TableColumn{
		{Title: "通道", ID: "channel", Width: 50, Align: ui.End, Sortable: true},
		{Title: "名称", ID: "name", Width: 132},
		{Title: "在线", ID: "online", Width: 52, Align: ui.Center},
		{Title: "模式", ID: "mode", Width: 76},
		{Title: "音频", ID: "audio", Width: 52, Align: ui.Center},
		{Title: "录像", ID: "recording", Width: 52, Align: ui.Center},
		{Title: "落盘", ID: "disk", Width: 52, Align: ui.Center},
	}
	if a.report.DeepAVCheck {
		cols = append(cols,
			ui.TableColumn{Title: "视频", ID: "video", Width: 48, Align: ui.Center},
			ui.TableColumn{Title: "音频抽检", ID: "audio_probe", Width: 72, Align: ui.Center},
		)
	}
	cols = append(cols, ui.TableColumn{Title: "详情", ID: "detail", Width: 220})
	detailCol := len(cols) - 1
	tbl := ui.Table(c, &a.table, cols, len(records), func(row, col int) {
		r := records[row]
		switch col {
		case 0:
			ui.Text(c, r.Channel).SingleLine()
		case 1:
			ui.Text(c, r.Name).SingleLine()
		case 2:
			ui.Text(c, onlineLabel(r.Online)).TextColor(statusColor(t, onlineLabel(r.Online)))
		case 3:
			ui.Text(c, r.RecordingMode).SingleLine()
		case 4:
			ui.Text(c, audioLabel(r.HasAudio)).TextColor(statusColor(t, audioLabel(r.HasAudio)))
		case 5:
			ui.Text(c, statusOr(r.RecordingOK, model.StatusUnknown)).TextColor(statusColor(t, r.RecordingOK))
		case 6:
			ui.Text(c, statusOr(r.DiskStatus, model.StatusUnknown)).TextColor(statusColor(t, r.DiskStatus))
		case detailCol:
			summary := recordDetailSummary(r, a.report.DeepAVCheck)
			color := t.TextMuted
			if rowIsProblem(r, a.report.DeepAVCheck) && summary != "" {
				color = t.Warning
			}
			ui.Text(c, summary).FontSize(10).TextColor(color).SingleLine()
		case 7:
			ui.Text(c, statusOr(r.VideoProbe, model.StatusSkip)).TextColor(statusColor(t, r.VideoProbe))
		case 8:
			ui.Text(c, statusOr(r.AudioProbe, model.StatusSkip)).TextColor(statusColor(t, r.AudioProbe))
		}
	}).Grow(1)
	a.syncTableSelection(records)
	if tbl.Submitted() {
		a.openDetail(a.selectedRow)
	}
	tbl.ContextMenu(func(m *ui.Menu) {
		if m.Item("查看详情").Chosen() {
			a.openDetail(a.selectedRow)
		}
		if m.Item("复制通道信息").Chosen() {
			if a.selectedRow >= 0 && a.selectedRow < len(a.report.Records) {
				c.Services().WriteClipboard(copyLine(a.report.Records[a.selectedRow]))
			}
		}
		m.Separator()
		if m.Item("单路抽检").Chosen() {
			a.runSingleCheck()
		}
	})
}

func (a *App) visibleRecords() []model.Record {
	query := strings.ToLower(strings.TrimSpace(a.channelFilter))
	var out []model.Record
	for _, r := range a.report.Records {
		if a.onlyProblem && !rowIsProblem(r, a.report.DeepAVCheck) {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(r.Channel), query) &&
			!strings.Contains(strings.ToLower(r.Name), query) {
			continue
		}
		out = append(out, r)
	}
	if sortOrder := a.table.Sort; sortOrder != nil {
		sort.SliceStable(out, func(i, j int) bool {
			var less bool
			switch sortOrder.Column {
			case "channel":
				less = channelNum(out[i].Channel) < channelNum(out[j].Channel)
			case "name":
				less = out[i].Name < out[j].Name
			default:
				less = out[i].Channel < out[j].Channel
			}
			if sortOrder.Descending {
				return !less
			}
			return less
		})
	}
	return out
}

func (a *App) syncTableSelection(records []model.Record) {
	if a.table.Selected == nil {
		a.selectedRow = -1
		return
	}
	idx := *a.table.Selected
	if idx < 0 || idx >= len(records) {
		a.selectedRow = -1
		return
	}
	want := records[idx]
	for i := range a.report.Records {
		if a.report.Records[i].TrackID == want.TrackID && a.report.Records[i].Channel == want.Channel {
			a.selectedRow = i
			return
		}
	}
	a.selectedRow = -1
}

func recordDetailSummary(r model.Record, deep bool) string {
	if deep && r.ProbeDetail != "" && (r.VideoProbe != model.StatusOK || r.AudioProbe != model.StatusOK) {
		return r.ProbeDetail
	}
	if r.DiskStatus != "" && r.DiskStatus != model.StatusOK && r.DiskStatus != model.StatusSkip && r.DiskDetail != "" {
		return r.DiskDetail
	}
	if r.Online == "false" {
		return "摄像头离线"
	}
	if r.HasAudio != nil && !*r.HasAudio {
		return "未开启音频录制"
	}
	if r.Resolution != "" {
		return r.Resolution
	}
	return r.DiskDetail
}

func channelNum(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 1 << 30
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func rowIsProblem(r model.Record, deep bool) bool {
	if r.RecordingOK != "" && r.RecordingOK != model.StatusOK && r.RecordingOK != model.StatusSkip {
		return true
	}
	if r.DiskStatus != "" && r.DiskStatus != model.StatusOK && r.DiskStatus != model.StatusSkip {
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

func (a *App) drivesSection(c *ui.Context, t *ui.Theme) {
	if len(a.report.Drives) == 0 {
		return
	}
	ui.Column(c).Padding(8, 12).Gap(6).Radius(8).Background(t.Surface).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("硬盘 · %d 块", len(a.report.Drives))).FontSize(12).Bold()
			ui.Spacer(c)
			ui.Text(c, "循环覆盖: "+a.report.DiskOverwrite.Label).FontSize(10).
				TextColor(statusColor(t, overwriteTone(a.report.DiskOverwrite.Enabled)))
		})
		for _, d := range a.report.Drives {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, d.Name).FontSize(11).Bold().Width(72).SingleLine()
				ui.Text(c, d.Type).FontSize(10).TextColor(t.TextMuted).Width(52).SingleLine()
				ui.Text(c, d.CapacityTB+" TB").FontSize(10).TextColor(t.TextMuted).Width(64).SingleLine()
				if pct, ok := util.ParsePercent(d.Usage); ok {
					ui.Progress(c, clampFloat(pct/100.0, 0, 1)).Width(120)
				}
				ui.Text(c, d.Usage).FontSize(10).TextColor(statusColor(t, usageTone(d.Usage))).Width(52)
				ui.Spacer(c)
				ui.Text(c, driveLabel(d.Status)).FontSize(10).TextColor(statusColor(t, driveLabel(d.Status)))
			})
		}
	})
}

func usageTone(usage string) string {
	var pct float64
	_, err := fmt.Sscanf(strings.TrimSuffix(usage, "%"), "%f", &pct)
	if err != nil {
		return "未知"
	}
	if pct >= 95 {
		return "警告"
	}
	return "正常"
}

func driveLabel(s string) string {
	switch s {
	case "ok":
		return "正常"
	case "sleep", "idle":
		return "休眠"
	default:
		return "异常"
	}
}

func (a *App) openDetail(i int) {
	if i < 0 || i >= len(a.report.Records) {
		return
	}
	a.detailRecord = a.report.Records[i]
	a.detailOpen = true
}

func (a *App) detailModal(c *ui.Context, t *ui.Theme) {
	if !a.detailOpen {
		return
	}
	open := true
	ui.Modal(c, &open, func() {
		r := a.detailRecord
		ui.Column(c).Width(560).Gap(12).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("通道 %s · %s", r.Channel, r.Name)).FontSize(15).Bold()
				ui.Spacer(c)
				ui.Text(c, statusOr(r.RecordingOK, model.StatusUnknown)).FontSize(12).
					TextColor(statusColor(t, r.RecordingOK))
			})
			ui.Form(c, func() {
				detailRow(c, t, "在线", onlineLabel(r.Online), statusColor(t, onlineLabel(r.Online)))
				detailRow(c, t, "录像模式", emptyDash(r.RecordingMode), t.Text)
				detailRow(c, t, "录像时段", emptyDash(r.RecordingSchedule), t.Text)
				detailRow(c, t, "含音频配置", audioLabel(r.HasAudio), statusColor(t, audioLabel(r.HasAudio)))
				detailRow(c, t, "落盘状态", statusOr(r.DiskStatus, "—")+" · "+emptyDash(r.DiskDetail), statusColor(t, r.DiskStatus))
				detailRow(c, t, "视频抽检", statusOr(r.VideoProbe, "—"), statusColor(t, r.VideoProbe))
				detailRow(c, t, "音频抽检", statusOr(r.AudioProbe, "—"), statusColor(t, r.AudioProbe))
				if r.ProbeDetail != "" {
					detailRow(c, t, "抽检详情", r.ProbeDetail, t.TextMuted)
				}
				if r.ProbeWindow != "" {
					detailRow(c, t, "抽检时段", r.ProbeWindow, t.TextMuted)
				}
				if r.Resolution != "" {
					detailRow(c, t, "分辨率", r.Resolution, t.Text)
				}
				if r.VideoCodec != "" {
					detailRow(c, t, "视频编码", r.VideoCodec, t.Text)
				}
				if r.AudioCodec != "" {
					detailRow(c, t, "音频编码", r.AudioCodec, t.Text)
				}
				if r.MeanVolumeDB != nil {
					detailRow(c, t, "音量", fmt.Sprintf("%.1f dB", *r.MeanVolumeDB), t.Text)
				}
				if r.SavePath != "" {
					detailRow(c, t, "保存路径", r.SavePath, t.TextMuted)
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				ui.Button(c.Key("detail-close"), "关闭").MinWidth(80).OnClick(func() { a.detailOpen = false })
				ui.PrimaryButton(c.Key("detail-single"), "单路抽检").MinWidth(90).OnClick(func() {
					a.detailOpen = false
					a.runSingleCheck()
				})
			})
		})
	})
	if !open {
		a.detailOpen = false
	}
}

func detailRow(c *ui.Context, t *ui.Theme, label, value string, color ui.Color) {
	ui.Row(c).Gap(10).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).Width(92)
		ui.Text(c, value).FontSize(11).TextColor(color).Grow(1)
	})
}

func copyLine(r model.Record) string {
	return fmt.Sprintf("通道 %s %s 在线=%s 录像=%s 落盘=%s 视频=%s 音频=%s",
		r.Channel, r.Name, onlineLabel(r.Online), statusOr(r.RecordingOK, "未知"),
		statusOr(r.DiskStatus, "未知"), statusOr(r.VideoProbe, "跳过"), statusOr(r.AudioProbe, "跳过"))
}

func onlineLabel(v string) string {
	switch v {
	case "true":
		return "在线"
	case "false":
		return "离线"
	default:
		return "未知"
	}
}

func audioLabel(v *bool) string {
	if v == nil {
		return "未知"
	}
	if *v {
		return "是"
	}
	return "否"
}

func statusOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func emptyDash(v string) string {
	if v == "" {
		return "—"
	}
	return v
}
