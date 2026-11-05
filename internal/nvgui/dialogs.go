package nvgui

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nvrstatus/internal/export"
	"nvrstatus/internal/history"
	"nvrstatus/internal/model"
)

// dialogs renders every modal/alert the app can show. It runs at the end of
// the frame so a dialog sits above the page.
func (a *App) dialogs(c *ui.Context, t *ui.Theme) {
	a.profileDialogModal(c, t)
	a.deviceFormModal(c, t)
	a.settingsDialog(c, t)
	a.confirmModal(c, t)
	a.historyModal(c, t)
}

// ---- scan settings --------------------------------------------------------

// formatSettingNumber renders a setting value with the decimals of its step.
func formatSettingNumber(v float64, step float64) string {
	decimals := 0
	if s := strconv.FormatFloat(step, 'f', -1, 64); strings.Contains(s, ".") {
		decimals = len(s) - strings.IndexByte(s, '.') - 1
	}
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

// numberField is a text input with a vertical stepper inside its right edge,
// replacing ui.NumberInput's external − / + buttons.
func numberField(c *ui.Context, key string, n *numberFieldState, lo, hi, step float64) ui.Element {
	t := c.Theme()
	if !n.Handle.Focused(c) {
		want := formatSettingNumber(n.Value, step)
		if n.Text != want {
			n.Text = want
		}
	}
	row := ui.Row(c).Height(34).Border(1, t.Border).Radius(t.Radius).
		Background(t.Surface).Shrink(0).AlignItems(ui.Stretch)
	row.Children(func() {
		in := ui.TextInput(c.Key(key), &n.Text).Border(0, ui.Transparent).Background(ui.Transparent).
			Grow(1).Bind(&n.Handle)
		if in.Changed() {
			if v, err := strconv.ParseFloat(strings.TrimSpace(n.Text), 64); err == nil {
				if v < lo {
					v = lo
				}
				if v > hi {
					v = hi
				}
				n.Value = v
			}
		}
		ui.Stepper(c.Key(key+"-step"), &n.Value, lo, hi, step).
			Width(24).Shrink(0).Border(0, ui.Transparent).Background(ui.Transparent)
	})
	return row
}

func (a *App) settingsDialog(c *ui.Context, t *ui.Theme) {
	if !a.settingsOpen {
		return
	}
	open := true
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(600).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "扫描设置").FontSize(15).Bold()
				ui.Spacer(c)
				if a.ffmpegAvailable() {
					ui.Badge(c, "ffmpeg 可用")
				} else {
					ui.Badge(c, "ffmpeg 缺失").Tooltip("未找到 ffmpeg/ffprobe，深度抽检将自动降级")
				}
			})
			// MaxHeight rather than a fixed Height: the dialog is as tall as its
			// content and only scrolls when the window is too short for it. The
			// bound has to clear the tallest state (deep check + saving samples),
			// or the last row is clipped with no scrollbar to hint there is more.
			ui.Scroll(c).MaxHeight(510).Children(func() {
				ui.Column(c).Gap(12).Children(func() {
					a.settingsBasics(c, t)
					a.settingsDeepAV(c, t)
				})
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Button(c.Key("settings-reset"), "恢复默认").FontSize(11).OnClick(func() {
					a.resetScanOptions()
					c.Toast("已恢复默认扫描设置")
				})
				ui.Text(c, "设置会随当前档案保存").FontSize(10).TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.PrimaryButton(c.Key("settings-done"), "完成").MinWidth(90).OnClick(func() {
					a.saveProfile()
					a.settingsOpen = false
				})
			})
		})
	})
	if !open {
		a.settingsOpen = false
		a.saveProfile()
	}
}

// resetScanOptions puts the whole form back to the shipped defaults. The form
// keeps numbers in numberFieldState and flags in a.opt, so both halves have to
// be reset — syncFormFromOptions is what copies the latter into the former.
func (a *App) resetScanOptions() {
	a.opt = model.DefaultScanOptions()
	a.syncFormFromOptions()
	a.saveProfile()
}

