# Go + MyGo 功能机会评估

> 基线：`go-rewrite` M0–M7（Go 1.27 / MyGo v0.5.1）· 2026-10-29
> 目的：找出「Python 版实现成本高 / Go 版天然适合」的功能，以及适合新增的运维能力。

## 0. 结论

Go 版目前只把 Go 当成了「更快的 Python」：业务逻辑搬过来了，但 **Go 的结构性优势还没用满**。最值得做的不是炫技功能，而是围绕 NVR 巡检的「**并发、常驻、可观测、可分享**」四件事：

1. **多设备并行巡检 + 全局 ffmpeg 并发预算**（最大即时收益）
2. **巡检报告对比 / 趋势图**（把一次性巡检变成可追踪的状态）
3. **托盘常驻 + 定时巡检 + 系统通知/Webhook**
4. **本地 HTTP 健康与 Prometheus 指标**
5. **单文件 HTML 报告 / 自动更新**（分发与协作）

这些在 Python/PySide6 + PyInstaller 体系里都能做，但要么依赖额外服务，要么跨平台打包/线程协调成本明显更高；Go + MyGo 把成本降到了「一个包、几个文件」。

**已落地（2026-10-29）**：
- ✅ 多设备并行巡检 + 全局 ffmpeg 预算（`internal/scan`、`internal/limit`、`internal/avprobe`）
- ✅ 巡检报告对比（`internal/history.Compare` + GUI「与上次相比」）
- ✅ 后台巡检 Agent + `/healthz` + Prometheus `/metrics`（`cmd/nvr-agent`、`internal/monitor`）

---

## 1. Go 已经兑现、但还没用满的底层优势

| 能力 | Python 版现状 | Go 版现状 / 可用能力 | 可落地的功能 |
|------|---------------|------------------------|--------------|
| 单二进制分发 | uv/venv + PyInstaller，Qt 插件与 ffmpeg 打包复杂 | 11.7MB 的 `.app`，CLI 10MB，无运行时依赖 | 内网直接分发；一个二进制跑 CLI/GUI/agent |
| 并发模型 | `ThreadPoolExecutor` + Qt Signal，线程池与 UI 生命周期需要人工协调 | goroutine + `context` + `errgroup`/semaphore，取消可向下贯穿 | 多 NVR 并行、跨设备 ffmpeg 限流、并行探测 |
| 子进程管理 | `subprocess.Popen` + 线程 kill；取消/超时边界多 | `exec.CommandContext`，进程组/超时/取消语义清晰 | 全局 ffmpeg 进程池、无残留进程、并发保护 |
| 原生桌面 API | PySide6 能做，但托盘/通知/更新/签名的打包链路长 | MyGo `NewFlyout`/`Tray`、`Notification`、`Dialog`、`global shortcut`、signed auto-update | 托盘常驻、通知、定时任务、自动更新 |
| 内置图表/表格/树 | Qt 需自定义 Model/View 或第三方图表 | MyGo `ui.Table`、`ui.Tree`、`ui/chart`、`ui.RichText`、`ui.NewTester` | 趋势图、设备树、报告对比、无头测试 |
| 静态资源嵌入 | `importlib.resources` + PyInstaller 资源路径 | `embed` + `html/template` | 单文件 HTML 报告、内置帮助/许可证 |
| 跨平台编译 | 每个平台单独 PyInstaller、Qt 插件差异 | 纯 Go、`mygo build -platform` 多平台产物 | Windows/Linux/macOS 同源构建 |

> 关键判断：**I/O 密集的 NVR 巡检非常适合 goroutine**；Python 的 GIL 对 I/O 影响不大，但线程池、取消、生命周期与 Qt 主线程的协调成本明显高于 Go。

---

## 2. Python 不容易 / Go 更容易的功能

### 2.1 多设备并行巡检 + 全局资源预算（P0，约 1–2 天）

**现状**：`internal/scan/scan.go:257 QueueWith` 是**顺序**遍历设备；`internal/recording/recording.go:128` 每台设备内部有 CMSearch 并发，但多台 NVR 是串行的。4 台设备时总耗时≈4×单台。

