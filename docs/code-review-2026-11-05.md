# 2026-11-05 代码审查与功能逻辑检查

> 审查对象：`nvr-status-go` 今天（2026-11-05）的 9 个提交
> 基线：`main`，`git status` 审查开始时干净；本文记录审查时发现的问题与核验结果。

## 0. 结论

**没有发现会阻塞当前 main 的 P0 问题；业务逻辑和 GUI 主路径可用。**
今天的主要改动（日志跟随、扫描设置布局、音频判定、磁盘未知口径、Go 工具链、保存目录、优先时段）均有对应测试或截图验证。

审查中发现并已在本工作区修复 1 个一致性问题（未提交）：

- **P1：硬盘使用率解析失败时，健康判定为警告，但 CLI 报告和 GUI 硬盘卡仍显示“正常”**。

仍建议跟进 2 个问题：

- **P1：`nvr-agent` 不读取 profiles.json 的 `scan_options`**，定时 Agent 不会沿用 GUI 中设置的优先时段、设备并发、深度抽检等。**本轮已修复。**
- **P2：优先时段取点语义**：已按用户要求改为在可抽检窗口内随机取一个能容纳完整片段的起点，不再取中点。

---

## 1. 门禁结果

| 检查 | 结果 |
|---|---|
| `go test ./... -count=1` | ✅ 全部通过 |
| `go test -race ./... -count=1` | ✅ 全部通过 |
| `go vet ./...` | ✅ 无输出 |
| `gofmt`（审查后工作区） | ✅ 无 diff |
| GUI 截图 | ✅ `SHOT=1` 渲染 1280×820 与 1000×700；扫描设置最高状态（深度 + 保存）无裁切 |

审查覆盖的今天提交：

```text
e548385 fix(gui): follow the log as it streams, and lay the settings out in two columns
995a52d feat(gui): split the settings dialog into named, separated columns
e30dd42 fix(avprobe): stop reporting an unmeasured audio track as healthy
1262d64 docs: drop the Python v2.3.0 oracle as a live constraint
bedfca5 fix: three first-tier hardening items
5fe08e4 refactor(toolchain): replace all Python build scripts with one Go tool
abecdc5 feat(gui): pick the sample save directory with a native folder dialog
efb3ea5 fix(gui): keep the save-directory row inside 时段与保存, with a compact field
f929575 fix(avprobe): make the preferred-hours setting actually move the probe instant
```

---

## 2. 发现与处理

### P1-1（已修复）硬盘使用率未知：健康结论与显示不一致

`bedfca5` 在 `health.Summarize` 中新增了 `DiskUsageUnknown`，并规定“所有返回硬盘的使用率都解析失败”时健康状态升为警告。但显示层没有同步：

- `internal/report/report.go` 的硬盘行仍只看 `avg` 和硬盘状态，可能打印 `硬盘 正常`，而预警区同时显示“硬盘使用率无法解析”。
- `internal/nvgui/results.go` 的硬盘指标卡仍用 `overwriteTone()` 决定颜色，`DiskUsageUnknown > 0` 时只改了 tooltip，卡片色调仍可能是绿色/正常。

**已修复（工作区，未提交）**：

- `internal/report/report.go:226-249`：
  - 有任何硬盘使用率未知且原 verdict 为正常时，全部未知 -> `警告`，部分未知 -> `未知`。
- `internal/nvgui/results.go:196-206`：
  - 同样根据 `DiskUsageUnknown` 调整为 `警告`/`未知`，tooltip 保留“使用率未确认 N 块”。
- 新增 `internal/report/report_test.go` 用例 `TestRenderDiskUsageUnknownIsNotGreen`，确保 CLI 不再把未知显示为绿色“正常”。

### P1-2（本轮已修复）`nvr-agent` 不读取档案的 scan_options

`cmd/nvr-agent/main.go` 原先以 `model.DefaultScanOptions()` 为起点，只接受命令行显式参数；`loadDevices()` 只返回设备列表和档案名，没有读取 `profiles.json` 里该档案的 `scan_options`。

影响：

- GUI 中设置 **优先时段 9–18**、**设备并发 4**、**深度抽检/保存片段** 后，如果再用 `nvr-agent --profile ...` 做定时巡检，Agent 仍使用默认 10–18、设备并发 2、深度关闭。
- 这会让“定时巡检”和“手动 GUI 巡检”产生不一致结果，尤其容易在优先时段/音频设置上造成误判。

**已实现**：

1. `loadConfig` 同时返回档案的 `model.ScanOptions`；
2. Agent 从档案 `scan_options` 起步；
3. `flag.Visit` 记录显式参数，只有用户明确传入时才覆盖档案值；
4. 新增 `cmd/nvr-agent/profile_options_test.go`，覆盖档案值保留、flag 覆盖和 profiles.json 读取。

