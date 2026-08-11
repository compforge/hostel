# 生命周期（request / bed / hostel）

## 理念与概念

hostel 里有三个粒度的生命周期，层层驱动、层层推导：

1. **request**：一次调用。用户视角只有两类——**operation**（无状态）和 **session**（有状态）。
2. **bed**：生命周期控制器持有 `phase/readiness`；resident bed 的 `activity` 由它持有的 operation **推导**得出。
3. **hostel**：status 由它持有的 bed 的 status **推导**得出，不存储。hostel 不能自己杀自己，只通过接口如实暴露 status，由上游调度（sandctl）决定何时释放。

贯穿三层的三条不变式：

- **终结权永远在上一层**：request 被调用方取消（ctx/timeout）、bed 被 manager 回收、hostel 被上游释放。
- **生命周期与操作态分开**：`phase/readiness` 表达 Bed 是否已经初始化并可服务；`activity` 只表达 resident / evicting Bed 当前有无 operation。控制面不会把 restoring 的目录误报成 idle Bed。
- **status 的变迁就是 lifecycle**：不另立 lifecycle 概念；bed 每次 status 变化（含触发原因）写一条结构化日志并保留最近摘要，hostel 的变迁由 bed 的变迁天然有据。

### request 的两类与 touch 副作用

| 类别 | 语义 | 生命周期边界 | evict 行为 | 例子 |
|------|------|--------------|------------|------|
| **operation** | 无状态请求 | 系统按 timeout 自动收口（**timeout 强制有界**：缺省用默认值，超限截断到上限） | inflight > 0 时拒绝，有界等待后重试必然成功 | exec、file、browser verbs、checkpoint |
| **session** | 有状态持有 | 客户端手动开、手动关（可能永远不关） | **不能等**——evict 主动 revoke（cancel + 等 handler 退出） | shell、cdp |

**touch 不是请求类别，是副作用**：operation 开闭、session 打开、session 上的真实流量，都会刷新 `last_active_at` / `retained_until`。观测类请求（`GET /v1/beds`、`/v1/beds/:id`、`/healthz`）不产生 touch——控制面看多少眼都不会让 bed "显得活着"，活跃度只能由真实使用产生。

类比 MySQL：operation ≈ autocommit statement（系统收口 + 执行超时），session ≈ 显式开启的连接（手动开闭 + 空闲超时，服务器可 KILL）。

### 三层 status

```text
operation ──持有关系──▶ bed.status.activity   ──聚合──▶ hostel.status
                         active  inflight>0       retained    有 Bed 在保留期或初始化中
                         idle    inflight==0      draining    全部过期、回收进行中
                                                releasable  无 resident、快照在远端
                                                pinned      store=noop（本地是唯一副本）

bed.status.phase：initializing → resident → evicting → dormant；初始化失败为 failed
bed.status.readiness：status + reason + message + updated_at
```

关键语义：**session 不抬升 bed.status.activity**——只有一条 CDP 连接挂着且没有 operation 的 bed 仍是 idle，可以过期回收。这正是"长连接不应阻塞回收"在模型层的表达。

`pinned` 也不是 lifecycle phase 或 activity，而是容量准入使用的复合事实：有 operation，或 durable store 下
`data_synced=false`，都表示 bed 暂时只能由当前 carrier 承接。noop 表示用户不要求远端数据完整性，
因此只在 operation 进行期间 pinned。`GET /v1/beds` 直接上报这一推导结果。

## 流程

### 1. Request

```text
operation：HTTP 请求 → web 准入（withOp 统一 BeginOperation(kind, timeout)，
           生命周期跨单个请求的才显式 begin）→ 执行 → finish
session：  客户端打开（POST /session、CDP ws 升级）→ 持有（流量即 touch）
           → 客户端关闭，或 evict revoke
```

准入即承诺：新 resident / dormant restore，以及未 pinned 的 idle bed 再次进入 active 时，
hostel 才检查 carrier 数量与资源 pressure。pinned bed 仍由当前 carrier
承担，不因 pressure 拒绝。被准入的 operation 会把 `retained_until` 预留到
`timeout + idleTTL`；未同步状态由 `last_active_at > persisted_at` 推导，noop store 按无待同步步骤处理。
hostel 不自行选择新 carrier，跨 carrier 溢出由上层调度负责。已接纳的工作不会被 idle reaper 杀死；timeout 有默认值和硬上限，任何 operation 的阻塞时间
有上界——evict 的"拒绝-重试"必然最终成功，死锁在模型上被消除。default bed 不参与数量准入。

### 2. Bed

```text
（不存在 / dormant）─InitializeBed→ initializing ─Ready→ resident（active ⇄ idle）
                                  └失败→ failed（保留原因；下一次 InitializeBed 可重试）
   resident ─过 retained_until 且 inflight==0→ evicting
   evicting ─revoke session → persist → 原子复核→ dormant（luggage 留在本机）
   dormant  ─同机 resume 且 generation 新鲜→ initializing → resident
   dormant  ─磁盘水位 GC→ 删除（快照仍在 store）
   任意态   ─Purge（显式销毁）→ 身份终结（删目录 + 删快照）
```

三条驱动线：