// settingsBasics is the first group: full-width single-column rows, since three
// fields do not need splitting. Checkboxes use an empty label so they line up
// with the controls above them.
func (a *App) settingsBasics(c *ui.Context, t *ui.Theme) {
	settingsGroup(c, t, "基础", func() {
		settingField(c, t, "回溯窗口", func() {
			numberField(c, "lookback", &a.lookbackValue, 1, 43200, 1).Width(96)
			ui.Select(c.Key("lookback-unit"), &a.lookbackUnit, []string{"分钟", "小时", "天"}).Width(84)
		})
		settingField(c, t, "落盘检查并发", func() {
			numberField(c, "workers", &a.workers, 1, 16, 1).Width(96).Tooltip("1–16")
		})
		settingField(c, t, "设备并发", func() {
			numberField(c, "device-workers", &a.deviceWorkers, 1, 4, 1).Width(96).Tooltip("1–4，同时巡检的 NVR 台数")
		})
		settingField(c, t, "", func() {
			ui.Checkbox(c.Key("no-search"), &a.opt.NoSearch, "跳过落盘检索（最快，仅查计划与音频配置）").FontSize(11)
		})
	})
}

// settingsDeepAV is the second group: two named, visibly separated columns.
// Left covers what and how much is probed, right covers when and where samples
// go. Everything below the switch stays in place but disabled while deep
// checking is off, so flipping it does not change the dialog's height. The
// switch itself must stay outside that block, or it could never be turned on.
func (a *App) settingsDeepAV(c *ui.Context, t *ui.Theme) {
	deep := &a.opt.DeepAVCheck
	settingsGroup(c, t, "深度音视频抽检", func() {
		// The switch spans both columns, so it starts at the group's left edge
		// rather than at either column's control line.
		ui.Checkbox(c.Key("deep"), deep, "启用短时 RTSP 音视频抽检").FontSize(11).
			Tooltip("需要 ffmpeg；缺失时自动降级").
			OnChange(func() {
				if !*deep {
					a.opt.AVSave = false
				}
			})
		ui.Column(c).Gap(4).Disabled(!*deep).Children(func() {
			settingsColumns(c, t,
				func() {
					colHeading(c, t, "抽检范围与质量")
					columnField(c, t, "每路抽检时长", func() {
						numberField(c, "av-seconds", &a.avSeconds, 3, 12, 1).Width(96).Tooltip("3–12 秒")
						unitLabel(c, t, "秒")
					})
					columnField(c, t, "抽检并发", func() {
						numberField(c, "av-workers", &a.avWorkers, 1, 3, 1).Width(96).Tooltip("1–3")
					})
					columnField(c, t, "最多抽检通道", func() {
						numberField(c, "av-limit", &a.avLimit, 0, 256, 1).Width(96).Tooltip("0 表示全部")
						unitLabel(c, t, "0 = 全部")
					})
					columnField(c, t, "静音阈值", func() {
						numberField(c, "silence", &a.silenceDB, -100, 0, 1).Width(96)
						unitLabel(c, t, "dB")
					})
				},
				func() {
					colHeading(c, t, "时段与保存")
					columnField(c, t, "优先时段", func() {
						numberField(c, "busy-start", &a.busyStart, 0, 23, 1).Width(60)
						unitLabel(c, t, "–")
						numberField(c, "busy-end", &a.busyEnd, 1, 24, 1).Width(60)
						unitLabel(c, t, "时")
					})
					columnField(c, t, "抽检日", func() {
						numberField(c, "busy-days", &a.busyDaysAgo, 0, 30, 1).Width(96)
						unitLabel(c, t, "0 = 今天")
					})
					columnField(c, t, "", func() {
						ui.Checkbox(c.Key("av-save"), &a.opt.AVSave, "保存抽检片段到本机").FontSize(11)
					})
					// Always present: showing it only when checked added a row
					// and made the dialog jump. Disabled instead of hidden.
					ui.Column(c).Disabled(!a.opt.AVSave).Children(func() {
						columnField(c, t, "保存目录", func() {
							ui.TextInput(c.Key("save-root"), &a.saveRoot).
								Placeholder("留空使用默认目录").Width(150)
						})
					})
				})
		})
	})
}

