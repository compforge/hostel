# Bed：身份、声明与生命周期

Bed 是 Hostel 的工作负载与资源归属单位。调用方声明期望状态，Bed Manager 协调数据准备、
运行资源、请求准入和回收。完整对象关系见 [kernel.md](kernel.md)；本篇定义这些动作的
先后顺序与失败后的责任，数据和进程细节分别见 [volumes.md](volumes.md)、[pillow.md](pillow.md)。

## 身份与声明

身份分为调用方的 `Bed.Name` 与本地的 `Bed.ID`。Name 是不解释业务含义的路由键，支持中文；
请求头 `X-Hostel-Bed`、查询参数 `bed`、管理路径/请求体中的 `id` 和命令环境 `BED_ID`
都沿用 Name 语义。Bed Manager 解析 Name，领域资源（UID、netns、Executor/cgroup）
使用 Hostel 生成的本地 ID；Amenity Manager 用本地 ID 绑定设施内的 Tenant ID。远端快照按 Name / 快照引用定位。

一个本地生命周期只有一个共享 `*bed.Bed`，从 Recover 到 Forget 都使用它；初始化重试和
Executor 重建只替换领域资源，不新建 Bed。ID 保存在 `{beds-root}/.identities/{name}.local`，
重启时与本地目录一起恢复；该记录位于可替换 BedFS 树之外，不进入远端快照。正常 evict/purge
在运行资源和所有本地目录清理成功后执行 Forget、删除记录；同名再次创建产生新 ID。
资源分配的 generation/租约由对应 Manager 持有，清理按原 allocation 执行。

Lifecycle 状态是初始化进度与调度视图的唯一来源；Store 统一发布快照/磁盘提示，Executor
观察自身退出并更新状态，Network 串行提交同一 Bed 的策略与状态，避免旧结果覆盖新结果。

BedSpec 包含有序 Pillows、命名 Volumes、端口及网络需求。本地生命周期内执行环境声明固定，
重复创建须匹配已有声明，原生 Ensure 加入已有初始化。运行期网络策略通过专用接口更新。
远端卷快照保存数据契约；跨实例重建时，调用方重新提交 Pillow 和宿主资源引用。

## 请求、Bed 与实例状态

请求按生命周期由谁收口分为两类：

| 类别 | 生命周期边界 | 回收行为 | 例子 |
|---|---|---|---|
| operation | 系统通过默认 timeout 和硬上限收口 | inflight > 0 时拒绝普通 evict，完成后可重试 | command、file、browser verbs、checkpoint |
| session | 客户端显式开闭，可能长期持有 | evict 主动 revoke，取消并等待使用者退出 | shell、CDP 连接 |

session 不增加 activity：只有空闲 CDP 连接的 Bed 仍可回收。shell 与 CDP 同属 session，
因为它们都由客户端持有状态，并需要在回收时主动终止。把长连接当作无界 operation 会让
回收永远等待，所以 operation 的 timeout 必须有界。

operation 开闭、session 打开和 session 的真实流量会 touch，按既有同步约定维护
`last_active_at`，并推进保活基准 `keepalive_at`。状态查询和重放不 touch；
控制面轮询不能让 Bed 显得活跃。显式续期只推进保活基准，不表示发生真实活动。
[Transfer](volumes.md) 属于 file operation，持有范围覆盖后台传输，不随创建它的 HTTP 请求结束。

生命周期事实按归属维护，上层状态由下层事实推导：

- Bed Manager 维护 Bed 的 `phase/readiness`，区分初始化、可服务、回收与失败。
- resident / 仍在持久化复核的 evicting Bed 的 `activity` 由 operation 数量推导；
  已停止准入的 CleanupPending 不再报告 activity。
- Hostel 的 `instance.status` 由 Bed 状态和保留期限推导，不单独存储：仍有保留或初始化中的 Bed
  为 retained，全部过期但回收未完成为 draining，无 resident 且满足释放条件时为 releasable。

```text
Bed phase：initializing → resident → evicting → 已移除
               └失败→ failed        Purge → purging → 已移除
resident activity：active ⇄ idle
readiness：是否可服务，以及当前等待或失败原因
```

