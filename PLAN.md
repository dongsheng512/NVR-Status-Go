# Go + MyGo 重写执行计划

> 基线：cam-gui v2.3.0（Python/PySide6，已归档，仅作历史参考）
> 目标：Go 1.27 + MyGo v0.5.1
> 原则：**业务先行、GUI 后置；每阶段可独立验收。自 2026-11-05 起 Go 是唯一主线，
> 判定口径、报告字段与行为以 Go 自身正确性和真机结果为准，不再与 Python 版做双跑对照。**

## 总览

```
M0  工程脚手架                     0.5 周   ✅ 进行中
M1  业务基础（model/util/config/   1.5 周
    credentials/export/history）
M2  ISAPI 客户端（Digest/XML）     1 周
M3  存储与录像（CMSearch）         1.5 周
M4  深度抽检（ffmpeg/RTSP）        2 周
M5  健康判定 + 巡检编排 + CLI      1.5 周
M6  GUI MVP（MyGo）                1 周
M7  GUI 功能对等                   3–4 周
M8  测试迁移 + 真机回归 + 打包     2–3 周
```

合计约 4–5 人月。M0–M5 为可独立验证的 Go 业务核心；M6 起为 GUI——**GUI 是唯一交付形态**。

---

## M0 工程脚手架（0.5 周）✅

- [x] 新建 `go-rewrite/` 目录与 Go module（`nvrstatus`）
- [x] `mygo.json`、`Makefile`、`README.md`、本计划
- [x] 包结构：`cmd/`、`internal/{model,util,config,credentials,export,history,isapi,recording,storage,avprobe,health,scan,ui}`
- [x] 共享领域模型 `internal/model`
- [x] 纯函数工具 `internal/util`（含表驱动单测）
- [x] `go build ./...` / `go test ./...` 通过

**验收**：`make cli` 产出可执行文件；`go test ./...` 全绿。

## M1 业务基础（1.5 周）✅ 已完成

- [x] `internal/config`：profiles.json 读写、原子写、损坏备份、默认档案、导入导出、与 v2 schema 兼容
- [x] `internal/credentials`：macOS Keychain / Windows Credential Manager / 回退明文；target 命名与 v2 兼容
- [x] `internal/export`：CSV(utf-8-sig、公式注入防护) / TXT
- [x] `internal/history`：报告归档 JSON、列表、清理
- [x] 单测：配置迁移/损坏/原子写、凭证快照、导出字段、历史归档（35 个用例）

**验收**：`go test ./internal/{config,credentials,export,history}` 全绿；Windows/Linux 交叉编译通过。

## M2 ISAPI 客户端（1 周）✅ 已完成

- [x] `internal/isapi/client.go`：HTTP Digest 认证、GET/POST、超时、SSL 跳过校验
- [x] `internal/isapi/xml.go`：XML 安全解析（拒 DTD/ENTITY、8MiB 上限、命名空间）
- [x] 端点缓存 + 单飞（single-flight）+ 负缓存语义
- [x] 设备信息 `/System/deviceInfo`、系统状态 `/System/status`、告警、摄像头（InputProxy + 回退 Streaming）
- [x] 取消：`context.Context` 贯穿
- [x] `httptest` 单测：Digest 挑战、缓存、DTD 拒绝、摄像头合并

**验收**：`go test ./internal/isapi` 全绿（含 digest/缓存/并发单飞）。

## M3 存储与录像（1.5 周）✅ 已完成

- [x] `internal/storage`：硬盘状态、循环覆盖（13 种固件字段变体 + 满盘推断）
- [x] `internal/recording`：录像计划解析、CMSearch 分页（首页+末页）、`totalMatches` 容错、近 90 分钟回退、物理通道合并（101/102→通道1）
- [x] 设备时区探测与本地墙钟换算（`util.ParseHikTime`）
- [x] 单测：分页、末页、`searchResultPostion` 拼写、物理通道、时区

**验收**：`go test ./internal/{storage,recording}` 全绿。

## M4 深度抽检（2 周）✅ 已完成

- [x] `internal/avprobe`：ffmpeg/ffprobe 子进程、可取消（`exec.CommandContext`）、stderr 脱敏
- [x] RTSP 候选：鉴权注入、`:554` 回退、时间改写、时区模式探测（Z=UTC/local）
- [x] 视频/音频独立拉流（`-map 0:v:0` / `-map 0:a:0` + `volumedetect`）
- [x] 繁忙时段/定点时刻选点、复检换时段、`av_limit`/`av_channels` 过滤
- [x] 单测：伪 ffmpeg/ffprobe 端到端（37 个用例）