// settingsGroup boxes related fields under a title, the way macOS groups a
// settings form. It keeps the dialog's own background: filling it would make it
// read as one giant input, since controls are drawn on Surface too.
func settingsGroup(c *ui.Context, t *ui.Theme, title string, body func()) {
	ui.Column(c).Gap(8).Padding(12).Radius(t.Radius).Border(1, t.Border).Children(func() {
		ui.Text(c, title).FontSize(12).Bold().TextColor(t.TextMuted)
		body()
	})
}

// settingsColumns splits a group into two named columns with a hairline and a
// generous gap between them. Fixed widths keep each column's control line
// predictable; two Grow(1) halves made the right one float in the middle.
func settingsColumns(c *ui.Context, t *ui.Theme, left, right func()) {
	ui.Row(c).Gap(24).Children(func() {
		ui.Column(c).Width(240).Gap(6).Children(left)
		ui.Divider(c)
		ui.Column(c).Width(240).Gap(6).Children(right)
	})
}

func colHeading(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).FontSize(11).Bold().TextColor(t.Accent)
}

// columnField is a form row inside one of the two columns: a narrower label
// column so a 96pt control still fits in 240pt.
func columnField(c *ui.Context, t *ui.Theme, label string, control func()) {
	fieldRow(c, t, 76, label, control)
}

func settingField(c *ui.Context, t *ui.Theme, label string, control func()) {
	fieldRow(c, t, 104, label, control)
}

// fieldRow puts a label in a fixed column with its controls beside it, so
// controls line up down the dialog. Stacking a label above every field cost two
// lines each and left most of the dialog width empty.
func fieldRow(c *ui.Context, t *ui.Theme, labelW float32, label string, control func()) {
	ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).Width(labelW).SingleLine()
		ui.Row(c).Grow(1).Gap(6).AlignItems(ui.Center).Children(control)
	})
}

// unitLabel is the suffix that gives a number its unit, kept out of the label so
// labels stay short enough for one column.
func unitLabel(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine()
}

// ---- profile dialogs ------------------------------------------------------

func (a *App) beginNewProfile() {
	a.profileDialog = profileDialogNew
	a.profileDraft = "新档案"
	a.profileErr = ""
}

func (a *App) beginSaveAsProfile() {
	a.profileDialog = profileDialogSaveAs
	a.profileDraft = a.profile + " 副本"
	a.profileErr = ""
}

func (a *App) beginRenameProfile() {
	a.profileDialog = profileDialogRename
	a.profileDraft = a.profile
	a.profileErr = ""
}

func (a *App) profileDialogModal(c *ui.Context, t *ui.Theme) {
	open := a.profileDialog != profileDialogNone
	ui.Modal(c, &open, func() {
		title := "新建档案"
		verb := "创建"
		switch a.profileDialog {
		case profileDialogSaveAs:
			title, verb = "另存为档案", "保存"
		case profileDialogRename:
			title, verb = "重命名档案", "重命名"
		}
		ui.Column(c).Width(380).Gap(14).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			ui.Field(c, "档案名", func() {
				ui.TextInput(c.Key("profile-name"), &a.profileDraft).AutoFocus().OnSubmit(a.commitProfileDialog)
			}).Error(a.profileErr)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				ui.Button(c.Key("profile-cancel"), "取消").MinWidth(80).OnClick(func() {
					a.profileDialog = profileDialogNone
				})
				ui.PrimaryButton(c.Key("profile-ok"), verb).MinWidth(80).OnClick(a.commitProfileDialog)
			})
		})
	})
	if !open {
		a.profileDialog = profileDialogNone
	}
	_ = t
}

func (a *App) commitProfileDialog() {
	name := strings.TrimSpace(a.profileDraft)
	if name == "" {
		a.profileErr = "档案名不能为空"
		return
	}
	if a.store == nil {
		return
	}
	switch a.profileDialog {
	case profileDialogNew:
		if _, err := a.store.CreateProfile(name, ""); err != nil {
			a.profileErr = err.Error()
			return
		}
		a.reloadProfiles()
		a.applyProfile(name)
	case profileDialogSaveAs:
		a.saveProfile()
		if _, err := a.store.CreateProfile(name, a.profile); err != nil {
			a.profileErr = err.Error()
			return
		}
		a.reloadProfiles()
		a.applyProfile(name)
	case profileDialogRename:
		if !a.store.RenameProfile(a.profile, name) {
			a.profileErr = "重命名失败：档案不存在或名称重复"
			return
		}
		a.profile = name
		a.profileChoice = name
		a.reloadProfiles()
	}
	a.profileDialog = profileDialogNone
}