`dormant` 只表示异常退出后遗留的本地目录。phase、readiness、activity 与数据同步状态是
正交事实；容量统计对这些事实做集合投影，具体口径和状态图见 [resource.md](resource.md#容量状态口径)。
对外状态及最近动作摘要的投影见 [observability.md](observability.md#生命周期接口投影)。

## 组件参与生命周期

每个 Bed hook 接收同一个 `*bed.Bed`，资源按具体 allocation 归属。组件还提供类型化
`Status() S`，由 Bed Manager 聚合，API handler 组合领域报告与设施状态，view 只定义响应结构及纯转换。报告契约见 [observability.md](observability.md)。

| Hook | Bed Manager 驱动时机与完成条件 |
|---|---|
| Recover | 启动准入前恢复已有本地身份；Privilege 根据目录 owner 保留 UID |
| Prepare | 初始化时先准备 Store、Filesystem 数据、Privilege、Network（含初始策略），再组装文件视图和 Environment，准备 Resource、Configuration、Executor、Amenity、Pillow 主进程；全部成功后才发布 Ready |
| Stop | 已停止数据面准入后终止 Transfer 与 Executor，阻止继续使用资源 |
| Release | Stop 成功后释放 Pillow 主进程、设施切片、Executor、资源组、Configuration、BedFS、网络和 Store 任务状态；文件视图先于其引用的网络配置释放，不释放 UID |
| Forget | 运行资源和本地 Bed / `.gc-*` 目录全部清理后，由 Privilege 释放身份 |

Stop / Release 保存每个参与者的完成进度，失败重试从未完成的 hook 继续；已成功的 hook 不重复执行。
Prepare 的失败保留已有冷数据；若资源回滚失败，原 allocation 进入待清理集合。
Executor 的实际创建与替换仍按需进行，Store 的 Persist / Delete 仍是领域动作，不扩成通用 hook。

## 启动恢复与日常运行

daemon 组装 Bed Manager 与 Amenity Manager，驱动全局启动。Bed Manager 的 Start 同步完成
领域初始化、本地目录恢复和 UID 保留；组合环境探测完成后才开放 Web。启动失败逆序关闭
已启动的组件，不能让未恢复的本地身份进入准入。

Run 启动 Store 同步、Resource 采样与 Bed 回收循环，并在取消或终止错误后等待循环退出。
各领域自己安排周期、合并和退避；后台终止错误通知 daemon 进入统一关闭流程。

## Bed 初始化与请求准入

```text
选择 Bed → 初始化或加入同一初始化任务 → Ready
  → operation / session 准入
  → BedFS、Executor 或 Amenity 的 Bed 级动作
  → 结果与终态 → touch / 同步触发 → 空闲回收
```

管理 API 接受 desired state：`POST /v1/beds` 为新任务返回 202，后台准备数据与运行环境，
已 Ready 返回 200。Store Stat/Restore 延迟属于数据准备，不占住创建连接。原生数据面通过
Ensure 加入同一任务并等待 Ready；远端故障不能被当成空 Bed，也不能暴露半恢复目录。

同 Name 初始化合并为一个任务，在任何 Store I/O 前预占容量。原生请求通过 header/query
选择 Bed，省略时使用 default；isolated-session 映射非 default Bed，不维护平行生命周期。
数量上限与 carrier 资源准入见 [resource.md](resource.md)，default 不参与 tenant 容量计数。

Ready 必须等到 Store Stage-in、BedFS、身份、已启用的网络及初始策略、资源记账、设施切片、Pillow 环境
及必需主进程全部准备完成。Executor 仍可按需创建；Ready 表达本次选定能力已准备好，不表示所有维度
均达到理想隔离，也不替代执行组合的端到端验证。

请求 handler 统一调用 Bed Manager 准入；跨 HTTP 请求存活的工作显式持有 operation。
准入会把保留期限预留到 timeout + idleTTL，已接纳的 operation 不被 idle reaper 杀死。
观测请求只读事实，不延长保留期。

## Bed 保活与续期

Bed Manager 是保活与回收决策的唯一 owner。共享 Bed 的 Lifecycle Status 保存
`keepalive_at`；它是保活计时基准，可能在未来，不是最后一次真实请求的发生时间。
`retained_until` 由该基准加本次驻留固定的 idleTTL 派生，不另存一份期限。

各调用路径通过同一单调推进规则表达保留需要：

- operation 准入：基准至少推进到当前时间加归一化后的 operation timeout。
- operation 结束、session touch：基准至少推进到当前时间，不覆盖已有更晚的承诺。
- renew 未指定期限：与操作结束的保活效果相同，但不更新活动或数据同步水位。
- renew 指定 expiresAt：基准至少推进到 expiresAt 减 idleTTL。

自动回收要求无 operation，且 `keepalive_at + idleTTL` 已到期；idleTTL 为零时禁用
自动到期，default Bed 仍不参与自动回收。operation 的执行超时由各执行入口负责，
保活承诺不取代任务取消。清理、Pillow 主进程 readiness 更新和状态发布不得覆盖保活基准。

续期与开始回收在 Bed Manager 内原子互斥：续期先成功则回收必须遵守更晚期限；
回收先开始则拒绝续期，不重新开放正在退出的 Bed。只有当前 resident 身份可续期，
不通过 Ensure 创建或唤醒；resident 的 readiness 暂时失败不妨碍保活。

续期不是 operation，不改变 active、pinned 或 Store 同步事实。一个 idle、非 pinned
的 Bed 仍可处于保留期，继续占用 resident/occupied 容量，使 Carrier 保持 retained。
期限只约束自动回收，不阻止显式删除或 daemon shutdown，也不保证 Pillow 主进程不会故障重启。
保活基准属于本次驻留，不进入 Bed 快照；daemon 重启后由上层重新确认运行态与保留期限。

## 数据同步与空闲回收

```text
resident → 保留期到期且无 operation → 关闭准入 → evicting
  → 停止 Pillow / Session / Transfer / Executor → persist
    ├─ 失败：保留本地数据，恢复可用环境或报告未就绪
    └─ 成功：Release → 删除本地目录 → Forget
已移除 → 再次初始化 → durable: Restore / noop: fresh → resident
```

初始化、operation、session 与 pressure 向 Store 提交同步 trigger，由 Store 合并、串行执行
并负责周期和失败退避；Checkpoint 与 evict 是必须等待同步结果的边界。数据版本、同步水位
与策略详见 [volumes.md](volumes.md)。

CollectExpired 扫描过期 Bed。普通 evict 在 operation 仍存活时拒绝；Transfer 的持有范围
覆盖整个传输。session 必须先撤销并等待退出，再 persist，避免会话写入与快照并发。
回收准入屏障拒绝新 operation，停止现有进程和传输后再持久化。删除前仍复核
`activitySeq` / `inflight`；persist 失败中止 evict，因为本地目录可能是唯一副本。

原子复核通过后，Bed 从 resident 移入待清理集合，继续占用 occupied 名额，以
`evicting / CleanupPending / not ready` 披露进度。运行资源清理和本地目录删除完成后才
释放身份；正常 evict 在 durable 与 noop 下都删除本地目录。

## Purge 与清理重试

Purge 显式终结 Bed 身份，进入 purging，取消并等待同 ID 初始化退出，取消并 join Transfer，
随后清理运行资源、本地目录与远端快照。Stop / Release 的任何一步失败都保留原 allocation
及资源句柄，后续 Evict、Purge 或关闭继续重试；同 ID 初始化和目录 GC 不能接管其目录。
初始化回滚失败也保留相同的清理占位。

本地删除由统一身份 owner 串行执行，先重命名为 `.gc-*`，使中断残留在重启时仍可恢复为
清理占位。同 ID 创建等待正在执行的清理；清理已失败或待重试时返回不可用，不等待下次
定时任务。启动、定时任务与关闭都尝试完成已认领的冷目录清理，不受 luggage 磁盘水位开关
控制。同 ID 仍有完整冷目录时只删除残留并保留 UID，之后允许重新初始化。

## 关闭与实例释放

关闭先停止准入，取消并等待后台循环，再取消并等待初始化，清理各 Bed 的运行资源，最后
逆序关闭领域组件。daemon 随后关闭共享 Amenity。正常关闭保留本地数据与 UID 对应关系，
不对所有 Bed 执行 Forget；Purge 才表达显式销毁。

生命周期终结由上层 owner 驱动：调用方取消请求，Bed Manager 回收 Bed，上层调度释放
Hostel。Hostel 没有 drain 接口，也不因空闲自行退出；它通过 `instance.status` 上报能否
安全释放，由上层调度决定。进程故障或后台终止错误仍进入关闭路径。

`instance.status` 与资源准入正交：前者回答能否安全释放，后者回答是否接纳新 Bed。
接口返回的是允许陈旧的观察值，正确性由准入和回收时的原子复核保证。

### 关闭与清理预算

`Close(ctx)` 的 deadline 是整个实例关闭的总预算。等待已有清理 owner、停止初始化/Purge、
逐 Bed Stop/Release 和组件 Close 都使用这一边界，不再给每个 Bed 重置独立关闭时间。
请求取消后的初始化回滚和 Purge 由 Bed Manager 持有，daemon 关闭会取消并 join；失败保持
清理 owner、已完成 hook 游标和名字占位，后续 Close/Evict 可重试，不能提前复用 UID 或资源。
