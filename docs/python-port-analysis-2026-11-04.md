# Python → Go 移植度分析：哪些是照搬，哪些该用 Go 重做

分析对象：`cam-gui`（Python/PySide6 v2.3.0）↔ `nvr-status-go`（Go 1.27 + MyGo）
日期：2026-11-04

## 0. 结论摘要

移植的整体取向是「**业务口径严格对齐，语言机制尽量 Go 化**」，而且守住了这条线：
生产代码里 **零 `interface{}`**、零手写 XML 字符串拼接、零 `sort.Slice` 之外的 Python 式排序。
`model.Record` 是强类型 struct，不是 Python 的 dict。

但仍有 **8 处**把 Python 的实现手段而非业务语义一并搬了过来。其中最值得改的是**取消机制**：

> Python 必须用 `subprocess` 轮询，因为 `proc.wait()` 不可打断；
> Go 的 `exec.CommandContext` 天生响应 ctx。
> 但现在的 Go 版仍然为每个 ffmpeg 进程开一个 50ms ticker goroutine 去「模拟轮询」。

这不是个别现象 —— `CheckCancel func() error` 这个"仿 Python Event"的字段已经渗进 5 个包、20+ 个调用点。

## 1. 判定方法

认定"照搬"的四条证据（满足两条以上才计入）：

1. **函数名逐字对应**：`_pick_busy_clip_times` → `PickBusyClipTimes`
2. **注释自述**：`mirrors the parts of Python's ElementTree`、`Go port of services/export_report.py`
3. **参数顺序/控制流同构**：连"先查 A 再查 B、失败退回 C"的分支顺序都一样
4. **保留源语言惯用法**：`any` 参数接 5 种类型、轮询代替事件、自定义 `max()`

## 2. 结构性直译清单

### 2.1 业务语义层 —— 照搬是对的，不要动

这些是**判定口径**，必须与 v2.3.0 逐字一致，否则 M8 的 A/B 对照失去意义。

| 模块 | Python | Go | 函数对应 |
| --- | --- | --- | --- |
| 健康判定 | `health.py` 376 行 | `health/health.go` 464 行 | `classify_detect_state` / `detect_reason` 保留；巨型 `get_health_summary` **拆成 `Summarize` + 辅助函数**（改进） |
| 录像检索 | `recording.py` 776 行 | `recording/recording.go` 746 行 | 11 个函数 1:1：`_cmsearch_page`→`cmsearchPage`、`_busy_hours_window`→`BusyHoursWindow`、`_judge_recording_ok`→`judgeRecordingOK` … |
| 抽检核心 | `av_probe.py` 1252 行 | `avprobe/avprobe.go` 1781 行 | 27 个函数 1:1：`_compute_clip_window`、`_rewrite_rtsp_times`、`_build_short_rtsp_candidates`、`_audio_verdict` … |
| 巡检编排 | `scan_runner.py` 444 行 | `scan/scan.go` 451 行 | `build_nvr` → `Build`，`run` → `Run` |

行数增加（1252→1781）主要是 Go 的显式错误分支、类型定义与包注释，不是逻辑膨胀。

### 2.2 语言机制层 —— 照搬了 Python 的手段，Go 有更好的写法

> 下表 1–7 项已全部修复（见 §5 的提交号），第 8 项保留未做。

| # | 位置 | Python 的做法 | Go 现在的做法 | 问题 |
| --- | --- | --- | --- | --- |
| 1 | `avprobe.go:712` `runCommand` | `Popen` + `while True: communicate(timeout=0.25)` | `context.Background()` + 50ms ticker 轮询 + `time.AfterFunc` | 见 §3.1 |
| 2 | `xmlnode/xmlnode.go` 全文 | `ElementTree.find(".//x")` | 自己实现 `Find/FindAll/FindText` 路径解析 | 见 §3.2 |
| 3 | `util.go:106,153` | `parse_channel_list(None/str/list)` 鸭子类型 | `ParseChannelList(value any)` + 5 分支 type switch | 见 §3.3 |
| 4 | `health.go:135` / `monitor.go:171` | 同一个 `_to_float` | 两份 `parsePercent`，语义还不一致 | 见 §3.4 |
| 5 | `recording.go:312` `avprobe.go:211,218` | 内置 `max()/min()` | 自定义 `max`/`minInt`/`maxInt` | Go 1.21+ 已内置 |
| 6 | `avprobe.go:354` `recording.go:310` | mixin 方法 | 纯转发的导出壳 | 死代码 |
| 7 | `recording.go:493` `cmsearchCollect` | `raise` 优先 | 返回 `(error, []*Node)`，error 在第一位 | 反 Go 惯例 |
| 8 | `scan.go:314` `queueConcurrent` | `ThreadPoolExecutor` | 手写 channel + WaitGroup worker pool | 可接受，但有 panic 死锁隐患 |

