# NVR Status 3.0（Go + MyGo）

海康威视 NVR 状态巡检工具。**自 2026-11 起，本仓库（Go 1.27 + [MyGo](https://github.com/egoist/mygo) v0.5.1）是唯一开发主线**。Python/PySide6 的 v2.3.0 留在[独立旧仓库](https://github.com/dongsheng512/NVR-Status)，已停止维护、仅作归档与历史参考；**两个仓库彼此独立、历史不共享**。自 2026-11-05 起，判定口径与报告行为以 Go 自身正确性和真机结果为准，不再与 Python 版做双跑对照。

- 业务核心：纯 Go，无 GUI 依赖；GUI 与后台 Agent 共用同一套 `internal/` 包
- GUI：MyGo 原生绘制（GPU、无 WebView），已实现功能对等（M7）—— **发布产物只有它**
- 后台巡检 Agent：定时巡检 + `/healthz` + Prometheus `/metrics` + Webhook（可选，随需自行构建）
- 目标平台：macOS / Windows / Linux

## 目录

```
cmd/nvr/            命令行巡检（与 GUI 同步维护，**不进发布包**）
cmd/nvr-gui/        GUI 入口
cmd/nvr-agent/      无 GUI 的常驻巡检 Agent
internal/model/     领域模型（设备 / 相机 / 通道记录 / 结果）
internal/util/      纯函数工具（通道/时刻解析、ffmpeg 探测、文件名）
internal/config/    多档案配置（与 v2 profiles.json 兼容）
internal/credentials/ 系统 keyring
internal/export/    CSV(utf-8-sig) / TXT 导出
internal/history/   巡检报告归档与「与上次相比」
internal/isapi/     ISAPI 客户端（Digest / XML / 缓存 / 取消）
internal/recording/ 录像计划与 CMSearch 落盘
internal/storage/   硬盘与循环覆盖
internal/avprobe/   ffmpeg 深度音视频抽检
internal/health/    健康判定
internal/scan/      巡检编排（CLI/GUI/Agent 共用）
internal/nvgui/     MyGo 界面
internal/monitor/   Agent 的快照与指标
internal/report/    终端报告渲染
cmd/nvrtool/        构建期工具（ffmpeg 捆绑、提交脱敏，Go 实现）
docs/               功能机会评估与界面截图
resources/          应用图标
```

## 构建

```bash
go mod tidy          # 首次
make gui             # → build/nvr-gui        （发布产物）
make agent           # → build/nvr-agent      （可选，无人值守监控）
make build           # gui + agent
make test            # go test ./...
make vet             # go vet ./...

make cli             # → build/nvr  与 GUI 同步维护，不进发布包
make app             # → .app + .dmg（自带 ffmpeg，见下）
make run             # = make app + open -n，日常验证用这一条
make tools           # → bin/nvrtool（预编译构建工具，供提交钩子走快速路径）
```

> **构建工具链只需 Go 1.27+**：仓库内没有 Python 脚本，运行时也没有 Python 依赖。
> 打包需要的 ffmpeg 捆绑、提交脱敏检查都在 `cmd/nvrtool` 里；外部只调 macOS 的
> `install_name_tool` / `codesign`。若卡在 `go: downloading ...` 很久或被 SIGTERM 杀掉，
> 说明模块缓存不可写，改用 `GOMODCACHE=/tmp/gomod make app`。

### MyGo 打包

```bash
make app                  # 先捆 ffmpeg，再打包
# 产出 build/darwin-arm64/NVR Status.app
#      build/darwin-arm64/NVR Status 3.0.0 arm64.dmg

go tool mygo build        # 不捆 ffmpeg 的裸包，仅供快速迭代
```

`mygo.json` 的 `main` 指向 `./cmd/nvr-gui`。**应用包与 dmg 里只有 GUI**——命令行巡检
（`cmd/nvr`）和 Agent（`cmd/nvr-agent`）都不会被打进去。Windows/Linux 产物用
`go tool mygo build -platform windows/amd64,linux/amd64`（需对应平台打包工具）。

#### ffmpeg 捆绑（`make app` 的第一步）

`.app` 被 Finder 或 `open` 启动时继承的是 **launchd** 的 PATH
（`/usr/bin:/bin:/usr/sbin:/sbin`），**不含 `/opt/homebrew/bin`**。而 `util.ToolBinDirs`
只查 bundle 内 `bin/` 与 PATH 两处，所以不捆 ffmpeg 的包在从图标启动时会把深度抽检
静默降级成「ffmpeg 缺失」。`make app` 用 `go run ./cmd/nvrtool bundle-ffmpeg` 解决这个问题：

- `resources/darwin/bin/` → MyGo 复制到 `Contents/Resources/bin/`，正是
  `ToolBinDirs` 的第一个查找位置（平台目录写法让 Windows/Linux 产物不带 mac 库）
- Homebrew 的 `ffmpeg` 只有 420K，真正的体量在 19 个 dylib 里，且按绝对路径引用
  `/opt/homebrew/...`。工具用 `debug/macho` 读依赖闭包（不再需要 `otool`），一起复制后
  用 `install_name_tool` 改写成 `@loader_path/`，再逐个 ad-hoc 重签
  （改 load command 会让签名失效，arm64 拒绝加载）
- 工具自我校验：任何文件残留绝对路径就报错退出，并把两个二进制真跑一次 `-version`
- 产物在 `.gitignore` 里，不进仓库；换了 ffmpeg 版本重跑 `make ffmpeg`
- **本机没装 ffmpeg 时默认警告并跳过**，不阻断 `make app`（fresh clone / CI 装依赖前也能出包），
  只是这个 `.app` 的深度抽检会降级。CI 里用
  `go run ./cmd/nvrtool bundle-ffmpeg --require-ffmpeg` 强制检查，避免"忘了装"被悄悄发布。
  可用 `NVR_FFMPEG_BIN` 覆盖额外搜索路径（冒号分隔）

代价是 `.app` 从 11.6 MB 涨到 49.4 MB（dmg 16.1 MB）。注意本机 Homebrew 的 ffmpeg 是
`--enable-gpl` 构建，对外分发要遵守 GPL。

## 与 Python v2.3.0 的关系（历史）

- 配置格式、用户数据目录（`~/Library/Application Support/NVRStatus`、`%APPDATA%\NVRStatus`）保持兼容；已有的 `profiles.json`、历史报告 `reports/`、系统钥匙串密码可直接沿用。
- CLI 参数与报告字段沿用 v2.3.0 的命名，方便老用户过渡；健康判定口径（`chanDetectResult` 白名单，以及「落盘未知」「音频抽检未知」「硬盘使用率未知」三处同用的比例口径）以当前 Go 实现为准，**不再要求与 Python 逐字一致**。
- 音频判定有两处按实测修正过（电平未测到 / 配置未知且无音轨不再误判为「正常」「异常」），原因见 [移植分析 §7](docs/python-port-analysis-2026-11-04.md)。那是现行口径，不是待对齐的差异。
- 本仓库于 2026-11-04 新建为干净的 Go 版主线，**不带 v2.x 的提交历史、tag 与 release**；与 Python 版仓库是**两个独立仓库**，不是分支、不是 fork。
- 后续功能开发、issue 与发布都以本仓库为准；Python 版不再参与验收。
- 详细阶段与验收标准见 [PLAN.md](PLAN.md)。

## 提交前脱敏检查（务必启用）

本工具的配置示例、测试 fixture、界面截图都容易夹带真实设备名或内网 IP。仓库内置一道提交前门禁：

```bash
git config core.hooksPath .githooks
# 之后每次 git commit 会运行 cmd/nvrtool check-secrets（Go 实现）
go run ./cmd/nvrtool check-secrets --all   # 手动全树自检
make tools                                 # 可选：预编译到 bin/nvrtool，钩子走快速路径
```

脚本从本机 `NVRStatus/profiles.json` 读取真实值做精确比对，**只报文件与行号、不打印真值**，命中即以非零码阻断提交；未找到本地档案时退化为内网地址提醒（只警告不阻断）。真值本身永不写入仓库。

## 当前进度（2026-11-04）

| 里程碑 | 状态 |
|---|---|
| M0 工程脚手架 | ✅ 完成 |
| M1 业务基础（配置/凭证/导出/历史） | ✅ 完成 |
| M2 ISAPI 客户端（Digest/XML/缓存） | ✅ 完成 |
| M3 存储与录像（CMSearch） | ✅ 完成 |
| M4 深度抽检（ffmpeg/RTSP） | ✅ 完成 |
| M5 健康判定 + 编排 + CLI | ✅ 完成（CLI 与 GUI 同步维护，不进发布包） |
| M6 GUI MVP | ✅ 扫描接入、档案/设备表单、通道表 |
| M7 GUI 功能对等 | ✅ 代码完成（待三平台人工验收） |
| M8 测试补齐 + 多平台发布 | 🟡 进行中：业务用例覆盖核对、真机验收、Windows/Linux 产物、签名/公证待做；**macOS 的 ffmpeg 捆绑已完成**（`make app`） |

```bash
# 在仓库根目录
go test ./...          # 全部通过（含伪 ISAPI 端到端、伪 ffmpeg）
make gui               # → build/nvr-gui（发布产物）
```

> 已知发布门禁未过：`.app` 仍是 ad-hoc 签名、未公证。ffmpeg 已随包捆绑（`make app`），
> 从 Finder/`open` 启动也能做深度抽检，不再依赖目标机器装没装 ffmpeg。

## 2026-10-28 UI 修复记录

- **扫描功能根因修复**：GUI 原先用 `GetProfile` 装载设备（该方法按设计返回空密码），导致所有 GUI 扫描用空密码认证失败；改用 `ResolveDevices` 从系统凭证库补全密码后，GUI 与 CLI 行为一致（已用一台 NVR 验证：64 路通道、健康警告）。
- **历史报告兼容**：`internal/history` 目录改为与 Python 版一致的 `<应用数据>/reports`，可直接读取已有的历史报告；文件命名/上限（500）保持一致。
- **循环覆盖探测降噪**：`/ContentMgmt/Storage/quota`、`record/AdvanceParam` 等固件差异端点改用静默探测；设备返回 403 时不再刷黄色警告（仍继续尝试下一个端点/推断）。
- **扫描设置改为弹窗**：原 340px 侧栏内联表单在 1000×700 下单位下拉被裁切、深度选项不可见；设置改为 620px 模态，分组展示基础/深度抽检。
- **布局优化**：隐藏原生标题栏，侧栏内容延伸到窗口顶部（macOS 红绿灯保留内缩）；设备列表高度随设备数自适应；日志区填充剩余空间，底部主题/历史固定在底部；设置按钮显示当前摘要。
- **结果区优化**：结果区顶部留白压缩，指标卡明细精简并加 tooltip；通道表列宽压缩，1000px 宽度下「音频抽检」列不再被裁切；扫描失败显示明确的错误页与「重试」按钮。
- **设备区重构**：设备区改为「全部设备」模式行 + 设备列表；列表行显示上次巡检状态点与健康结论，点选即切换巡检目标。设备选择收起为「当前巡检目标」按钮，添加与管理收进按钮。
- **交互补强**：设备选中/编辑、表格行选中、历史行选中、设置按钮点击均有 `ui.NewTester` 交互测试；记住上次巡检设备（`ui_settings.json.last_target`）。
- UI 截图见 [docs/screenshots/](docs/screenshots/)。

> 注：部分设备可能因网络或固件原因对 ISAPI HTTP 返回空响应（Python 版与 curl 同样失败），建议先在命令行确认连通性；这不是 GUI 代码问题。

## 2026-11-04 修复记录

- GUI「跳过落盘检索」选项此前只绑到表单状态、未传入巡检选项，导致勾选既不生效也不写回档案；已修复并补 `TestNoSearchOptionIsAppliedAndPersisted`（含变异测试验证）。
- 界面截图与测试 fixture 全部改用示例设备名和 RFC 5737 文档地址。

## 应用图标

![NVR Status icon](resources/icon.png)

- 1024×1024 PNG：`resources/icon.png`（已提交，是一次性素材，不在构建链中）
- `go tool mygo build` 会自动从 `resources/icon.png` 生成 macOS `AppIcon.icns` 与 Windows `.ico`

## 后台巡检 Agent（无 GUI）

```bash
go build -o build/nvr-agent ./cmd/nvr-agent
./build/nvr-agent --interval 30m
```

- 默认监听 `127.0.0.1:9101`
- `GET /healthz`：最新巡检 JSON，有设备失败/严重时返回 503
- `GET /metrics`：Prometheus 文本指标（`nvr_up`、`nvr_health_status`、`nvr_cameras_online`、`nvr_disk_usage_percent` 等）
- `GET /api/reports`：最新完整报告
- `--webhook <url>`：巡检完成后 POST JSON，可接企业微信/钉钉中转。失败按 1s/2s 重试两次；4xx 不重试（重发同样的载荷没意义）
- `--once`：只跑一次后退出
- `--token <t>` / `--listen <addr>`：见下

**鉴权**：三个端点都会返回设备名、IP、通道名。

- 绑回环地址（默认）：不要求令牌，开箱即用。
- 绑非回环地址（如 `--listen 0.0.0.0:9101`）：**自动生成本次启动有效的令牌并打印**，所有端点需带
  `Authorization: Bearer <令牌>`；想固定令牌就用 `--token` 指定。Prometheus 侧配 `bearer_token_file` 即可。

> 磁盘使用率无法解析时不会退化成 `0%`（那会被读成"空盘"），而是单独输出 `nvr_disk_usage_unknown{...} 1`。

## 命令行巡检（与 GUI 同步维护，不进发布包）

**交付形态只有 GUI**：`.app` / `.dmg` 里只装 `nvr-gui`，不打包命令行巡检。
但 `cmd/nvr` **会跟着一起更新**，不是一次性遗留物 —— 新功能仍以 GUI 为第一入口，CLI 同步跟上。

它有三条 GUI 替代不了的路径：

1. **真机排障**：SSH 到现场机器上直接打一轮，把 stderr 日志和文本报告贴出来，比让人截图快得多。
2. **稳定文本便于比对与留档**：同一设备的巡检结果可以直接 `diff`、贴进工单或接进脚本；GUI 只能肉眼比。
3. **定点抽检**：`--av-channels` 按物理通道号筛、`--av-at` 指定具体时刻，这两项目前只在 CLI 上有
   （GUI 未暴露；`cmd/nvr` 因此比 GUI 多出两种抽检能力，不是功能子集）。

```bash
make cli                       # → build/nvr
./build/nvr --version          # 3.0.0-dev
./build/nvr all                # 当前档案的全部设备
./build/nvr 2 --deep-av-check  # 第 2 台，含音视频抽检
./build/nvr -i 192.0.2.10 -w <密码> --av-channels 31,64
```

- 退出码：`0` 正常、`1` 有设备失败、`2` 无失败但存在「严重」；`Ctrl-C`/`SIGTERM` 返回 `130`/`143`。
- 参数全集见 `./build/nvr --help`。

### 两侧不漏同步的保障

CLI 只是 `internal/scan` 的一层薄封装：健康判定、抽检、CMSearch 这些改动都在 `internal/` 下，
**CLI 自动继承，不用动它**。真正会漂移的只有一处 —— `model.ScanOptions` 新增字段时，
GUI 表单接了、CLI 忘了加开关。这条用测试卡住，不靠记忆：

| 测试 | 拦住什么 |
| --- | --- |
| `TestEveryScanOptionHasACLIFlagOrIsExempt` | 新增 `ScanOptions` 字段却没加 CLI 开关（或没在 `cliExempt` 写明原因）；豁免条目过期也会报错 |
| `TestEveryScanOptionFlagReachesOptions` | 开关加了、但 `buildOptions` 里漏了赋值 —— 参数传了不生效 |
| `TestReleaseBuildDoesNotIncludeCLI` | `make build` 被改成依赖 `cli`，发布构建连带产出 CLI |
| `TestOnlyCLITargetBuildsTheCLIBinary` | 除 `cli` 外还有别的目标引用 `build/nvr` —— 被打进 `.app` 的前兆 |

加字段时不用记规则，跑一次 `go test ./cmd/nvr/` 就会告诉你缺哪一步。

> 这四条都做过变异验证（故意改坏 → 确认测试变红 → 还原），不是「写了就算数」。