**验收**：`go test ./internal/avprobe` 全绿（含取消、候选、多时刻）。

## M5 健康判定 + 编排 + CLI（1.5 周）✅ 已完成

> 2026-11-04 起：**交付形态只保留 GUI**（`.app`/`.dmg` 里只有 `nvr-gui`），但 `cmd/nvr` **与 GUI 同步维护**，
> 不是废弃的遗留物 —— 它承载 GUI 没有的按通道/按时刻定点抽检，以及真机排障时可直接贴出的稳定文本报告。
> 同步的兜底成本很低：CLI 只是 `internal/scan` 的一层薄封装，业务改动都在 `internal/` 下自动继承；
> 唯一会漂移的是 `ScanOptions` 新增字段，已由 `cmd/nvr/parity_test.go` 与 `release_test.go` 卡住。

- [x] `internal/health`：`chanDetectResult` 白名单、双「未知」口径、严重度聚合（84 个子用例）
- [x] `internal/scan`：`Build`（选项 → NVR）、`Run`（编排+进度）、`Queue`（多设备）、`RunSingleAV`
- [x] `cmd/nvr`：全部参数（`--lookback`、`--deep-av-check`、`--av-channels`、`--av-at`、`--av-save`…）
- [x] 终端报告、退出码 0/1/2
- [x] 单测：健康用例、Build 降级/扩窗、伪 ISAPI 服务端到端

**验收**：`build/nvr --version` / `--help` / 伪 NVR 端到端通过；`go test ./...` 全绿。

## M6 GUI MVP（1 周）✅ 已完成

- [x] MyGo Window、左侧配置面板、扫描按钮、进度/日志
- [x] `ui.Table` 通道表（虚拟化、排序、筛选、多选/复制/详情）
- [x] `ui.Progress`、`ui.RichText` 预警、主题亮/暗/跟随系统
- [x] `ui.NewTester` 无窗口测试（空态、表格、设置、设备表单、详情、历史、三主题）
- [x] 扫描按钮接入 `scan.Runner`（单台/全部设备队列，支持取消）
- [x] 档案/设备表单接入（新增/编辑/删除、密码写入系统凭证库）

**验收**：`go test ./internal/nvgui`、`go build ./...`、`go tool mygo build` 均通过。

## M7 GUI 功能对等（3–4 周）✅ 代码完成（待三平台人工验收）

- [x] 档案条 CRUD / 导入导出；设备行增删改
- [x] 扫描设置全量表单；快速/深度/单路抽检；取消
- [x] 结果区指标卡、预警、详情窗、历史对话框、CSV/TXT 导出
- [x] 主题跟随系统、窗口/分栏记忆、崩溃日志
- [x] `ui.Table` 列宽/顺序持久化（`ListState.Columns` + `ui_settings.json`）
- [x] 多设备队列结果切换、样片目录打开、日志复制/清空

**验收**：功能清单逐项实现；真实 NVR 与三平台观感留待 M8 人工验收。

## M8 测试补齐 + 发布（2–3 周）

- [ ] 核对业务用例覆盖（Go 侧已有 234 个顶层用例），补齐判定边界与固件差异用例
- [ ] 伪 ffmpeg 端到端；真实 NVR 验收（重点是判定结论与真机现象一致，不再双跑对照）
- [ ] `make app` 三平台产物；签名/公证；ffmpeg 捆绑（macOS 已完成）
- [ ] 文档与发布说明

**验收**：真机上健康判定、落盘/音频抽检结论与实际状态一致；安装包可分发。

---

## 依赖关系（并行开发用）

```
M0 model/util ──┬─> M1 config/services
                ├─> M2 isapi ──> M3 recording/storage ──> M4 avprobe ─┐
                └──────────────────────────────────────> M5 health/scan/CLI
                                                                      │
                                                    M6/M7 GUI <───────┘
```

- M1、M2 可并行（互不依赖）
- M3 依赖 M2 的 `isapi.Client`
- M4 依赖 M3 的 `Record` 与设备时区
- M5 依赖 M1–M4
- GUI 可与 M4/M5 并行做骨架

## 质量门禁

- 每个包必须有表驱动单测；`go test ./...` 全绿
- 涉及健康判定/固件兼容的改动，用真机或伪 ISAPI 用例把结论锁住；改动判定逻辑时做变异验证
- 提交前 `go vet ./...`、`gofmt`
- 不在仓库中提交真实密码/内网 IP（沿用 v2 脱敏规则）