## 3. 逐项分析与改法

### 3.1 【高】取消机制：轮询模拟事件 ✅ 已修复

**原状**（`avprobe.go:712-775`）：

```go
ctx, cancel := context.WithCancel(context.Background())  // ← 断开了上游 ctx
...
stopPoll := make(chan struct{})
go func() {
    ticker := time.NewTicker(50 * time.Millisecond)
    for {
        select {
        case <-stopPoll: return
        case <-ticker.C:
            if s.checkCancel() != nil { cancelRequested.Store(true); cancel(); return }
        }
    }
}()
```

**为什么这是照搬**：Python 侧 `_run_cancellable`（`av_probe.py:476`）必须这样写 ——
`subprocess.Popen` 的 `communicate(timeout=)` 超时后进程还在跑，只能靠外层循环反复 poll。
Go 的 `exec.CommandContext` 在 ctx 取消时**由运行时直接 Kill 子进程**，一行就够。

**已改成**：

```go
ctx := s.ctx()                       // 来自 Service.Ctx（调用链注入）
if timeout > 0 { ctx, _ = context.WithTimeout(ctx, timeout) }
cmd := exec.CommandContext(ctx, args[0], args[1:]...)
```

`CheckCancel func() error` 换成 `Ctx context.Context`；`recording`/`storage` 的 Fetcher
接口也从 `CheckCancel() error` 改成 `Context()`。20+ 个检查点保留了（它们本身是合理的
短路径检查），但读的都是同一个 ctx。

**实测**：SIGINT 到进程退出 6ms（原来受 50ms 轮询粒度限制），`exit=130`，无残留进程。
变异验证：把 `exec.CommandContext` 换回 `exec.Command`，取消测试 8 秒超时变红。

> ⚠️ **这次改动自己引入过一个回归**，记录在此：`Build` 里最初写成
> `Ctx: client.Context()`，而 ctx 是之后的 `onNVR` 钩子才 `SetContext` 的 ——
> 快照拿到的是 `Background`。结果是 HTTP 能取消、ffmpeg 不能。
> 原实现用 `CheckCancel: client.CheckCancel`（方法值，每次现读）所以没这个问题，
> **改成快照就坏了**。修法是 `NVR.SetContext` 单点同时注入客户端与抽检层，
> 并补 `internal/scan/context_test.go` 锁住。
> 教训：把"每次现读的回调"换成"一次性快照"时，要检查所有安装点的时序。

### 3.2 【中】xmlnode：用 Go 复刻 ElementTree

**现状**：包注释第一行就写着

> It mirrors the parts of Python's ElementTree the original code relies on

`Find(".//playbackURI")` 就是在 Go 里重新发明 ElementTree 的 XPath-lite。
每次调用都要：`strings.Split` 路径 → 遍历子树 → 字符串比较节点名。

**实测成本**（`bench_test.go`，40 条 matchItem/页）：

| 操作 | 优化前 | 优化后 |
| --- | --- | --- |
| `Parse`（每页一次） | 217 µs | ~160 µs |
| `FindAll`（每页一次） | 952 ns | ~824 ns |
| 逐条取 3 字段（每页 120 次） | 5.29 µs | 2.47 µs |
| `Find` 首个命中 | 1.0 µs | 0.15 µs |

**原判断要修正**：查询不是瓶颈，`Parse` 才是（占 98%），而它的绝对值
（160 µs × 128 页 ≈ 20 ms）对一次几十秒的巡检完全可忽略。所以 ——

