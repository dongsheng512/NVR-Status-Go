# NVR Status 3.0（Go + MyGo）

海康威视 NVR 状态巡检工具。**自 2026-11 起，本仓库（Go 1.27 + [MyGo](https://github.com/egoist/mygo) v0.5.1）是唯一开发主线**。Python/PySide6 v2.3.0 保留在[独立旧仓库](https://github.com/dongsheng512/NVR-Status)，仅作归档；两个仓库彼此独立、历史不共享。

- **业务核心**：纯 Go，无 GUI 依赖；GUI、CLI、Agent 共用同一套 `internal/` 包。
- **GUI**：MyGo 原生绘制（GPU、无 WebView），发布产物只有它。
- **后台 Agent**：定时巡检 + `/healthz` + Prometheus `/metrics` + Webhook。
- **目标平台**：macOS / Windows / Linux。

## 快速开始

### 环境

- Go **1.27+**
- macOS 打包 `.app` 需要 MyGo 工具链；ffmpeg 捆绑是可选增强，详见下文。
- 构建工具链只需要 Go，仓库内没有 Python 脚本。

### 常用命令

```bash
go mod tidy            # 首次拉依赖
make gui               # → build/nvr-gui
make agent             # → build/nvr-agent
make build             # gui + agent
make test              # go test ./...
make vet               # go vet ./...

make cli               # → build/nvr（不进发布包）
make app               # → .app + .dmg（先捆绑 ffmpeg）
make run               # = make app + open -n

make tools             # → bin/nvrtool（供提交钩子使用）
```

> 模块缓存不可写时（例如卡在 `go: downloading ...`），可用：
> `GOMODCACHE=/tmp/gomod make app`。

## 构建与发布

### MyGo 打包

```bash
make app
# 产出：
#   build/darwin-arm64/NVR Status.app
#   build/darwin-arm64/NVR Status 3.0.0 arm64.dmg
```

`mygo.json` 的 `main` 指向 `./cmd/nvr-gui`。**应用包与 dmg 里只有 GUI**；`cmd/nvr` 和 `cmd/nvr-agent` 不会被打进去。Windows/Linux 可用：

```bash
go tool mygo build -platform windows/amd64,linux/amd64
```

### ffmpeg 捆绑（`make app` 的第一步）

`.app` 从 Finder / `open` 启动时继承的是 launchd 的 PATH（`/usr/bin:/bin:/usr/sbin:/sbin`），**不含** `/opt/homebrew/bin`。因此 `make app` 会把 ffmpeg 依赖一起放进：

```text
resources/darwin/bin/ → Contents/Resources/bin/
```

细节：

- `cmd/nvrtool bundle-ffmpeg` 用 `debug/macho` 读取 dylib 依赖闭包；
- 复制依赖后用 `install_name_tool` 改写成 `@loader_path/`，并逐个 ad-hoc 重签；
- 任何残留绝对路径都会报错退出，并实际运行 `ffmpeg -version` / `ffprobe -version` 自检；
- 产物被 `.gitignore` 忽略；换 ffmpeg 版本后重跑 `make ffmpeg`；
- 没装 ffmpeg 时默认 **警告并跳过**，`.app` 仍可构建，只是深度抽检自动降级；
- CI 可用 `go run ./cmd/nvrtool bundle-ffmpeg --require-ffmpeg` 强制检查；
- `NVR_FFMPEG_BIN` 可覆盖额外搜索路径（冒号分隔）。

带 ffmpeg 的 `.app` 约 49 MB，dmg 约 16 MB。Homebrew ffmpeg 是 `--enable-gpl` 构建，对外分发需遵守 GPL。

## 功能

### 状态巡检

- ISAPI Digest 认证，兼容常见海康固件差异；
- 设备信息、系统状态、报警、摄像头、录像计划；
- CMSearch 近期落盘检查（并发可调，支持跳过）；
- 硬盘容量、循环覆盖与休眠状态；
- 健康判定：摄像头在线、录像、落盘、音频抽检、硬盘使用率未知等统一口径。

### 深度音视频抽检

- 通过 ffmpeg/ffprobe 拉取短 RTSP 片段，检查视频/音频轨与音量；
- 音频判定按实测修正：电平未测到、无有效采样、volumedetect 失败不会被误报为「正常」；
- 保存片段可指定目录，文件名附带抽检时刻；
- **优先时段**（默认 10–18 点）和抽检日决定检索窗口，实际抽检点在可抽检窗口内**随机取一个能容纳完整片段的时间点**（本机时区），并遵守「距现在至少 10 分钟」约束；
- CLI 的 `--av-at` 可以钉到具体时刻；失败复检使用窗口内换时段策略。

保存片段有三个不同的时间，别混：

| 位置 | 含义 | 时区 |
|---|---|---|
| 保存目录名 `YYYYMMDD_HHMMSS`、文件修改时间 | 本次巡检**启动时刻** | 本机 |
| 文件名末尾 `HHMMSS` | 该片段的**抽检时刻** | **设备** |
| 视频画面水印 | 录像时刻 | 设备 |

设备时区与本机不一致时，文件名小时可能与设置不同，这是正常的；日志中的 `优先时段: …` 行会打印实际窗口和最终抽检点。

### 报告与历史

- CSV（utf-8-sig）/ TXT 导出；
- 历史报告目录与 Python v2 兼容，可直接读取旧报告；
- GUI 支持历史列表、结果对比（新增异常 / 已恢复 / 健康变化）；
- 结果区包含指标卡、通道表、硬盘卡、预警汇总与错误页。

### 后台 Agent（无 GUI）

```bash
make agent
./build/nvr-agent --interval 30m
# 或只跑一次
./build/nvr-agent --once --no-search
```

- 默认监听 `127.0.0.1:9101`；
- `GET /healthz`：最新巡检 JSON，有设备失败/严重时返回 503；
- `GET /metrics`：Prometheus 文本指标（`nvr_up`、`nvr_health_status`、`nvr_cameras_online`、`nvr_disk_usage_*` 等）；
- `GET /api/reports`：最新完整报告；
- `--profile <档案>` 会继承该档案的 `scan_options`；命令行显式传入的参数优先；
- `--webhook <url>`：巡检完成后 POST JSON，可接企业微信/钉钉中转；失败按 1s/2s 重试两次，4xx 不重试；
- `--token <t>` / `--listen <addr>`：绑非回环地址时自动生成并打印本次启动有效的 Bearer 令牌，也可用 `--token` 固定；
- 磁盘使用率无法解析时不会输出 0%，而是单独输出 `nvr_disk_usage_unknown{...} 1`。

### 命令行巡检（与 GUI 同步维护，不进发布包）

交付形态只有 GUI；`cmd/nvr` 用于真机排障、稳定文本留档和定点抽检。

```bash
make cli
./build/nvr --version
./build/nvr all                  # 当前档案全部设备
./build/nvr 2 --deep-av-check    # 第 2 台，含音视频抽检
./build/nvr -i 192.0.2.10 -w <密码> --av-channels 31,64
```

- 退出码：`0` 正常、`1` 有设备失败、`2` 无失败但存在「严重」；
- `Ctrl-C` / `SIGTERM` 返回 `130` / `143`；
- 参数全集见 `./build/nvr --help`。

## 配置与数据

- macOS 数据目录：`~/Library/Application Support/NVRStatus`
- Windows 数据目录：`%APPDATA%\NVRStatus`
- `profiles.json`、历史报告 `reports/`、系统钥匙串密码与 v2 兼容；
- 配置示例、测试 fixture、界面截图均使用示例设备名和 RFC 5737 文档地址，不提交真实设备名/IP/密码。

### 提交前脱敏检查

```bash
git config core.hooksPath .githooks
go run ./cmd/nvrtool check-secrets --all   # 手动全树自检
make tools                                 # 可选：预编译到 bin/nvrtool
```

钩子从本机 `NVRStatus/profiles.json` 读取真实值做精确比对，只报告文件与行号，不打印真值。

## 目录

```text
cmd/nvr/            CLI 巡检（不进发布包）
cmd/nvr-gui/        GUI 入口
cmd/nvr-agent/      常驻 Agent
cmd/nvrtool/        构建期工具（ffmpeg 捆绑、提交脱敏）
internal/           业务、界面、存储、报告等公共包
docs/               方案、截图、代码审查
resources/          图标与可选 ffmpeg 平台目录
```

## 当前进度（2026-11-05）

| 里程碑 | 状态 |
|---|---|
| M0 工程脚手架 | ✅ 完成 |
| M1 业务基础（配置/凭证/导出/历史） | ✅ 完成 |
| M2 ISAPI 客户端（Digest/XML/缓存） | ✅ 完成 |
| M3 存储与录像（CMSearch） | ✅ 完成 |
| M4 深度抽检（ffmpeg/RTSP） | ✅ 完成 |
| M5 健康判定 + 编排 + CLI | ✅ 完成 |
| M6 GUI MVP | ✅ 完成 |
| M7 GUI 功能对等 | ✅ 代码完成 |
| M8 测试补齐 + 多平台发布 | 🟡 进行中：Windows/Linux 产物、签名/公证、真机验收 |

macOS 的 ffmpeg 捆绑已完成；`.app` 仍是 ad-hoc 签名、未公证，正式分发前需完成签名和真机验收。

## 文档

- [PLAN.md](PLAN.md)：阶段计划与验收标准
- [FEATURE-OPPORTUNITIES.md](docs/FEATURE-OPPORTUNITIES.md)：后续功能机会
- [python-port-analysis-2026-11-04.md](docs/python-port-analysis-2026-11-04.md)：Python/Go 移植分析
- [code-review-2026-11-05.md](docs/code-review-2026-11-05.md)：2026-11-05 代码审查
- [docs/screenshots/](docs/screenshots/)：界面截图

## 变更记录

- **2026-10-28**：GUI 扫描、历史报告、循环覆盖降噪、设置弹窗与布局优化。
- **2026-11-04**：修复 GUI 空密码认证、补齐 `NoSearch` 传递与持久化、测试/截图脱敏。
- **2026-11-05**：日志自动跟随、扫描设置分栏、音频判定修正、磁盘未知口径、Go 构建工具链、保存目录选择器、优先时段随机抽检与代码审查。

## 图标

![NVR Status icon](resources/icon.png)

`resources/icon.png` 是 1024×1024 一次性素材，已提交；`go tool mygo build` 会据此生成 macOS `AppIcon.icns` 与 Windows `.ico`。
