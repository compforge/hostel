# Hostel 可观测性

## 理念与概念

hostel 需要从三个层面回答同一组问题：

1. bed 当前能否接收请求、何时可以安全回收；
2. 最近一次初始化、持久化或回收是否成功；
3. 一次 execution 是否正常退出；若被终止，内核观察和发起原因分别是什么；
4. 慢或失败具体发生在哪个等待边界。

三层各有职责，不应各自发明状态和阶段：

- **日志**还原单次生命周期动作，适合排查具体 bed；
- **接口**提供当前事实和有界的最近摘要，适合控制面诊断；
- **Trace**串联入口请求、bed 生命周期和 execution，适合定位单次跨服务调用；
- **metric**聚合整体成功率和耗时分布，适合趋势与 SLO。

### 当前状态与过程记录分离

`Status` 是当前状态的唯一事实源：

- `phase=initializing|resident|evicting|purging|dormant|failed` 表示 Bed 生命周期位置；
- `readiness.status/reason/message/updated_at` 表示是否可接收数据面请求及当前等待或失败边界；
- `activity=active|idle` 表示 resident / 仍在持久化复核的 evicting Bed 当前有无 operation，由 inflight 派生；
- `evicting` + `readiness.reason=CleanupPending` 表示已经停止准入但仍持有待清理资源，只占 occupied 名额，不占 resident，也不报告 activity；
- `generation` 表示本地数据版本；
- `retained_until` 表示最早安全回收期限；
- `inflight` 表示仍在执行的 bed 请求数。
- `executor` 表示 resident bed 当前的进程域 identity、backend 与 state；没有执行需求时可为空。

`BeginOperation` 是请求准入与并发正确性边界，不是观测 timeline。可观测性另用
`LifecycleRecord` 描述已经完成的生命周期动作，避免“operation”同时表示业务请求和
管理动作。

`Execution` 是一次命令运行的稳定身份，前台、后台和 session run 只改变等待方式，不改变
结果语义。终态由两份正交事实组成：

- `ProcessOutcome` 是 Executor 从内核 wait status 得到的 exited / signaled / lost；
- `TerminationCause` 是 execution controller 在发信号前记录的 timeout / client canceled /
  interrupted / bed teardown 等意图。没有 stop 意图的 signal 只记 external signal，不猜 OOM。

生命周期 action 包含：

- **initialize**：为一个非 resident bed Stage-in fresh / luggage / snapshot，准备 BedFS 并发布 resident；
- **persist**：串行生成新 generation、写入 store、提交持久化水位；
- **evict**：尝试持久化并移除 resident runtime，可能因并发活动而 canceled。

resident bed 只保留最近一次 initialization 和 persist。它们是有界诊断摘要，不是历史库；
evict 完成后 bed 已离开内存，因此 evict 只写日志。长期历史由日志与 Trace 承担。

### 生命周期接口投影

生命周期模型和准入、回收契约由 [kernel.md](kernel.md) 统一定义，HTTP 只投影事实。

| 接口 | 回答的问题 |
|------|-----------|
| `POST /v1/beds` | 接受 Bed 初始化；新任务返回 `202` 与 initializing readiness，已 Ready 返回 `200` |
| `GET /v1/beds` | hostel 什么状态（`instance.status`）+ 全部 bed 概要（含 initializing / failed / dormant） |
| `GET /v1/beds/:id` | 这个 bed 为什么是这个状态：`phase/readiness`；resident 时再给 activity / lifecycle / executor |
| `GET /healthz` | 实例可服务性（探活/调度用） |

bed 明细只进 `/v1/beds/:id`；`/v1/beds` 的 bed 条目保持概要。上游读到的任何字段都是 stale-tolerant hint——正确性由准入/回收点的原子复核兜底，不靠上报实时性。

## 主流程

```text
bed core
  ├─ Status：当前状态、版本、期限、活动请求数
  ├─ LifecycleRecord：action / result / source / trigger / stages
       ├─ structured logs
       └─ GET /v1/beds/:id lifecycle detail
  └─ Executor：当前进程域 identity / backend / state
       └─ Execution：identity / output / process outcome / termination cause
       ├─ SSE execution_start → stdout|stderr → execution_end
       ├─ status + cursor output API
       └─ structured logs + Trace
```

阶段按真实等待边界划分，而不是按函数数量划分：

- initialize：`stage_in_bedfs → prepare_bedfs → prepare_resident`，网络启用时另有 `prepare_network`；Stage-in 的内部等待边界另投影为 readiness reason（inspect/select/restore）
- persist：`wait_persist_lock → prepare_snapshot → persist_store → commit_watermark`

