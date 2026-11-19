package nvgui

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *App) sidebar(c *ui.Context, t *ui.Theme) {
	bar := c.TitleBar()
	ui.Column(c).Fill().AlignItems(ui.Stretch).Background(t.Surface).Children(func() {
		// With the native title bar hidden the sidebar runs to the window's
		// top edge. Keep the brand header inline with the traffic lights as
		// the window's drag handle.
		ui.Row(c).MinHeight(maxFloat(bar.Height+20, 46)).
			Padding(0, bar.Right+14, 0, bar.Left+10).AlignItems(ui.Center).DragWindow().
			Children(func() {
				a.header(c, t)
			})
		ui.Column(c).Grow(1).Padding(0, 14, 14, 14).Gap(10).Children(func() {
			a.profileBar(c, t)
			ui.Divider(c)
			a.deviceSection(c, t)
			ui.Divider(c)
			a.settingsBar(c, t)
			a.actions(c, t)
			a.logSection(c, t)
			a.footer(c, t)
		})
	})
}

func maxFloat(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func (a *App) header(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Grow(1).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Column(c).Gap(1).Grow(1).Children(func() {
			ui.Text(c, "NVR Status").FontSize(19).Bold()
			ui.Text(c, "海康 NVR 状态巡检").FontSize(11).TextColor(t.TextMuted)
		})
		if a.ffmpegAvailable() {
			ui.Badge(c, "ffmpeg").Tooltip("深度抽检可用")
		} else {
			ui.Badge(c, "lite").Tooltip("未找到 ffmpeg/ffprobe，深度抽检将降级")
		}
	})
}

func (a *App) profileBar(c *ui.Context, t *ui.Theme) {
	names := a.profiles
	if len(names) == 0 {
		names = []string{a.profile}
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		sel := ui.Select(c.Key("profile"), &a.profileChoice, names).Grow(1).Label("配置档案")
		if sel.Changed() && a.profileChoice != "" && a.profileChoice != a.profile {
			a.saveProfile()
			a.applyProfile(a.profileChoice)
		}
		ui.Button(c.Key("profile-new"), "新建").Shrink(0).OnClick(a.beginNewProfile)
		ui.Button(c.Key("profile-manage"), "管理").Shrink(0).Menu(func(m *ui.Menu) {
			if m.Item("另存为…").Chosen() {
				a.beginSaveAsProfile()
			}
			if m.Item("重命名…").Chosen() {
				a.beginRenameProfile()
			}
			m.Separator()
			if m.Item("导入档案…").Chosen() {
				a.importProfile()
			}
			if m.Item("导出当前档案…").Chosen() {
				a.exportProfile()
			}
			m.Separator()
			if m.Item("删除当前档案").Chosen() {
				a.askDeleteProfile()
			}
		})
	})
	_ = t
}

func (a *App) deviceSection(c *ui.Context, t *ui.Theme) {
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Textf(c, "设备 · %d", len(a.devices)).FontSize(12).Bold().TextColor(t.TextMuted)
		ui.Spacer(c)
		ui.Button(c.Key("device-add"), "添加").FontSize(11).OnClick(a.beginAddDevice)
		ui.Button(c.Key("device-manage"), "管理设备").FontSize(11).Menu(func(m *ui.Menu) {
			edit := m.Item("编辑当前设备…")
			if a.selectedDev < 0 || a.selectedDev >= len(a.devices) {
				edit.Disabled(true)
			} else if edit.Chosen() {
				a.beginEditDevice(a.selectedDev)
			}
			del := m.Item("删除当前设备")
			if a.selectedDev < 0 || a.selectedDev >= len(a.devices) {
				del.Disabled(true)
			} else if del.Chosen() {
				a.askDeleteDevice(a.selectedDev)
			}
		})
	})
	if len(a.devices) == 0 {
		ui.Text(c, "还没有设备，先添加一台 NVR").FontSize(11).TextColor(t.TextMuted)
		return
	}

	// Only the current scan target is visible. The device list lives in this
	// button's menu, so a long device list no longer pushes the rest of the
	// sidebar down.
	row := ui.Row(c).Gap(8).AlignItems(ui.Center)
	row.Children(func() {
		btn := ui.Button(c.Key("device-current"), a.currentTargetLabel()).Grow(1).
			Tooltip("点击切换巡检设备")
		btn.Menu(func(m *ui.Menu) {
			allLabel := fmt.Sprintf("全部设备（%d 台）", len(a.devices))
			if a.target == allDevices {
				allLabel = "✓ " + allLabel
			}
			if m.Item(allLabel).Chosen() {
				a.target = allDevices
				a.selectedDev = -1
			}
			m.Separator()
			for i := range a.devices {
				d := a.devices[i]
				name := deviceName(d, i)
				label := fmt.Sprintf("%s  ·  %s", name, d.IP)
				if a.target == name {
					label = "✓ " + label
				}
				if m.Item(label).Chosen() {
					a.selectedDev = i
					a.target = name
				}
			}
		})
		if status := a.currentTargetStatus(); status != "" {
			ui.Text(c, status).FontSize(10).TextColor(statusColor(t, status))
		}
	})
}