- **不做 struct 映射 / 流式改造**。ISAPI 字段名在固件间有差异，改写要重跑全部
  用例加真机验证，收益抵不上风险。若将来真需要提速，方向是流式解析
  （边读边产出 matchItem，不建整棵树），而不是换查询方式。
- **已做**：`Find` 命中即停（6.8×）、`FindAll`/`trimText` 改显式栈。
  后者不是性能优化 —— 8 MiB 上限允许几十万层嵌套，递归版会吃掉几十 MB 栈；
  栈溢出是 fatal error，`recover()` 接不住，进程直接退出而不是返回解析错误。

**顺带修的一处**：

- `xmlnode.go:42` `if err.Error() == "EOF"` → 已改 `errors.Is(err, io.EOF)`。
  字符串比较遇到包装错误会漏判。（实测 `encoding/xml` 对截断文档返回 `unexpected EOF`，
  所以截断保护**一直是有效的** —— 依赖的是标准库而不是这行判断。）

**暂不处理**：`Text += string(t)` 理论上是 O(n²) 累积。ISAPI 响应里每个节点的
CharData 基本只有一个片段，实测 `Parse` 已经很便宜，改它收益为零。

### 3.3 【中】`ParseChannelList(value any)`：鸭子类型渗进核心 ✅ 已修复

**现状**（`util.go:106`）：`any` 接 `nil`/`bool`/`int`/`string`/`[]int`/`[]string` 六种形态，
只因为 Python 那边 `av_channels` 可能是 `None`、`"31,64"` 或 `[31, 64]`。

**Go 里这样做的问题**：`bool` 要单独报错、`nil` 要特判、每个调用点都得想"我传的是哪种"。
类型系统在这里完全没帮上忙 —— 而这正是换语言的主要收益之一。

**改法**：在**边界**归一化，核心只收 `[]int`。

- `config` 解码：`json.Unmarshal` 到 `[]int`，或自定义 `UnmarshalJSON` 兼容字符串
- CLI flag：`--av-channels` 已经是 string，解析一次得到 `[]int`
- GUI：表单本来就是 `[]int`

`ParseInstantList(value any, ...)` 同理（`util.go:153`）。

收益：删掉约 40 行类型分支，`bool`/`float` 误传变成编译错误而不是运行时"无法解析的通道列表"。

### 3.4 【中】两份 `parsePercent`，语义还不一致 ✅ 已修复

| 位置 | 签名 | 解析失败时 |
| --- | --- | --- |
| `health/health.go:135` | `parsePercent(s string) float64` | **静默返回 0** |
| `monitor/monitor.go:171` | `parsePercent(s string) (float64, bool)` | 返回 `0, false`，调用方输出 `nvr_disk_usage_unknown` |

同一个磁盘 `Usage` 字符串，两个包两套解析。monitor 那份是上一轮修的（避免把"解析失败"
报成 0% 空盘），health 那份还是老行为。

**当前是否误判**：`health.go:196` 用 `>= fullDriveUsagePct` 判满盘，解析失败得 0 不会误报满盘 ——
**安全方向**，但也不会报"未知"，与 monitor 的口径已经分叉。

**改法**：抽到 `util.ParsePercent(s) (float64, bool)`，两处共用，health 侧把 `!ok` 计入
「磁盘使用率未确认」统计。

### 3.5 【低】自定义 min/max 全家桶 ✅ 已修复

Go 1.21 起内置 `min`/`max`（泛型，支持 int/float/string）。

| 位置 | 可删 |
| --- | --- |
| `recording.go:312` `func max(a, b int)` | ✅ 直接删 |
| `avprobe.go:211` `minInt` / `:218` `maxInt` | ✅ 直接删（20 处调用点改内置） |
| `avprobe.go:225` `minTime` / `:232` `maxTime` | ⚠️ `time.Time` 不是 ordered 类型，内置 `max` 不支持；可改用 `a.Compare(b)` 内联，或保留 |
| `avprobe.go:239` `absDuration` | ⚠️ Go 没有内置 `abs`，保留 |

注意：同包内自定义 `max` 会 **shadow** 内置函数，不报错 —— 所以这个问题不会被编译器提醒。