func (a *App) askDeleteProfile() {
	if len(a.profiles) <= 1 {
		a.logf("至少保留一个档案", "warn")
		return
	}
	a.confirm = confirmDeleteProfile
	a.pendingProfile = a.profile
	a.confirmText = fmt.Sprintf("确定删除档案「%s」吗？此操作不可撤销。", a.profile)
}

func (a *App) importProfile() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:   "导入配置档案",
			Filters: []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		name, err := a.store.ImportProfile(paths[0], "")
		if err != nil {
			a.logFromWorker("导入档案失败: "+err.Error(), "error")
			return
		}
		a.post(func() {
			a.reloadProfiles()
			a.applyProfile(name)
			a.logf("已导入档案："+name, "ok")
		})
	}()
}

func (a *App) exportProfile() {
	a.saveProfile()
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出配置档案",
			DefaultPath: a.profile + ".json",
			Filters:     []mygo.FileFilter{{Name: "JSON", Extensions: []string{"json"}}},
		})
		if err != nil || path == "" {
			return
		}
		if err := a.store.ExportProfile(a.profile, path); err != nil {
			a.logFromWorker("导出档案失败: "+err.Error(), "error")
			return
		}
		a.post(func() { a.logf("已导出档案："+path, "ok") })
	}()
}

// ---- device form ----------------------------------------------------------

func (a *App) beginAddDevice() {
	a.deviceForm = true
	a.editingDevice = -1
	a.deviceErr = ""
	a.draftName = fmt.Sprintf("NVR%d", len(a.devices)+1)
	a.draftIP = ""
	a.draftPort = "80"
	a.draftUser = "admin"
	a.draftPass = ""
	a.draftSSL = false
}

func (a *App) beginEditDevice(i int) {
	if i < 0 || i >= len(a.devices) {
		return
	}
	d := a.devices[i]
	a.deviceForm = true
	a.editingDevice = i
	a.deviceErr = ""
	a.draftName = d.Name
	a.draftIP = d.IP
	a.draftPort = strconv.Itoa(d.Port)
	if d.Port == 0 {
		a.draftPort = "80"
	}
	a.draftUser = d.Username
	if a.draftUser == "" {
		a.draftUser = "admin"
	}
	a.draftPass = d.Password
	a.draftSSL = d.SSL
}

func (a *App) deviceFormModal(c *ui.Context, t *ui.Theme) {
	if !a.deviceForm {
		return
	}
	open := true
	ui.Modal(c, &open, func() {
		title := "添加设备"
		verb := "添加"
		if a.editingDevice >= 0 {
			title, verb = "编辑设备", "保存"
		}
		ui.Column(c).Width(430).Gap(14).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			ui.Form(c, func() {
				ui.Field(c, "名称", func() {
					ui.TextInput(c.Key("dev-name"), &a.draftName).Placeholder("例如 NVR-A")
				})
				ui.Field(c, "IP 地址", func() {
					ui.TextInput(c.Key("dev-ip"), &a.draftIP).Placeholder("192.168.1.64").AutoFocus()
				})
				ui.Field(c, "端口", func() {
					ui.TextInput(c.Key("dev-port"), &a.draftPort).Placeholder("80")
				})
				ui.Field(c, "用户名", func() {
					ui.TextInput(c.Key("dev-user"), &a.draftUser).Placeholder("admin")
				})
				ui.Field(c, "密码", func() {
					ui.TextInput(c.Key("dev-pass"), &a.draftPass).Password()
				})
				ui.Toggle(c.Key("dev-ssl"), &a.draftSSL, "使用 HTTPS")
			})
			if a.deviceErr != "" {
				ui.Text(c, a.deviceErr).FontSize(11).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				ui.Button(c.Key("dev-cancel"), "取消").MinWidth(80).OnClick(func() { a.deviceForm = false })
				ui.PrimaryButton(c.Key("dev-ok"), verb).MinWidth(80).OnClick(a.commitDeviceForm)
			})
		})
	})
	if !open {
		a.deviceForm = false
	}
}

