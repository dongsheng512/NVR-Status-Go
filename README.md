# NVR Status 3.0（Go + MyGo）

海康威视 NVR 状态巡检工具。**自 2026-11 起，本仓库（Go 1.27 + [MyGo](https://github.com/egoist/mygo) v0.5.1）是唯一开发主线**；Python/PySide6 的 v2.3.0 保留在本地作为行为对照（oracle），不再随本仓库分发。

- 业务核心与 CLI：纯 Go，无 GUI 依赖，可单独构建为 `nvr` 单二进制
- GUI：MyGo 原生绘制（GPU、无 WebView），已实现功能对等（M7）
- 后台巡检 Agent：定时巡检 + `/healthz` + Prometheus `/metrics` + Webhook
- 目标平台：macOS / Windows / Linux

## 目录

```
cmd/nvr/            CLI 入口（多设备巡检）
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
docs/               功能机会评估与界面截图
scripts/            图标生成、提交前脱敏检查
resources/          应用图标
```

## 构建

```bash
go mod tidy          # 首次
make cli             # → build/nvr
make gui             # → build/nvr-gui
make agent           # → build/nvr-agent
make test            # go test ./...
make vet             # go vet ./...
```

> MyGo 要求 Go 1.27+。若 `go env GOMODCACHE` 不可写（沙箱环境），使用
> `GOMODCACHE=/tmp/gomod make build`。

### MyGo 打包

```bash
go tool mygo build
# 产出 build/darwin-arm64/NVR Status.app
#      build/darwin-arm64/NVR Status 3.0.0 arm64.dmg
```

`mygo.json` 的 `main` 指向 `./cmd/nvr-gui`。Windows/Linux 产物用
`go tool mygo build -platform windows/amd64,linux/amd64`（需对应平台打包工具）。

## 与 Python v2.3.0 的关系 / 迁移说明

- 配置格式、用户数据目录（`~/Library/Application Support/NVRStatus`、`%APPDATA%\NVRStatus`）保持兼容；已有的 `profiles.json`、历史报告 `reports/`、系统钥匙串密码可直接沿用。
- CLI 参数、报告字段、健康判定口径（含 `chanDetectResult` 白名单与双「未知」比例口径）与 v2.3.0 对齐。
- 本仓库于 2026-11-04 重建为干净的 Go 版主线，**不带 v2.x 的提交历史、tag 与 release**；旧代码仅留本地作 oracle 双跑，直到 M8 的逐字段对照完成。
- 详细阶段与验收标准见 [PLAN.md](PLAN.md)。

## 提交前脱敏检查（务必启用）

本工具的配置示例、测试 fixture、界面截图都容易夹带真实设备名或内网 IP。仓库内置一道提交前门禁：

```bash
git config core.hooksPath .githooks
# 之后每次 git commit 会自动运行 scripts/check-secrets.py
python3 scripts/check-secrets.py --all   # 也可手动全树自检
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
| M5 健康判定 + 编排 + CLI | ✅ 完成 |
| M6 GUI MVP | ✅ 扫描接入、档案/设备表单、通道表 |
| M7 GUI 功能对等 | ✅ 代码完成（待三平台人工验收） |
| M8 测试迁移 + 多平台发布 | ⬜ 待做：~200 个业务用例迁移、真机 A/B 逐字段对照、三平台产物、签名/公证、ffmpeg 捆绑 |

```bash
# 在仓库根目录
go test ./...          # 全部通过（含伪 ISAPI 端到端、伪 ffmpeg）
go build -o build/nvr ./cmd/nvr
go build -o build/nvr-gui ./cmd/nvr-gui
./build/nvr --version  # 3.0.0-dev
```

> 已知发布门禁未过：当前 `.app` 为 ad-hoc 签名、未捆绑 ffmpeg；换机器需先安装 ffmpeg 才能做深度抽检。

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

- 1024×1024 PNG：`resources/icon.png`
- 由 `python3 scripts/make_icon.py` 生成（Pillow，4× 超采样后缩放）
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
- `--webhook <url>`：巡检完成后 POST JSON，可接企业微信/钉钉中转
- `--once`：只跑一次后退出

> 安全提示：`/healthz` 与 `/api/reports` 会返回设备名、IP 与通道名，且无鉴权。默认只绑 loopback；改用 `--listen 0.0.0.0:9101` 前请自行加访问控制。