### P2-1（已按用户选择调整）优先时段随机取点

用户明确要求不取中点，而是取时间段中的随机点。`internal/recording/recording.go` 现在：

- 在 `[ws, we]` 内计算能容纳完整片段的随机起点范围；
- 使用 `math/rand/v2.Int64N` 随机取偏移（`RandInt64N` 可注入，测试可复现）；
- 片段长度固定为 `seconds`，不越过 `we` 或 `cutoff`；
- 日志标记从“优先时段中点”改为“优先时段随机点”。

因此同一个优先时段每次运行会得到不同的合规抽检点，不会固定在 14:00 或某个中点。

### P2-2（本轮已修复）档案中 `AVSave=true` 但 `DeepAVCheck=false` 时 UI 语义不一致

`internal/scan/scan.go:83`：

```go
wantDeep := opt.DeepAVCheck || opt.AVSave
```

即 `AVSave` 会隐式开启深度抽检。但 GUI 设置弹窗按 `DeepAVCheck` 决定深度区块是否启用；如果从旧档案/手工编辑读到 `AVSave=true, DeepAVCheck=false`：

- 深度总开关显示关闭；
- 保存片段复选框被禁用但仍可能为选中；
- 实际巡检仍会做深度抽检。

**已实现**：`internal/nvgui/app.go` 的 `syncFormFromOptions` 现在会把 `AVSave=true` 规范化为 `DeepAVCheck=true`，并新增 `internal/nvgui/options_invariant_test.go` 回归测试。

### P3 测试/文档小项

- `TestPickBusyClipTimesFollowsPreferredHours` 只断言四个窗口“不同且不等于 14:00”，没有断言期望值（13:05/13:35/12:00/05:00），也没有覆盖默认 10–18 在 18:00 后的 14:00。建议补精确语义断言。
- `log_scroll_test.go` 覆盖了“用户上翻后一帧不跟随”，但没有覆盖“上翻后新增日志仍不跟随”；建议加一条追加日志后 Y 不变的断言。
- `numberField`（自定义输入框 + 内嵌 Stepper）没有直接测试键盘输入/步进后的 `Value` 与 `Text` 同步；当前主要靠截图验证。
- `internal/nvgui/save_root_test.go` 有一条重复注释（“must be the state change it makes must be verified”），不影响逻辑。

---

## 3. 功能逻辑逐项核验

| 功能 | 验证结论 |
|---|---|
| 日志自动跟随末尾 | ✅ `log_scroll_test`：日志溢出时 `Y >= MaxY`，用户上翻后保持原位置 |
| 扫描设置布局 | ✅ `settings-small` 1000×700 截图：基础单列、深度双列、保存目录不裁切；重置默认同时重置数字与开关 |
| 保存目录选择 | ✅ `save_root_test`：Directory/CreateDirectories/DefaultPath 正确，选中路径进入 `ScanOptions.AVSaveRoot`，取消/出错不改值 |
| 音频判定 | ✅ `e30dd42` 测试：无 mean_volume -> 未知，-inf -> 警告，volumedetect 非 0 -> 未知，配置未知且无音轨 -> 未知 |
| 磁盘使用率未知 | ✅ 健康统计与警告正确；显示层一致性已由本次审查修复 |
| 优先时段 | ✅ 9–18/10–20/11–13/0–10 四个窗口产生不同合规抽检点且都在窗口内；随机点行为由 `TestPickBusyClipTimesRandomizesWithinWindow` 覆盖 |
| 工具链 Go 化 | ✅ `go test ./...`、`go vet ./...`、`cmd/nvrtool` 测试通过；`make app` 的 ffmpeg 捆绑由独立测试覆盖 |
| 扫描 worker panic | ✅ `panic_test` 覆盖 onDevice/onNVR panic，worker 继续处理其他设备 |

---

## 4. 本次审查的工作区改动

```text
internal/report/report.go       +9/-1   硬盘未知 verdict
internal/nvgui/results.go       +8/-1   硬盘卡色调
internal/report/report_test.go  +15     回归测试
cmd/nvr-agent/main.go          66     档案 scan_options + flag 覆盖
cmd/nvr-agent/profile_options_test.go  新增  Agent 回归测试
internal/nvgui/app.go          7      AVSave/DeepAVCheck 规范化
internal/nvgui/options_invariant_test.go 新增 回归测试
```

代码修复已提交 `5995688`；README 与本文档为文档提交。P1-2、P2-2 与 P2-1 均已处理；当前只剩代码审查建议中的测试覆盖小项。