func (a *App) commitDeviceForm() {
	ip := strings.TrimSpace(a.draftIP)
	if ip == "" {
		a.deviceErr = "IP 地址不能为空"
		return
	}
	if net.ParseIP(ip) == nil {
		a.deviceErr = "IP 地址格式不正确"
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(a.draftPort))
	if err != nil || port < 1 || port > 65535 {
		a.deviceErr = "端口必须是 1–65535 之间的数字"
		return
	}
	user := strings.TrimSpace(a.draftUser)
	if user == "" {
		user = "admin"
	}
	d := model.Device{
		Name:     strings.TrimSpace(a.draftName),
		IP:       ip,
		Port:     port,
		Username: user,
		Password: a.draftPass,
		SSL:      a.draftSSL,
	}
	if a.editingDevice >= 0 && a.editingDevice < len(a.devices) {
		a.devices[a.editingDevice] = d
	} else {
		a.devices = append(a.devices, d)
		a.selectedDev = len(a.devices) - 1
	}
	a.deviceForm = false
	a.rebuildTargets()
	a.target = deviceName(d, a.selectedDev)
	if a.deviceList.Selected != nil {
		*a.deviceList.Selected = a.selectedDev
	}
	a.saveProfile()
	a.logf("设备已保存："+deviceName(d, a.selectedDev), "ok")
}

func (a *App) askDeleteDevice(i int) {
	if i < 0 || i >= len(a.devices) {
		return
	}
	a.confirm = confirmDeleteDevice
	a.pendingDevice = i
	name := deviceName(a.devices[i], i)
	a.confirmText = fmt.Sprintf("确定删除设备「%s」吗？", name)
}

func (a *App) confirmModal(c *ui.Context, t *ui.Theme) {
	if a.confirm == confirmNone {
		return
	}
	open := true
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(380).Gap(14).Children(func() {
			ui.Text(c, "请确认").FontSize(15).Bold()
			ui.Text(c, a.confirmText).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				ui.Button(c.Key("confirm-cancel"), "取消").MinWidth(80).OnClick(func() { a.confirm = confirmNone })
				ui.PrimaryButton(c.Key("confirm-ok"), "删除").MinWidth(80).OnClick(a.commitConfirm)
			})
		})
	})
	if !open {
		a.confirm = confirmNone
	}
}

func (a *App) commitConfirm() {
	switch a.confirm {
	case confirmDeleteDevice:
		i := a.pendingDevice
		if i >= 0 && i < len(a.devices) {
			name := deviceName(a.devices[i], i)
			a.devices = append(a.devices[:i], a.devices[i+1:]...)
			a.selectedDev = -1
			a.target = allDevices
			if a.deviceList.Selected != nil {
				*a.deviceList.Selected = -1
			}
			a.rebuildTargets()
			a.saveProfile()
			a.logf("已删除设备："+name, "warn")
		}
	case confirmDeleteProfile:
		name := a.pendingProfile
		if a.store != nil && a.store.DeleteProfile(name) {
			a.reloadProfiles()
			a.applyProfile(a.store.ActiveName())
			a.logf("已删除档案："+name, "warn")
		}
	}
	a.confirm = confirmNone
}

// ---- history --------------------------------------------------------------