### 3.6 【低】纯转发的导出壳 ✅ 已修复

```go
// avprobe.go:350-356
// fmtRTSPTime formats a time as the device local wall clock...
// swapRTSPPort is the method form of swapRTSPPort, kept for symmetry with the
// Python AVProbeMixin.
func (s *Service) swapRTSPPort(uri string, port int) (string, bool) {
    return swapRTSPPort(uri, port)
}
```

注释自己承认是"为了和 Python 的 mixin 保持对称"。而且这段注释**挂错了位置** ——
它紧跟在 `fmtRTSPTime` 的文档注释后面，godoc 会把两段拼在一起当成一个函数的说明。

`recording.go:310` 的 `JudgeRecordingOK` 是同一个模式（私有 `judgeRecordingOK` + 导出包装）。

改：删掉方法版，调用点直接用包级函数；`judgeRecordingOK` 直接改名导出。

### 3.7 【低】`cmsearchCollect` 的返回值顺序 ✅ 已修复

```go
func (s *Service) cmsearchCollect(trackID string, start, end time.Time) (error, []*xmlnode.Node)
```

error 在第一位。Go 惯例是最后一位，`if err != nil` 才读得顺。这是 Python「先抛异常」
的思维映射。改起来只是签名 + 2 个调用点。

### 3.8 【低】`queueConcurrent` 的手工 worker pool ⬜ 未做

`scan.go:314` 的 channel + WaitGroup 写法是**正确**的，但与 Python 的 `ThreadPoolExecutor`
同构，没有用上 Go 的现成工具：

- `for i := range devices` 可以写成 `for i := range total`（Go 1.22 range-over-int）
- `golang.org/x/sync/errgroup` 能省掉 `wg` + 错误收集
- 当前 jobs channel 无缓冲，**worker 若 panic 会导致主 goroutine 卡在 `jobs <- i` 死锁**
  （除非 `wg.Wait()` 在别处）。加 `defer func(){ recover() }` 或改成有缓冲 + `select ctx.Done()`

优先级低：设备数 ≤ 4，且 `runWith` 内部已有 recover 兜底。

## 4. 已经优于 Python 的地方（保持，别改回去）

| 项 | 说明 |
| --- | --- |
| **GUI 全重写** | Python 是 Qt 保留模式（1261 行 `main_window.py` 手工维护 widget 树）；Go 是 MyGo 即时模式，`// App is the window state; the view is rebuilt from it every frame`。这不是移植，是架构替换 |
| **共享 HTTP Transport** | `client.go:110` 显式配 `MaxIdleConnsPerHost: 16` + `KeepAlive: 30s`，对齐 `search_workers` 上限。Python 靠 `requests.Session` 隐式复用 |
| **`Cmd.WaitDelay = 1s`** | `avprobe.go:726`。ffmpeg 的 stderr 管道在子进程被杀后可能仍被孙进程持有，没有 WaitDelay 会永久挂住 `Wait()`。Python 侧靠 `communicate(timeout=2)` 兜底，Go 这版更干净 |
| **单飞解析缓存** | `client.go:70` `parseEntry{done chan struct{}}` —— 并发请求同一端点只解析一次。Python 侧无此机制 |
| **类型化模型** | `model.Record` / `model.HealthSummary` 替代 `Dict[str, Any]`，`GetProfile` 返回空密码这类问题在 Go 里编译期就能发现 |
| **表驱动健康判定** | `detectOnlineValues` / `detectAbnormalValues` 两个 set 替代 Python 的 if/elif 链，白名单语义更清楚 |
| **`Summarize` 拆分** | Python 的 `get_health_summary` 是单个 300 行方法；Go 拆成 `Summarize` + `parsePercent`/`driveStatusBad`/`severityRank` |

## 5. 执行顺序与落地状态

