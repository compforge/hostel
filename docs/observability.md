# Bed 可观测性

## 理念与概念

hostel 需要从三个层面回答同一组问题：

1. bed 当前能否接收请求、何时可以安全回收；
2. 最近一次激活、持久化或回收是否成功；
3. 慢或失败具体发生在哪个等待边界。

三层各有职责，不应各自发明状态和阶段：

- **日志**还原单次生命周期动作，适合排查具体 bed；
- **接口**提供当前事实和有界的最近摘要，适合控制面诊断；
- **metric**聚合整体成功率和耗时分布，适合趋势与 SLO。

### 当前状态与过程记录分离

`Status` 是当前状态的唯一事实源：

- `state=active|idle|evicting|dormant` 表示互斥操作态；
- `generation` 表示本地数据版本；
- `retained_until` 表示最早安全回收期限；
- `inflight` 表示仍在执行的 bed 请求数。

`BeginOperation` 是请求准入与并发正确性边界，不是观测 timeline。可观测性另用
`LifecycleRecord` 描述已经完成的生命周期动作，避免“operation”同时表示业务请求和
管理动作。

第一阶段统一三个 action：

- **activate**：为一个非 resident bed 选择 fresh / luggage / snapshot 来源并使其可服务；
- **persist**：串行生成新 generation、写入 store、提交持久化水位；
- **evict**：尝试持久化并移除 resident runtime，可能因并发活动而 canceled。

resident bed 只保留最近一次 activation 和 persist。它们是有界诊断摘要，不是历史库；
evict 完成后 bed 已离开内存，因此 evict 只写日志。长期历史由日志和未来的时序指标承担。

## 主流程

```text
bed core
  ├─ Status：当前状态、版本、期限、活动请求数
  └─ LifecycleRecord：action / result / source / trigger / stages
       ├─ structured logs
       ├─ GET /v1/beds/:id lifecycle detail
       └─ aggregate metrics（第二阶段）
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

## 接口

`GET /v1/beds` 是调度 hint：实例容量、state 数量、每个本机 bed（resident + dormant
luggage）的当前三维事实，不承载 timeline。

`GET /v1/beds/:id` 是单 bed 诊断入口，在基本视图之外返回：

- 当前 `generation` 和 `activity`（operations / sessions 按 kind 计数）；
- `lifecycle.last_activation`；
- `lifecycle.last_persist`。

每条 record 包含 action 结果、来源或触发原因、起止时间、总耗时、阶段耗时和失败阶段。
接口只返回固定数量的最近摘要，不返回原始日志或无限增长的历史。

实例 health / capabilities 只表达 hostel 实例是否可服务及支持什么能力，不能混入某个
bed 的一次失败。

## Metric（第二阶段）

hostel 已有 OpenSandbox 兼容的 `/metrics` 与 `/metrics/watch`，它们返回目标 bed 的资源
使用 JSON。Prometheus 生命周期指标必须使用独立入口，不能改变现有协议。

metric 直接聚合同一份 lifecycle 事实，覆盖：

- action 结果计数与总耗时；
- stage 结果计数与耗时；
- 当前各 state 的 bed 数量；
- activate 来源和 persist 触发原因。

label 只允许 action、stage、result、source、trigger 等固定枚举。bed id、路径、
generation、错误原文不得成为 label。错误需要聚合时使用有限错误类别，未知错误落入通用
类别，不能把底层错误字符串直接标签化。

metric 失败不得影响 bed 生命周期；上报不能扩大核心锁持有时间。具体名称和 bucket 在
第二阶段结合 sandctl 压测查询确定，避免先固定一套没有消费方验证的指标。

## 并发与正确性边界

观测只能记录事实，不能改变 bed 生命周期的并发不变式：

- 所有 Bed 级请求仍通过 `BeginOperation` 准入；
- persist 仍由 `persistMu` 串行，generation 在打包前写入 meta；
- evict 仍在 persist 后原子复核 `activitySeq` / `inflight`；
- persist 失败必须中止 evict，不能销毁唯一副本；
- timeline 有固定阶段和固定保留数量，读取返回副本。

## 分阶段落地

1. **第一阶段**：activate / persist / evict 结构化日志，单 bed 最近 lifecycle 摘要，
   以及详情接口；复用既有状态与并发模型。
2. **第二阶段**：把同一事实投影为低基数 Prometheus metric，用 sandctl perf 验证
   激活、持久化、回收耗时和错误率。
3. **后续**：只有出现稳定、跨场景的诊断需求时，才把同一模式扩展到 amenity / instance；
   不按单个故障不断增加孤立概念。