**Go 做法**：
- `Runner.QueueConcurrent(devices, deviceWorkers)`：设备级 worker pool，结果顺序保持原设备顺序。
- 每台设备仍保留 `recording.SearchWorkers`，但增加全局 `semaphore.Weighted`，限制**所有设备合计**的 CMSearch 并发。
- ffmpeg 深度抽检增加全局 `avGlobalWorkers`（例如 4–6），防止多设备同时抽检把 NVR/本机压垮。
- 进度映射按「设备完成数 + 设备内进度」计算。

**价值**：4 台设备从「串行 4×T」降到约「max(单台 T, 总量/并发)」；对日常巡检最直接。
**Python 难点**：需要跨线程池共享信号量、跨设备取消和 Qt 进度归并，容易出现线程泄漏/取消后继续跑。

### 2.2 巡检报告对比与趋势（P0，约 1–2 天）

**现状**：`internal/history` 已按时间保存 JSON 报告（`reports/`），`ListReports` 可读历史；但只能看单次。

**Go 做法**：
- `history.Compare(prev, cur)`：按通道 + IP 做 diff，输出「新增异常 / 已恢复 / 持续异常 / 健康变化」。
- 历史趋势数据：每次报告抽取 `在线数、录像正常数、磁盘使用率`，在 GUI 用 MyGo `ui/chart` 画折线。
- 报告详情增加「上次对比」区块；导出 CSV 增加 diff 列。

**价值**：把「巡检」从一次性检查变成「可追踪状态」，非常适合运维。
**Python 难点**：JSON 对比本身不难，但趋势图、历史列表与 Qt 打包/字体/图表依赖会提高成本。

### 2.3 托盘常驻 + 定时巡检 + 通知/Webhook（P1，约 2–3 天）

**现状**：GUI 是普通窗口，关闭即结束；巡检是手动触发。

**Go/MyGo 做法**：
- `mygo.NewFlyout(mygo.FlyoutOptions{Tray: ...})` 做菜单栏/托盘面板；
- `time.Ticker` 定时巡检（例如每 30 分钟快速巡检，夜间深度抽检）；
- `mygo.Notification` 在「健康变差 / 新异常通道 / 磁盘满」时通知；点击通知打开对应报告；
- 企业微信/钉钉/邮件 Webhook：Go `net/http` 一个文件即可发送 JSON。

**价值**：从「人找问题」变成「问题找人」，这是 NVR 巡检工具最自然的产品升级。
**Python 难点**：PySide6 托盘 + 定时线程 + 通知 + 打包后的后台行为（macOS LSUIElement/Windows 启动项）需要较多平台胶水。

### 2.4 本地 HTTP 健康/指标接口（P1，约 1–2 天）

**现状**：CLI 只能人看；GUI 只能本机看。

**Go 做法**：
- `nvr --serve :9101` 启动 `net/http`：
  - `GET /healthz`：最近一次各设备健康状态 JSON；
  - `GET /metrics`：Prometheus 文本格式，例如 `nvr_up{device="NVR-A"} 1`、`nvr_channels_online`、`nvr_disk_usage_percent`、`nvr_last_scan_timestamp`。
- 可被 Prometheus/Grafana/Uptime Kuma 直接采集。

**价值**：接入现有监控体系，无需装 Python/Agent。
**Python 难点**：需要额外 web 框架与部署；Go 标准库即可，单二进制更符合内网工具分发。

### 2.5 单文件 HTML 报告（P1，约 0.5–1 天）

**Go 做法**：`embed` 模板 + `html/template` 生成一个自包含 HTML（内联 CSS/JS/SVG 图表），发微信/邮件/工单即可打开；CLI 增加 `--html`，GUI 增加「导出 HTML」。

**价值**：比 CSV/TXT 更适合跨团队分享，且不需要对方装任何东西。
**Python 难点**：Jinja2 + 资源路径 + PyInstaller 打包模板，容易在打包后路径失效。

### 2.6 自动更新 / 多平台安装包 / CI（P2，约 2–3 天）

**MyGo 原生优势**：`go tool mygo build` 已产出签名 `.app`/`.dmg`；文档还内置 Windows 安装器、Debian 包、签名、公证、**增量自动更新**（Sparkle 风格）。Python 版要自己维护 PyInstaller + 签名/公证 + 更新器。

**建议**：GitHub Actions 矩阵构建 + `mygo build -platform darwin/universal,windows/amd64,linux/amd64`；发布页放 `.dmg` / `.exe` / `.deb`。