阶段集合是诊断契约。新增阶段应表示新的可行动等待边界，不能仅为某次故障增加同义概念。

## 日志

每个可能阻塞的 stage 在执行前写 `event=start`，结束后写 `event=finish`；action 结束时再写
一条 summary。这样即使进程卡在 store、restore 或锁等待中，没有 finish 日志，也能从最后
一条 start 判断停在哪里。

日志字段包含完整 `bed`、`action`、`stage`、`result`、`duration_ms`、`source`、
`trigger`、`failed_stage` 和错误原文。bed id 和错误原文是高基数诊断数据，只进入日志
或单 bed 接口，不进入 metric label。

日志不是稳定 API。控制面不得通过解析日志判断 bed 是否 active 或是否已持久化。

每个 execution 至少写 started 与 finished 两条结构化日志。字段包含 execution id、bed、mode、
executor id、executor backend、process outcome、exit code 或 signal、termination cause 与 duration；
不得记录 command、env 或原始输出。实际 backend 同时进入 health / capabilities，调用方不从配置推断运行态。

带有效 span context 的生命周期与 execution 日志增加 `trace_id` / `span_id`，用于从日志跳转到
Trace；启动日志和没有上下文的后台日志保持原格式。

## Trace

Hostel 接收 W3C Trace Context 与 Baggage，并通过 OTLP gRPC 或 HTTP 导出。入站 HTTP span 使用
Gin 路由模板命名；`/healthz`、`/ping`、`/metrics`、`/metrics/watch`、`/v1/status`
不创建 span，避免探针和高频采样淹没有效请求。

领域 span 保持小而稳定：

- `hostel.execution`：覆盖前台、后台和 session execution 的完整进程生命周期；
- `hostel.bed.initialize` / `hostel.bed.persist` / `hostel.bed.evict`：覆盖一次 bed 管理动作；
- lifecycle stage 记录为 `stage.start` / `stage.end` event，不为每个函数制造 child span。

后台 initialization / execution 继承发起请求的 trace identity，但不继承请求取消信号，因此 HTTP 响应返回后仍能
完整记录命令终态。span 只包含 execution id、bed id、mode、executor id/backend、process outcome、termination
cause、exit code/signal、action/stage/result/source/trigger 与耗时；禁止写入 command、env、stdout、
stderr、路径和错误原文以外的用户数据。

非零退出、非预期 signal 和 executor lost 标记为 error；client cancel、interrupt、bed teardown、
daemon shutdown 属于预期控制动作，不把 trace 标红。启用 Trace 但未配置 endpoint 时保持 no-op；
两种 endpoint 同时存在时优先 gRPC，与 sandctl 的部署语义一致。

supervisor backend 的 transport 失败以 `executor.transport.failure` event 和 warning 日志记录 operation、
attempt、executor/process identity 与错误原文；重连成功再记录 `executor.transport.recovered`。因此瞬态
EOF 即使被内部重试吸收也可观测。对外 execution 结果仍只暴露稳定的 `executor_lost`，不泄漏 Unix
socket 实现细节；`GetFileMetadata` 等业务操作名由调用方 span 负责。

## 接口

`GET /v1/beds` 是调度 hint：实例容量、分别聚合的 phase/activity 数量、每个本机 bed（initializing /
purging / failed / resident / dormant luggage）的当前事实，不承载 timeline。

`GET /v1/beds/:id` 是单 bed 诊断入口。初始化期间先返回 phase/readiness；resident 后在基本视图之外返回：

- 当前 `generation` 和 `activity`（operations / sessions 按 kind 计数）；
- 当前 `executor`（id / backend / state；尚未创建时省略）；
- `lifecycle.last_initialization`；
- `lifecycle.last_persist`。

每条 record 包含 action 结果、来源或触发原因、起止时间、总耗时、阶段耗时和失败阶段。
接口只返回固定数量的最近摘要，不返回原始日志或无限增长的历史。

实例 health / capabilities 表达 Hostel 是否可服务、实际选定的机制和当前可用能力；
不能把某个 Bed 的一次失败泛化成全实例能力缺席。文件档位、workspace 进程视图、网络
作用域、资源记账、容量准入和设施状态分别披露。`isolator_ok`、Bed Ready 或 amenity
running 都不能推导出完整隔离，语义由 [isolation.md](isolation.md) 统一定义。

