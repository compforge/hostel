# Hostel 可观测性

## 理念与概念

hostel 需要从三个层面回答同一组问题：

1. bed 当前能否接收请求、何时可以安全回收；
2. 最近一次激活、持久化或回收是否成功；
3. 一次 execution 是否正常退出；若被终止，内核观察和发起原因分别是什么；
4. 慢或失败具体发生在哪个等待边界。

三层各有职责，不应各自发明状态和阶段：

- **日志**还原单次生命周期动作，适合排查具体 bed；
- **接口**提供当前事实和有界的最近摘要，适合控制面诊断；
- **Trace**串联入口请求、bed 生命周期和 execution，适合定位单次跨服务调用；
- **metric**聚合整体成功率和耗时分布，适合趋势与 SLO。

### 当前状态与过程记录分离

`Status` 是当前状态的唯一事实源：

- `state=active|idle|evicting|dormant` 表示互斥操作态；
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

- **activate**：为一个非 resident bed 选择 fresh / luggage / snapshot 来源并使其可服务；
- **persist**：串行生成新 generation、写入 store、提交持久化水位；
- **evict**：尝试持久化并移除 resident runtime，可能因并发活动而 canceled。

resident bed 只保留最近一次 activation 和 persist。它们是有界诊断摘要，不是历史库；
evict 完成后 bed 已离开内存，因此 evict 只写日志。长期历史由日志与 Trace 承担。

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

- activate：`stat_snapshot → select_source → [restore] → prepare_workspace → commit_resident`
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
Gin 路由模板命名；`/healthz`、`/ping`、`/metrics`、`/metrics/watch` 不创建 span，避免探针和
高频采样淹没有效请求。

领域 span 保持小而稳定：

- `hostel.execution`：覆盖前台、后台和 session execution 的完整进程生命周期；
- `hostel.bed.activate` / `hostel.bed.persist` / `hostel.bed.evict`：覆盖一次 bed 管理动作；
- lifecycle stage 记录为 `stage.start` / `stage.end` event，不为每个函数制造 child span。

后台 execution 继承发起请求的 trace identity，但不继承请求取消信号，因此 HTTP 响应返回后仍能
完整记录命令终态。span 只包含 execution id、bed id、mode、executor id/backend、process outcome、termination
cause、exit code/signal、action/stage/result/source/trigger 与耗时；禁止写入 command、env、stdout、
stderr、路径和错误原文以外的用户数据。

非零退出、非预期 signal 和 executor lost 标记为 error；client cancel、interrupt、bed teardown、
daemon shutdown 属于预期控制动作，不把 trace 标红。启用 Trace 但未配置 endpoint 时保持 no-op；
两种 endpoint 同时存在时优先 gRPC，与 sandctl 的部署语义一致。

bed-init 的 transport 失败以 `executor.transport.failure` event 和 warning 日志记录 operation、
attempt、executor/process identity 与错误原文；重连成功再记录 `executor.transport.recovered`。因此瞬态
EOF 即使被内部重试吸收也可观测。对外 execution 结果仍只暴露稳定的 `executor_lost`，不泄漏 Unix
socket 实现细节；`GetFileMetadata` 等业务操作名由调用方 span 负责。

## 接口

`GET /v1/beds` 是调度 hint：实例容量、state 数量、每个本机 bed（resident + dormant
luggage）的当前三维事实，不承载 timeline。

`GET /v1/beds/:id` 是单 bed 诊断入口，在基本视图之外返回：

- 当前 `generation` 和 `activity`（operations / sessions 按 kind 计数）；
- 当前 `executor`（id / backend / state；尚未创建时省略）；
- `lifecycle.last_activation`；
- `lifecycle.last_persist`。

每条 record 包含 action 结果、来源或触发原因、起止时间、总耗时、阶段耗时和失败阶段。
接口只返回固定数量的最近摘要，不返回原始日志或无限增长的历史。

实例 health / capabilities 只表达 hostel 实例是否可服务及支持什么能力，不能混入某个
bed 的一次失败。

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