func (a *App) currentTargetLabel() string {
	if a.target == "" || a.target == allDevices {
		return fmt.Sprintf("%s（%d 台）  ▾", allDevices, len(a.devices))
	}
	for i, d := range a.devices {
		if deviceName(d, i) == a.target {
			return fmt.Sprintf("%s · %s  ▾", a.target, d.IP)
		}
	}
	return a.target + "  ▾"
}

func (a *App) currentTargetStatus() string {
	if a.target == "" || a.target == allDevices {
		return ""
	}
	for i, d := range a.devices {
		if deviceName(d, i) == a.target || d.Name == a.target {
			return a.lastHealth[d.IP]
		}
	}
	return ""
}

func (a *App) targetNames() []string {
	out := []string{allDevices}
	for i, d := range a.devices {
		out = append(out, deviceName(d, i))
	}
	return out
}

func (a *App) settingsBar(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Button(c.Key("settings-toggle"), a.settingsButtonLabel()).FontSize(11).Grow(1).
			Tooltip("打开扫描设置").OnClick(func() { a.settingsOpen = true })
		ui.Toggle(c.Key("quick-deep"), &a.opt.DeepAVCheck, "深度抽检").FontSize(11).Shrink(0).
			Tooltip("快速切换短时 RTSP 音视频抽检").
			OnChange(func() {
				if !a.opt.DeepAVCheck {
					a.opt.AVSave = false
				}
				a.saveProfile()
			})
	})
	if a.opt.DeepAVCheck && !a.ffmpegAvailable() {
		ui.Text(c, "ffmpeg 缺失，深度抽检将自动降级").FontSize(9).TextColor(t.Warning)
	}
}

func (a *App) settingsButtonLabel() string {
	return "设置 · " + a.settingsSummary()
}

func (a *App) settingsSummary() string {
	o := a.options()
	return fmt.Sprintf("%d分 · %d并发", o.Lookback, o.Workers)
}

func (a *App) actions(c *ui.Context, t *ui.Theme) {
	if a.scanning || a.singleCheck {
		ui.Progress(c, clampFloat(a.progress, 0, 1)).Label(a.progressLabel)
	}
	ui.Row(c).Gap(8).Children(func() {
		if a.scanning || a.singleCheck {
			ui.PrimaryButton(c.Key("cancel"), "取消巡检").Grow(1).OnClick(a.cancelRunning)
		} else {
			ui.PrimaryButton(c.Key("scan"), "开始巡检").Grow(1).
				Disabled(len(a.targetDevices()) == 0).
				OnClick(a.startScan)
		}
	})
	_ = t
}

func (a *App) logSection(c *ui.Context, t *ui.Theme) {
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "日志").FontSize(12).Bold().TextColor(t.TextMuted)
		ui.Spacer(c)
		if ui.Button(c.Key("log-copy"), "复制").FontSize(10).Clicked() {
			c.Services().WriteClipboard(a.logText())
			c.Toast("日志已复制")
		}
		if ui.Button(c.Key("log-clear"), "清空").FontSize(10).Clicked() {
			a.logs = nil
		}
	})
	if !a.logExpanded {
		ui.Spacer(c).Grow(1)
		return
	}
	// Follow the end of the log while the view is at the end, so lines stream
	// in during a scan. Scrolling up pins the view: Y stops tracking MaxY and
	// stays where the user left it until they scroll back down.
	if a.logScroll.Y >= a.logScroll.MaxY {
		a.logScroll.Y = math.MaxFloat32
	}
	ui.Scroll(c).Grow(1).TrackScroll(&a.logScroll).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			if len(a.logs) == 0 {
				ui.Text(c, "巡检日志会显示在这里").FontSize(10).TextColor(t.TextMuted)
				return
			}
			start := 0
			if len(a.logs) > 250 {
				start = len(a.logs) - 250
			}
			for _, line := range a.logs[start:] {
				ui.Text(c, line.Text).FontSize(10).TextColor(logColor(t, line.Level)).SingleLine()
			}
		})
	})
}

func (a *App) footer(c *ui.Context, t *ui.Theme) {
	ui.Divider(c)
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Select(c.Key("theme"), &a.themeMode, []string{"跟随系统", "浅色", "深色"}).Width(110).OnChange(a.saveSettings)
		ui.Spacer(c)
		ui.Button(c.Key("history"), "历史报告").FontSize(11).OnClick(a.openHistory)
	})
	_ = t
}

func logColor(t *ui.Theme, level string) ui.Color {
	switch level {
	case "error":
		return t.Danger
	case "warn":
		return t.Warning
	case "ok":
		return t.Success
	case "step":
		return t.Accent
	default:
		return t.TextMuted
	}
}

func statusColor(t *ui.Theme, status string) ui.Color {
	switch status {
	case "正常", "良好", "在线", "是":
		return t.Success
	case "警告", "未知", "跳过":
		return t.Warning
	case "异常", "严重", "离线", "否", "未配置":
		return t.Danger
	default:
		return t.TextMuted
	}
}

func (a *App) logText() string {
	var b strings.Builder
	for _, l := range a.logs {
		fmt.Fprintln(&b, l.Text)
	}
	return b.String()
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