实例诊断沿 domain owner 汇总，而不是在 HTTP 层重新解释组件状态：

```text
domain component → Status() 返回自己定义的 Status
Bed Manager      → 聚合领域 Status，并推导 Bed inventory / 实例容量状态
HTTP             → 序列化响应
```

Status 同时是组件内部事实到运维协议的边界。新增或修改诊断项时，由拥有该事实的组件定义语义和
快照方式；聚合层只决定顶层结构与 schema 版本，web 层不读取组件内部状态。
`Component[S]` 将这一读取契约与 daemon、Bed 两层 Lifecycle 放在同一个组件协议下；报告保持领域类型，
不通过通用 map 或类型断言组装。Status 只观察，不执行生命周期 hook。

Bed 的分域 Status 是单 Bed 的实际准备结果；组件 Status 是领域的实例级报告，两者粒度不同。
Filesystem 自己提供文件隔离报告，Resource 自己提供 accounting/admission，Bed Manager 只聚合；
HTTP 不重做探测、不读取领域内部句柄。

`GET /v1/status` 是版本化的运维诊断快照，当前 `schema_version` 为 `1`。顶层按所有者分为
`environment`、`isolation`、`privilege`、`network`、`executor`、`store`、`resource` 与 `amenities`，HTTP 层只负责序列化，
不跨组件推导状态。`isolation.system` 和 `isolation.probes` 保存启动时缓存的系统事实与机制原始探测，
包括 runtime、进程 capability/seccomp、LSM label、namespace sysctl、kernel feature、ptrace Yama scope，
以及 PRoot 启动序列探测。二进制探测保留配置名、解析路径、可执行性、退出码、stdout、stderr、错误和耗时。
`privilege` 给出 daemon 身份、Bed user 策略、setpriv 解析结果，以及 Bed 降权和清理所需与缺失的 capability；
`preconditions_satisfied` 只表示这些静态前置条件满足，字段的判断边界见
[privilege.md](privilege.md)。`environment.probe_status` 单独记录通过真实 Bed
命令和 shell 验证完整组合的 `not_run|running|passed|failed` 状态、时间和错误。`store.transfers_configured`
只表示 S3 transfer 配置存在，不推导远端连通或 restic 可执行。诊断接口不披露 bucket、endpoint 或凭据；
读取接口不重新探测主机，也不访问远端存储。
`instance` 与 `beds` 由 Bed Manager 的 `InventoryStatus()` 一次聚合，`/v1/beds` 使用同一入口。
phase/activity、occupied/resident/pinned 计数与本次返回的行一致，不再分别读取原子计数器。
`retained/draining/releasable` 由 Bed Manager 推导；HTTP 只序列化。冷目录大小在锁外采样，
再按锁内的准入与清理身份过滤，属于可滞后的调度提示；活跃操作及组件状态不构成全局事务快照。
`/healthz` 保留轻量健康与能力视图，不增加目录扫描。

Bed Manager 的 `local_cleanups` 报告已认领本地目录的清理状态和最近失败，供区分运行中、等待重试与完成。
Privilege 的 `reserved_users` 表示 per-Bed UID 池中保留的租约数，包括冷数据与待清理身份；fixed 策略不占池。
不存在的内核节点以 `value: null` 和 `read_error` 表达，与节点存在且值为 `0` 严格区分。

所有 execution 进入同一个有界 registry。status 返回结构化终态，logs 返回带 stream 与单调
sequence 的有界输出；游标落入已淘汰区间时显式返回 truncated。registry 只保留最近完成记录，
不能成为无限增长的历史库。

## Metric 边界

`/metrics` 与 `/metrics/watch` 是 OpenSandbox 兼容的目标 bed 资源 JSON，不是 Prometheus
入口。Hostel 的 OpenTelemetry 接入只导出 Trace，不导出 Metrics 或 Logs。

## 并发与正确性边界

观测只能记录事实，不能改变 bed 生命周期的并发不变式：

- 所有 Bed 级请求仍通过 `BeginOperation` 准入；
- persist 仍由 `persistMu` 串行，generation 在打包前写入 meta；
- evict 仍在 persist 后原子复核 `activitySeq` / `inflight`；
- persist 失败必须中止 evict，不能销毁唯一副本；
- timeline 有固定阶段和固定保留数量，读取返回副本。
- stop cause 必须先于 kill 原子记录，多个 stop 请求只接受第一个；
- `execution_start` 恰好对应一个携带 result 的 `execution_end`，输出 pipe 泄漏不能无限阻塞终态发布。