- **初始化线**：`POST /v1/beds` 只接受 desired state，返回 `202`，后台执行 `StageInBedFS → prepare BedFS/isolation → publish resident`。Store Stat/Restore 不占用创建请求；同 id 并发初始化合并为一个任务，且在外部 I/O 前预占 `max-beds` 名额。`GET /v1/beds/:id` 通过 phase/readiness 观察进度；原生数据面首次请求走 `Ensure`，加入同一任务并等待 Ready，因此不会操作半成品目录。

- **活跃度线**：request 的 touch 刷新 `last_active_at` 与 `retained_until`；`CollectExpired` 定时扫描过期 bed 触发 evict。evict 先 revoke 全部 session（cancel + 有界等待，shell 的 Close 也在这一阶段），再 persist，最后原子复核 `activitySeq`/`inflight`——persist 窗口内来了新活动则取消本次回收（服务优先于回收）。
- **数据同步线**：`generation` 是数据版本，`persistedAt` 是同步水位，`last_active_at > persistedAt` 即 dirty。initialization/operation/session/pressure 只向 Store 同步循环提交 trigger；循环负责合并、串行、周期与失败退避。`Checkpoint` 和 `evict` 是必须等待结果的边界。语义详见 `docs/store.md`。

### 3. Hostel

```text
启动组装（isolation → amenity → store → bed manager → web）→ 服务
   ├─ 后台循环：idle bed reaper / luggage GC / Store 同步调度
   └─ 事实上报：/healthz（可服务性）、GET /v1/beds（status + bed 概要）
→ SIGTERM 优雅关停
```

hostel 不自杀，也没有 drain 接口。它表达"可以释放我"的唯一方式是 `GET /v1/beds` 里的 `instance.status`（retained / draining / releasable / pinned）——判据收敛在 hostel 内，上游只读结论，不再自己拼 bed_counts / store / luggage。

容量准入与这里的生命周期状态正交：`instance.status` 回答“能否安全释放这个 Hostel”，未来的
`admission.accepting_new_beds` 回答“资源余量是否还能承接新的未 pinned bed”。短期数量安全阀与长期
pod/cgroup 资源水位方案见 `resource.md`〈当前准入策略〉。

## 接口边界

| 接口 | 回答的问题 |
|------|-----------|
| `POST /v1/beds` | 接受 Bed 初始化；新任务返回 `202` 与 initializing readiness，已 Ready 返回 `200` |
| `GET /v1/beds` | hostel 什么状态（`instance.status`）+ 全部 bed 概要（含 initializing / failed / dormant） |
| `GET /v1/beds/:id` | 这个 bed 为什么是这个状态：`phase/readiness`；resident 时再给 activity / lifecycle / executor |
| `GET /healthz` | 实例可服务性（探活/调度用） |

bed 明细只进 `/v1/beds/:id`；`/v1/beds` 的 bed 条目保持概要。上游读到的任何字段都是 stale-tolerant hint——正确性由准入/回收点的原子复核兜底，不靠上报实时性。

## 关键设计

### 为什么 request 只分两类

区分的本质不是"状态有无"，而是**生命周期由谁收口**：系统收口（timeout 有界）的，evict 等得起；客户端收口（可能永远不关）的，evict 等不起、必须能主动断。CDP 长连接的教训：它被错建模为 `timeout=0` 的 operation（无界），evict 永远等不到结束，回收链路死锁、carrier 无限堆积。归位 session 类 + operation timeout 强制有界后，两个方向都被堵死。

### 为什么 session 和 shell 是一类

shell（`/session`）与 CDP 连接语义逐项相同：客户端显式开闭、持有状态（cwd/env、browser context）、不占 operation、evict 时主动终止。shell 是 session 类的第一个实例（kind=shell），CDP 归位后（kind=cdp）概念统一；shell 的 Close 也因此从 teardown 挪到 revoke 阶段，顺带消除 persist 与会话写入并发的一致性问题。

### 为什么 bed 的回收要"期限 → revoke → 复核 → 销毁"

- `retained_until` 是对已接纳工作的承诺，准入时预留、完成后续期，回收只看承诺是否到期；
- session 必须先断再 persist——否则 persist 与会话驱动的写入并发，快照不一致；
- persist 窗口可能很长，窗口内新活动必须能取消回收（原子复核 `activitySeq`），否则静默丢写；
- persist 失败必须中止 evict——本地目录可能是唯一副本，销毁它等于丢数据。

### 为什么初始化是异步 desired state

Store Stat/Restore 的延迟和可用性属于数据准备，不属于 HTTP handler 或 Hostel 进程启动。管理接口先记录“希望这个 Bed resident”，初始化控制器再把它推进到 Ready：调用方可区分“请求已接受”和“Bed 已可服务”，S3 冷启动不会占住创建连接，也不会让半恢复目录进入 resident map。readiness reason 保留当前等待边界，失败 message 保留原始错误链，控制面无需从裸 EOF 或 500 文本猜原因。

### 为什么终结权必须在上一层

数据面组件自行了断会破坏调度语义：bed 自杀会让 manager 的 placement 出现幽灵，hostel 自杀会让 sandctl 失去对 carrier 的控制，且 noop store 下自杀就是数据丢失。各层只持有"自己能否被安全终结"的事实并推导成 status 暴露，终结动作留给上一层。