### 2.7 局域网自动发现（P2，约 2–4 天，按需）

**Go 做法**：UDP 广播/组播、WS-Discovery（ONVIF）扫描同网段 NVR，结果填入「添加设备」；可结合 Hikvision ISAPI 的 `deviceInfo` 验证。
**Python 难点**：socket/多网卡/超时处理繁琐；Go 的 `net` + goroutine 更自然。

### 2.8 全局快捷键 / Deep Link / 文件拖放（P2，约 1–2 天）

- `mygo` 全局快捷键：一键触发快速巡检；
- `nvrstatus://scan?device=NVR-A` deep link；
- 拖入报告 JSON 直接打开历史报告；
- macOS 菜单栏常驻 + 状态点。

这些是 MyGo 自带桌面 API，Python/Qt 需要更多平台代码。

---

## 3. 不建议跟风做的功能

| 功能 | 原因 |
|------|------|
| 应用内实时视频预览 / 播放器 | 需要解码/渲染/GPU/音频同步，复杂度和包体积远超收益；继续用 NVR 客户端或 Web 预览 |
| 整段录像下载 | 违背现有「不写 NVR 盘、不整段下载」的安全约定；单段可达 GB |
| 全厂商 ONVIF 支持 | 海康 ISAPI 的固件差异已经很多；多厂商会指数级增加兼容成本 |
| 把所有功能塞进 GUI 主进程 | 定时巡检/HTTP 服务应做成独立 agent 或后台 goroutine，GUI 崩溃不应影响监控 |

---

## 4. 建议的落地顺序

| 顺序 | 功能 | 预估 | 理由 |
|------|------|------|------|
| 1 | 多设备并行巡检 + 全局 ffmpeg/CMSearch 预算 | 1–2 天 | 直接缩短巡检时间，风险可控（有单台并发上限） |
| 2 | 报告对比 + 趋势图 | 1–2 天 | 把历史 JSON 变成价值，无需新数据源 |
| 3 | 托盘常驻 + 定时巡检 + 通知/Webhook | 2–3 天 | 产品形态升级，最贴近「巡检工具」使用方式 |
| 4 | HTTP `/healthz` + `/metrics` | 1–2 天 | 接入现有监控，几乎零依赖 |
| 5 | 单文件 HTML 报告 | 0.5–1 天 | 分享成本最低，适合跨部门 |
| 6 | 自动更新 + CI 多平台产物 | 2–3 天 | 发布体验；MyGo 已内置大半 |
| 7 | 局域网发现 / 全局快捷键 | 按需 | 属于便利性增强，不是核心 |

---

## 5. 代码落点（当前仓库）

- 多设备并发：`internal/scan/scan.go:257`（`QueueWith` 目前顺序）
- 全局 ffmpeg 预算：`internal/avprobe/avprobe.go:1456`（每台设备自己的 `AVWorkers`）
- 录像检索并发：`internal/recording/recording.go:128`（每台设备自己的 `SearchWorkers`）
- 历史报告/对比：`internal/history/history.go`（已有 `ListReports` / `LoadReport`，加 `Compare`）
- 导出：`internal/export/export.go`（加 `ExportHTML`，用 `embed` 模板）
- GUI：`internal/nvgui/`（MyGo `ui/chart`、`ui.Tree`、`Tray`/`Flyout`、`Notification` 尚未使用）
- 打包：`mygo.json` / `go tool mygo build`（已支持多平台与签名/更新配置）

## 6. 风险提示

- **并发不是越大越好**：NVR 的 RTSP/HTTP 能力有限；建议默认 `device_workers=2–3`、全局 ffmpeg 上限 4–6，并在设置中可调。
- **取消语义**：多设备并发后要保证「取消一台/取消全部」都不残留 ffmpeg 进程；现有 `context` 机制可复用。
- **通知疲劳**：只对「健康从良好变警告/严重」「新异常通道」「磁盘 ≥95%」通知，不要每次巡检都弹。
- **HTTP 服务安全**：默认只监听 `127.0.0.1`；需要局域网访问时再加 token/来源限制。
- **Windows 托盘/服务**：需要单独验收，MyGo Windows 后端已支持但托盘行为与 macOS 不同。