func (a *App) historyModal(c *ui.Context, t *ui.Theme) {
	if !a.historyOpen {
		return
	}
	open := true
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(720).Height(520).Gap(12).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "历史报告").FontSize(15).Bold()
				ui.Spacer(c)
				ui.Button(c.Key("history-refresh"), "刷新").FontSize(11).OnClick(func() {
					a.historyList = nil
					if list, err := history.ListReports(); err == nil {
						a.historyList = list
					}
				})
				ui.Button(c.Key("history-close"), "关闭").FontSize(11).OnClick(func() { a.historyOpen = false })
			})
			if len(a.historyList) == 0 {
				ui.Text(c, "暂无历史报告").FontSize(12).TextColor(t.TextMuted)
				return
			}
			if a.historyListState.Selected != nil {
				if idx := *a.historyListState.Selected; idx >= 0 && idx < len(a.historyList) {
					a.historySel = idx
				}
			}
			ui.List(c, &a.historyListState, len(a.historyList), func(i int) {
				meta := a.historyList[i]
				row := ui.Column(c).Key("hist-"+meta.Path).Padding(6, 10).Gap(2).Radius(6)
				if i == a.historySel {
					row.Background(t.SurfacePressed)
				}
				row.OnClick(func() { a.historySel = i })
				row.ContextMenu(func(m *ui.Menu) {
					if m.Item("查看").Chosen() {
						a.loadHistory(i)
					}
					if m.Item("导出 CSV…").Chosen() {
						a.exportHistoryCSV(i)
					}
				})
				row.Children(func() {
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, meta.DeviceName).FontSize(12).Bold().SingleLine()
						ui.Text(c, meta.Health).FontSize(11).TextColor(statusColor(t, meta.Health))
						ui.Spacer(c)
						ui.Text(c, meta.ModTime.Format("2006-01-02 15:04")).FontSize(10).TextColor(t.TextMuted)
					})
					ui.Text(c, fmt.Sprintf("%s · %d 路", meta.IP, meta.Channels)).FontSize(10).TextColor(t.TextMuted)
				})
			}).Grow(1).Label("历史报告")
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				ui.Button(c.Key("history-view"), "查看").MinWidth(80).
					Disabled(a.historySel < 0).OnClick(func() { a.loadHistory(a.historySel) })
				ui.Button(c.Key("history-export"), "导出 CSV…").MinWidth(90).
					Disabled(a.historySel < 0).OnClick(func() { a.exportHistoryCSV(a.historySel) })
			})
		})
	})
	if !open {
		a.historyOpen = false
	}
}

func (a *App) loadHistory(i int) {
	if i < 0 || i >= len(a.historyList) {
		return
	}
	rep, err := history.LoadReport(a.historyList[i].Path)
	if err != nil || rep == nil {
		a.logf("读取历史报告失败", "error")
		return
	}
	a.report = rep
	a.reports = []model.Report{*rep}
	a.diff = nil
	a.selectedRow = -1
	a.historyOpen = false
	a.logf("已加载历史报告："+rep.DeviceName, "ok")
}

func (a *App) exportHistoryCSV(i int) {
	if i < 0 || i >= len(a.historyList) {
		return
	}
	rep, err := history.LoadReport(a.historyList[i].Path)
	if err != nil || rep == nil {
		a.logf("读取历史报告失败", "error")
		return
	}
	a.exportReportCSV(rep, rep.DeviceName+"-history.csv")
}

// ---- export ---------------------------------------------------------------

func (a *App) exportCurrentCSV() {
	if a.report == nil {
		return
	}
	a.exportReportCSV(a.report, a.report.DeviceName+"-"+a.report.IP+".csv")
}

func (a *App) exportCurrentTXT() {
	if a.report == nil {
		return
	}
	rep := a.report
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出 TXT 报告",
			DefaultPath: rep.DeviceName + "-" + rep.IP + ".txt",
			Filters:     []mygo.FileFilter{{Name: "Text", Extensions: []string{"txt"}}},
		})
		if err != nil || path == "" {
			return
		}
		if err := export.ExportTXT(path, *rep, ""); err != nil {
			a.logFromWorker("导出 TXT 失败: "+err.Error(), "error")
			return
		}
		a.post(func() { a.logf("已导出 TXT："+path, "ok") })
	}()
}

func (a *App) exportReportCSV(rep *model.Report, defaultName string) {
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Title:       "导出 CSV 报告",
			DefaultPath: defaultName,
			Filters:     []mygo.FileFilter{{Name: "CSV", Extensions: []string{"csv"}}},
		})
		if err != nil || path == "" {
			return
		}
		if err := export.ExportCSV(path, *rep); err != nil {
			a.logFromWorker("导出 CSV 失败: "+err.Error(), "error")
			return
		}
		a.post(func() { a.logf("已导出 CSV："+path, "ok") })
	}()
}
