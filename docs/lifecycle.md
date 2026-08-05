# 生命周期（request / bed / hostel）

## 理念与概念

hostel 里有三个粒度的生命周期，层层驱动、层层推导：

1. **request**：一次调用。用户视角只有两类——**operation**（无状态）和 **session**（有状态）。
2. **bed**：status 由它持有的 request **推导**得出，不存储。
3. **hostel**：status 由它持有的 bed 的 status **推导**得出，不存储。hostel 不能自己杀自己，只通过接口如实暴露 status，由上游调度（sandctl）决定何时释放。

贯穿三层的三条不变式：

- **终结权永远在上一层**：request 被调用方取消（ctx/timeout）、bed 被 manager 回收、hostel 被上游释放。
- **status 是推导，不是状态机**：每层读取时由下一层的事实算出来；唯一的存储态标记是 `evicting`（并发正确性需要的过渡态）。
- **status 的变迁就是 lifecycle**：不另立 lifecycle 概念；bed 每次 status 变化（含触发原因）写一条结构化日志并保留最近摘要，hostel 的变迁由 bed 的变迁天然有据。

### request 的两类与 touch 副作用

| 类别 | 语义 | 生命周期边界 | evict 行为 | 例子 |
|------|------|--------------|------------|------|
| **operation** | 无状态请求 | 系统按 timeout 自动收口（**timeout 强制有界**：缺省用默认值，超限截断到上限） | inflight > 0 时拒绝，有界等待后重试必然成功 | exec、file、browser verbs、checkpoint |
| **session** | 有状态持有 | 客户端手动开、手动关（可能永远不关） | **不能等**——evict 主动 revoke（cancel + 等 handler 退出） | shell、cdp |

**touch 不是请求类别，是副作用**：operation 开闭、session 打开、session 上的真实流量，都会刷新 `last_active_at` / `retained_until`。观测类请求（`GET /v1/beds`、`/v1/beds/:id`、`/healthz`）不产生 touch——控制面看多少眼都不会让 bed "显得活着"，活跃度只能由真实使用产生。

类比 MySQL：operation ≈ autocommit statement（系统收口 + 执行超时），session ≈ 显式开启的连接（手动开闭 + 空闲超时，服务器可 KILL）。

### 三层 status 推导

```text
request ──持有关系──▶ bed.status              ──持有关系──▶ hostel.status
                        active     inflight>0    retained    有 bed 在 retained_until 内
                        idle       inflight==0   draining    全部过期、回收进行中
                        evicting   过渡态        releasable  无 resident、快照在远端
                        dormant    不在内存      pinned      store=noop（本地是唯一副本）
```

关键语义：**session 不抬升 bed.status**——只有一条 CDP 连接 idle 挂着的 bed 就是 idle，可以过期回收。这正是"长连接不应阻塞回收"在模型层的表达。

## 流程

### 1. Request

```text
operation：HTTP 请求 → web 准入（withOp 统一 BeginOperation(kind, timeout)，
           生命周期跨单个请求的才显式 begin）→ 执行 → finish
session：  客户端打开（POST /session、CDP ws 升级）→ 持有（流量即 touch）
           → 客户端关闭，或 evict revoke
```

准入即承诺：`BeginOperation` 在 tenant bed 的 `inflight 0→1` 时原子申请 `max-active-beds` 名额，
再把 `retained_until` 预留到 `timeout + idleTTL`；名额不足时不改变 bed 活跃事实，返回可重试的
429。已接纳的工作不会被 idle reaper 杀死；timeout 有默认值和硬上限，任何 operation 的阻塞时间
有上界——evict 的"拒绝-重试"必然最终成功，死锁在模型上被消除。default bed 不参与数量准入。

### 2. Bed

```text
（不存在）─Ensure 惰性创建→ resident（active ⇄ idle）
   resident ─过 retained_until 且 inflight==0→ evicting
   evicting ─revoke session → persist → 原子复核→ dormant（luggage 留在本机）
   dormant  ─同机 resume 且 generation 新鲜→ resident
   dormant  ─磁盘水位 GC→ 删除（快照仍在 store）
   任意态   ─Purge（显式销毁）→ 身份终结（删目录 + 删快照）
```

两条驱动线：

- **活跃度线**：request 的 touch 刷新 `last_active_at` 与 `retained_until`；`CollectExpired` 定时扫描过期 bed 触发 evict。evict 先 revoke 全部 session（cancel + 有界等待，shell 的 Close 也在这一阶段），再 persist，最后原子复核 `activitySeq`/`inflight`——persist 窗口内来了新活动则取消本次回收（服务优先于回收）。
- **数据同步线**：`generation` 是数据版本，`persistedAt` 是同步水位，`last_active_at > persistedAt` 即 dirty。persist 触发点：`Checkpoint`（显式）、`evict`（回收前必持久化）、`PersistDirty`（周期兜底）。语义详见 `docs/persistence.md`。

### 3. Hostel

```text
启动组装（isolation → amenity → store → bed manager → web）→ 服务
   ├─ 后台循环：idle bed reaper / luggage GC / PersistDirty 兜底
   └─ 事实上报：/healthz（可服务性）、GET /v1/beds（status + bed 概要）
→ SIGTERM 优雅关停
```

hostel 不自杀，也没有 drain 接口。它表达"可以释放我"的唯一方式是 `GET /v1/beds` 里的 `instance.status`（retained / draining / releasable / pinned）——判据收敛在 hostel 内，上游只读结论，不再自己拼 bed_counts / store / luggage。

容量准入与这里的生命周期状态正交：`instance.status` 回答“能否安全释放这个 Hostel”，未来的
`admission.accepting_new_beds` 回答“资源余量是否还能承接新的 active bed”。短期数量安全阀与长期
pod/cgroup 资源水位方案见 `resource.md`〈当前准入策略〉。

## 接口边界

| 接口 | 回答的问题 |
|------|-----------|
| `GET /v1/beds` | hostel 什么状态（`instance.status`）+ 全部 bed 概要（含 dormant，无 bed 内部细节） |
| `GET /v1/beds/:id` | 这个 bed 为什么是这个状态：`status` + `activity`（operations / sessions 按 kind 展开） |
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

### 为什么终结权必须在上一层

数据面组件自行了断会破坏调度语义：bed 自杀会让 manager 的 placement 出现幽灵，hostel 自杀会让 sandctl 失去对 carrier 的控制，且 noop store 下自杀就是数据丢失。各层只持有"自己能否被安全终结"的事实并推导成 status 暴露，终结动作留给上一层。