| 批次 | 内容 | 状态 |
| --- | --- | --- |
| 1 | §3.5 删 `max`/`minInt`/`maxInt`、§3.6 删转发壳、§3.7 调返回值顺序 | ✅ `92f3e70`，净删 29 行；顺带补了分页合并的测试缺口 |
| 2 | §3.4 统一 `parsePercent` 到 `util` | ✅ `88bb236`，只统一实现、不改判定 |
| 3 | §3.3 收窄 `ParseChannelList` 类型 | ✅ `26f67cc`，净删 43 行。**实际只有 2 个调用点**（都传 string），不是预想的 config/CLI/GUI 三入口 |
| 4 | §3.2 xmlnode：`Find` 命中即停、遍历改显式栈、`errors.Is` | ✅ `5b0cf39`，`Find` 6.8×；**不做** struct 映射 |
| 5 | §3.1 用 ctx 替换 `CheckCancel` 回调 + 删轮询 goroutine | ✅ `a696a95`，SIGINT 6ms 退出 |
| — | §3.8 `queueConcurrent` worker pool | ⬜ 未做（设备数 ≤4，`runWith` 内已有兜底，优先级最低） |

**过程中被推翻的两个判断**（原始分析高估了）：

1. **xmlnode 性能**：以为查询是热路径瓶颈，实测 120 次 `FindText` 只占 2.5µs，
   `Parse` 占绝对大头且总量可忽略。最终只做了 `Find` 提前返回和显式栈，
   没做 struct 映射。
2. **批次 3 的改动面**：原以为要动 config、CLI、GUI 三个入口，实际 `any` 的
   `bool`/`int`/`[]int`/`[]string` 分支全是死代码，生产调用点只有两个字符串。

## 6. 一个提醒

**不要为了"更 Go"去改判定逻辑。** §2.1 那些 1:1 的函数是业务口径，M8 的 Python/Go
逐字段对照正是靠它们逐字一致。要动的是**实现手段**（轮询→ctx、字符串路径→struct tag、
鸭子类型→强类型），不是**判定结果**（哪些 `chanDetectResult` 算在线、双「未知」怎么算）。

## 7. 有意偏离 Python oracle 的判定（2026-11-05）

§6 的原则有一个例外：**当 v2 口径本身会把故障判成"正常"时，允许在 Go 侧修正**。
下面两处是 dongsheng 明确要求「只改当前 Go 项目」的，Python v2.3.0 保持原样。
**M8 做逐字段 A/B 时，这两类用例预期会有差异，不是移植 bug。**

| 场景 | Python v2.3.0 | Go 3.0（现状） | 理由 |
|---|---|---|---|
| 拉到音轨，但 `volumedetect` 没有 `mean_volume` 行（0 可解码采样）、输出 `-inf dB`、或解码非 0 退出 | `正常` | 前两者 → `未知`「电平未测到」，`-inf` → `警告`「无有效采样」，非 0 退出 → `未知` 并带退出码 | 实测：`anullsrc` 纯静音输出 `mean_volume: -91.0 dB`（阈值 −80 能抓到）；零采样文件**根本没有 mean_volume 行**。把"没测到"当"正常"是最坏的一类漏判。原版还漏了 `runCommand` 通过 `ExitCode` 而非 error 报告失败这一点 |
| `no_stream` 且 `SaveAudio` 配置查询返回 `nil` | `异常 无音轨` → 健康判定升 `严重` | `未知`「配置未知且无音轨」 | 不是所有固件都返回 `SaveAudio`。大量 IPC 本来就没麦克风，仅凭"配置没读到"就升最高档是误报。配置明确 `true` 仍判 `异常`、`false` 仍判 `跳过` |

具体落点：`internal/avprobe/avprobe.go` 的 `audioVerdict`、`meanVolumeRe`、
`probeAudioTrack` 的 volCmd 段。健康判定侧**没有改动**——`未知` 继续走既有的
「只有实际检查到的通道全部未知才升警告」比例口径，不会新增严重度级别。

新增 4 个端到端用例 + 2 个表驱动用例，均做了变异验证（逐条还原旧实现，确认测试变红
后还原）；其中 M1 第一次变异因产生重复 `case` 而编译失败，已重做为可编译的旧行为。

**未改**：`疑似静音(警告)` 仍不参与换时段复检。实测纯静音为 −91 dB、阈值 −80，触发的
基本是接近数字静音，误报概率低；要不要让 `警告` 也复检，等真机数据再说。
