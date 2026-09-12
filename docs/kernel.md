# Hostel 核心架构

## 一、定位与目标

Hostel 是面向 AI Agent 的 sandbox runtime，在一个 Carrier（一台机器或一个容器）内，
用一份重运行环境低成本承载多个 Bed。理想状态下 Bed 的文件、进程、网络和资源相互隔离；
Hostel 根据环境能力尽量兑现，并如实披露实际边界。当前面向可信或半可信代码。

Hostel 负责实例内 Bed 生命周期、执行、文件、持久化、可选网络和共享设施。上层调度系统
负责实例选择、跨实例路由、单写者归属与信任分档。资源与文件 API 以 OpenSandbox execd
为设计基线，执行协议由 Hostel 自己拥有，归属见 [NOTICE](../NOTICE)。

## 二、核心概念与所有权

| 对象 | 身份与职责 |
|---|---|
| Bed | 调用方以 Name 路由、本地以 ID 归属的 sandbox 单元，持有文件数据、配置与生命周期 |
| BedFS | Bed 的文件系统数据域，拥有 bed_home、workspace 和三类路径空间 |
| Executor | Bed 当前可替换的进程承载域，一个 resident Bed 同时至多有一个 |
| Execution | 一次命令运行，记录所属 Bed、Executor、输出与结构化终态 |
| Amenity | Carrier 共享的设施，按 Bed 分配状态并管理设施本身的生命周期 |

Bed 是上层路由与归属的单位，不等于某个 OS 进程。Hostel 在 Pod 内以进程为中心提供轻量隔离单元，
Executor 替换不会改变 Bed 的数据和网络身份。

```text
daemon
├── Web Server
├── Bed Manager（composite）
│   ├── Filesystem Manager
│   ├── Privilege Manager
│   ├── Network Manager
│   ├── Store Manager
│   ├── Executor Manager
│   └── Resource Manager
└── Amenity Manager
```

`internal/bed` 只放共享模型和契约，全仓只有一个 `Bed` 类型。Spec 保存解析后的配置与准备输入，
Status 分为 Lifecycle、Filesystem、Privilege、Network、Store、Executor、Resource 七个
类型化部分。各领域能读取完整快照，只获得自己部分的 `StatusWriter`；Bed Manager 拥有 Spec 和
Lifecycle 的更新权。读写都复制可变成员，不能通过返回的 map、slice 或指针绕过写入边界。

资源句柄、锁、后台任务和客户端由对应领域 Manager 持有。Bed Manager 内部的 `managedBed`
承载 operation/session、版本水位与清理重试等协调状态，通过 `Resident` 操作句柄供入口层使用；
它引用共享 Bed，不另建一份领域 Spec/Status。调度 API 的 Status 是这些生命周期事实的投影。

身份分为调用方的 `Bed.Name` 与本地的 `Bed.ID`。Name 是不解释业务含义的路由键，支持中文；
请求头 `X-Hostel-Bed`、查询参数 `bed`、管理路径/请求体中已有的 `id` 和命令环境 `BED_ID`
都沿用 Name 语义。Bed Manager 解析 Name，领域资源（UID、netns、Executor/cgroup）
使用 Hostel 生成的本地 ID；Amenity Manager 用本地 ID 绑定设施内的 Tenant ID。远端快照仍按 Name / 快照引用定位。

一个本地生命周期只有一个共享 `*bed.Bed`，从 Recover 到 Forget 都使用它；初始化重试和
Executor 重建只替换领域资源，不新建 Bed。ID 保存在 `{workspace-root}/.identities/{name}.local`，
重启时与本地目录一起恢复；该记录位于可替换 BedFS 树之外，不进入远端快照。正常 evict/purge
在运行资源和所有本地目录清理成功后执行 Forget、删除记录；同名再次创建产生新 ID。
资源分配的 generation/租约由对应 Manager 持有，清理按原 allocation 执行。

Lifecycle 状态是初始化进度与调度视图的唯一来源；Store 统一发布快照/磁盘提示，Executor
观察自身退出并更新状态，Network 串行提交同一 Bed 的策略与状态，避免旧结果覆盖新结果。

### 请求、Bed 与实例状态

请求按生命周期由谁收口分为两类：

| 类别 | 生命周期边界 | 回收行为 | 例子 |
|---|---|---|---|
| operation | 系统通过默认 timeout 和硬上限收口 | inflight > 0 时拒绝普通 evict，完成后可重试 | command、file、browser verbs、checkpoint |
| session | 客户端显式开闭，可能长期持有 | evict 主动 revoke，取消并等待使用者退出 | shell、CDP 连接 |

session 不增加 activity：只有空闲 CDP 连接的 Bed 仍可回收。shell 与 CDP 同属 session，
因为它们都由客户端持有状态，并需要在回收时主动终止。把长连接当作无界 operation 会让
回收永远等待，所以 operation 的 timeout 必须有界。

operation 开闭、session 打开和 session 的真实流量会 touch，刷新 `last_active_at` 与
`retained_until`。状态查询和重放不 touch；控制面轮询不能让 Bed 显得活跃。
[Transfer](transfers.md) 属于 file operation，持有范围覆盖后台传输，不随创建它的 HTTP 请求结束。

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

### 领域与 Host 机制

实现按“入口 → 业务概念与流程 → 底层机制”协作：Web、daemon 和后台触发进入领域流程；
Bed 与 Amenity 的 Manager 决定资源为谁使用、使用策略及生命周期；`internal/host` 提供通用
宿主能力。底层资源经领域赋予身份与归属，再组合成 Bed 操作或设施能力。

Host 按能力分包，不设置统一 HostManager，也不预设调用方是 Bed、Amenity 或其他组件：

- `host/network`：namespace、地址、DNS 与出站规则，返回具体网络 allocation。
- `host/filesystem`：有序挂载、Landlock、pathshim/PRoot 进程路径视图及 ptrace 探测。
- `host/cgroup`：层次与组、进程放置句柄、用量和释放；组名与父子组织由调用方决定。
- `host/privilege`：进程 UID/GID、capability 清除及文件 ownership 操作。
- `host/facts`：只读系统事实与执行探测记录；事实不等于机制已可用。

Host 不导入 Bed/Amenity 模型，不解释 `/workspace`、房型或 Tenant。BedFS 路径语义、
房型与降级、BedUser/UID 租约、Bed → Executor 资源层次和准入仍由领域负责。
PRoot/pathshim 提供路径视图，不因此成为安全边界。Host 返回真实结果和资源句柄，
领域决定是否允许降级并发布 component status。实例聚合器将 Host 的只读启动事实放入
`/v1/status.host`，与 `components`、`amenities` 并列；Host 不组装接口或业务 readiness。

资源按具体分配清理，关闭旧句柄不能影响同名的新分配；部分失败保留原清理 owner，成功后才允许复用。
提取通用机制不自动让 Amenity 获得隔离，设施仍自行选择和组合所需能力。

## 三、组件契约

领域 Manager 有三个驱动面：daemon 的 `Start/Close`、带 `*bed.Bed` 的单 Bed hooks，以及可选的
`Run(ctx)` 后台循环。Start 完成同步初始化后返回，Run 阻塞到取消或终止错误；定时节奏、合并与退避
由领域自己决定。`Component[S]` 汇合启停、Bed lifecycle 和类型化诊断，允许未参与的阶段嵌入 Noop。
Bed Manager 明确安排跨领域顺序；不使用动态注册顺序推导依赖，也不统一抽象 Tick。

Amenity 的全局启停直属 daemon；Bed Manager 通过薄生命周期适配器通知 Amenity Manager 释放 Bed 绑定的 Tenant。Web handler 负责协议适配，Bed 级
文件、网络、浏览器、MCP 与执行都经过 Bed Manager 的准入。隔离是多个领域共同实现的结果，
文件隔离机制归 Filesystem，网络、身份与执行环境的组合顺序归 Bed Manager。

### 组件参与生命周期

每个 Bed hook 接收同一个 `*bed.Bed`，资源按具体 allocation 归属。组件还提供类型化
`Status() S`，由 Bed Manager 聚合，Web 只序列化，报告契约见 [observability.md](observability.md)。

| Hook | Bed Manager 驱动时机与完成条件 |
|---|---|
| Recover | 启动准入前恢复已有本地身份；Privilege 根据目录 owner 保留 UID |
| Prepare | 初始化时按 Store、Filesystem、Privilege、Network（含初始策略）、Resource、Executor、Amenity 顺序准备；全部成功后才发布 Ready |
| Stop | 已停止数据面准入后终止 Transfer 与 Executor，阻止继续使用资源 |
| Release | Stop 成功后释放设施切片、Executor、资源组、网络、BedFS 和 Store 任务状态；不释放 UID |
| Forget | 运行资源和本地 Bed / `.gc-*` 目录全部清理后，由 Privilege 释放身份 |

Stop / Release 保存每个参与者的完成进度，失败重试从未完成的 hook 继续；已成功的 hook 不重复执行。
Prepare 的失败保留已有冷数据；若资源回滚失败，原 allocation 进入待清理集合。
Executor 的实际创建与替换仍按需进行，Store 的 Persist / Delete 仍是领域动作，不扩成通用 hook。

## 四、主流程

### 启动恢复与日常运行

daemon 组装 Bed Manager 与 Amenity Manager，驱动全局启动。Bed Manager 的 Start 同步完成
领域初始化、本地目录恢复和 UID 保留；组合环境探测完成后才开放 Web。启动失败逆序关闭
已启动的组件，不能让未恢复的本地身份进入准入。

Run 启动 Store 同步、Resource 采样与 Bed 回收循环，并在取消或终止错误后等待循环退出。
各领域自己安排周期、合并和退避；后台终止错误通知 daemon 进入统一关闭流程。

### Bed 初始化与请求准入

```text
选择 Bed → 初始化或加入同一初始化任务 → Ready
  → operation / session 准入
  → BedFS、Executor 或 Amenity 的 Bed 级动作
  → 结果与终态 → touch / 同步触发 → 空闲回收
```

管理 API 接受 desired state：`POST /v1/beds` 为新任务返回 202，后台准备数据与运行环境，
已 Ready 返回 200。Store Stat/Restore 延迟属于数据准备，不占住创建连接。原生数据面通过
Ensure 加入同一任务并等待 Ready；远端故障不能被当成空 Bed，也不能暴露半恢复目录。

同 ID 初始化合并为一个任务，在任何 Store I/O 前预占容量。原生请求通过 header/query
选择 Bed，省略时使用 default；isolated-session 映射非 default Bed，不维护平行生命周期。
数量上限与 carrier 资源准入见 [resource.md](resource.md)，default 不参与 tenant 容量计数。

Ready 必须等到 Store Stage-in、BedFS、身份、已启用的网络及初始策略、资源记账和设施切片
全部准备完成。Executor 仍可按需创建；Ready 表达本次选定能力已准备好，不表示所有维度
均达到理想隔离，也不替代执行组合的端到端验证。

请求 handler 统一调用 Bed Manager 准入；跨 HTTP 请求存活的工作显式持有 operation。
准入会把保留期限预留到 timeout + idleTTL，已接纳的 operation 不被 idle reaper 杀死。
观测请求只读事实，不延长保留期。

### 执行与进程归属

`/command` 每次创建独立进程；只有显式 `/session` 才保留 shell 的 cd/export 等状态。
普通命令之间通过文件延续状态，避免某条脚本的 exit、trap 或 set -e 终结其他执行。

```text
hostel daemon
├─ Chromium 等共享设施进程
└─ Bed（领域身份，非 OS 进程）
   └─ Executor（当前进程承载域）
      └─ supervisor（Linux backend）
         ├─ 一次性命令进程
         └─ 显式 session shell
```

Linux supervisor 负责派生、收尸与整域回收，可重连 IPC 和幂等 Start 避免重试重复执行。
local backend 由 daemon 直接派生并管理进程组，不承诺清理已脱离进程组的后代。
两种 backend 都通过同一 Executor/Process 契约提供终态；容器内 tini 是进程收尸的额外兜底。

Execution 区分进程退出、信号终结和丢失，并独立记录 timeout/cancel/teardown 等终止原因。
Executor 丢失终结其所属 Execution，不能把传输 EOF 当成正常退出。协议和观测见
[observability.md](observability.md)，实现锚点在 `internal/bed/executor`、`internal/supervisor`
与 `internal/bed/manager/execution.go`。

### 数据同步与空闲回收

```text
resident → 保留期到期且无 operation → evicting
  → revoke session → persist → 原子复核活动
    ├─ 有新活动：取消本次回收
    └─ 无新活动：停止准入 → Stop → Release → 删除本地目录 → Forget
已移除 → 再次初始化 → durable: Restore / noop: fresh → resident
```

初始化、operation、session 与 pressure 向 Store 提交同步 trigger，由 Store 合并、串行执行
并负责周期和失败退避；Checkpoint 与 evict 是必须等待同步结果的边界。数据版本、同步水位
与策略详见 [store.md](store.md)。

CollectExpired 扫描过期 Bed。普通 evict 在 operation 仍存活时拒绝；Transfer 的持有范围
覆盖整个传输。session 必须先撤销并等待退出，再 persist，避免会话写入与快照并发。
持久化窗口可能出现新活动，所以删除前必须原子复核 `activitySeq` / `inflight`；有新活动
就取消回收。persist 失败也中止 evict，因为本地目录可能是唯一副本。

原子复核通过后，Bed 从 resident 移入待清理集合，继续占用 occupied 名额，以
`evicting / CleanupPending / not ready` 披露进度。运行资源清理和本地目录删除完成后才
释放身份；正常 evict 在 durable 与 noop 下都删除本地目录。

### Purge 与清理重试

Purge 显式终结 Bed 身份，进入 purging，取消并等待同 ID 初始化退出，取消并 join Transfer，
随后清理运行资源、本地目录与远端快照。Stop / Release 的任何一步失败都保留原 allocation
及资源句柄，后续 Evict、Purge 或关闭继续重试；同 ID 初始化和目录 GC 不能接管其目录。
初始化回滚失败也保留相同的清理占位。

本地删除由统一身份 owner 串行执行，先重命名为 `.gc-*`，使中断残留在重启时仍可恢复为
清理占位。同 ID 创建等待正在执行的清理；清理已失败或待重试时返回不可用，不等待下次
定时任务。启动、定时任务与关闭都尝试完成已认领的冷目录清理，不受 luggage 磁盘水位开关
控制。同 ID 仍有完整冷目录时只删除残留并保留 UID，之后允许重新初始化。

### 关闭与实例释放

关闭先停止准入，取消并等待后台循环，再取消并等待初始化，清理各 Bed 的运行资源，最后
逆序关闭领域组件。daemon 随后关闭共享 Amenity。正常关闭保留本地数据与 UID 对应关系，
不对所有 Bed 执行 Forget；Purge 才表达显式销毁。

生命周期终结由上层 owner 驱动：调用方取消请求，Bed Manager 回收 Bed，上层调度释放
Hostel。Hostel 没有 drain 接口，也不因空闲自行退出；它通过 `instance.status` 上报能否
安全释放，由上层调度决定。进程故障或后台终止错误仍进入关闭路径。

`instance.status` 与资源准入正交：前者回答能否安全释放，后者回答是否接纳新 Bed。
接口返回的是允许陈旧的观察值，正确性由准入和回收时的原子复核保证。

## 五、Bed 独立性与共享实现

隔离沿 Bed 的统一目标演进，各维度按环境尽量兑现。文件房型 dorm/room/suite 只表示文件
边界的兑现程度，不代表网络、进程或资源已经全面隔离。BedFS 的路径归属在所有档位一致，
具体进程视图与强制访问控制由选中的机制提供。完整模型、组合约束和能力缺口见
[isolation.md](isolation.md)。

BedFS 负责逻辑数据根及路径投影；Store 管理自动持久化，也提供无业务含义的 [Bed ↔ S3 文件传输](transfers.md)。
自动持久化负责数据在生命周期之外能否恢复。当前默认只
持久化 workspace 子树和 Bed 元数据。正常 evict 删除本地工作副本，durable 策略可从快照
恢复，noop 不保留数据。配置归属与恢复契约见 [store.md](store.md)。

Amenity 是独立设施。Tenant 是 Hostel 为服务 Bed 定义的设施使用单元，具体资源实现由设施隐藏。Chromium 使用 BrowserContext，
MCP 使用独立配置、凭据与会话；Jupyter 实例仍在待办。共享设施没有自动进入 Bed 命令的
netns，其出站与故障边界必须单独说明。设施协议不得裸透传跨 Bed 能力，具体代理与
过滤边界见 [amenity.md](amenity.md) 和 [mcp.md](mcp.md)。

## 六、模块边界与深入阅读


`cmd/hostel` 组装配置与组件；`web` 负责 HTTP 路由、协议适配和结果投影；Bed、BedFS、
Executor、Store 与 Network 保持领域职责，不依赖 HTTP 类型；通用宿主机制归 `internal/host`。
跨机制顺序由领域执行与生命周期入口协调，不能让 Web handler 拼接内部细节。

| 文档 | 所有权与内容 |
|---|---|
| [isolation.md](isolation.md) | Bed 理想隔离语义、尽力兑现、机制组合与实际保证 |
| [filesystem.md](filesystem.md) | BedFS、路径空间、进程视图与文件隔离机制 |
| [network.md](network.md) | 网络资源、连通性与出站边界 |
| [store.md](store.md) | 策略选择、快照与恢复 |
| [resource.md](resource.md) | 容量状态口径、资源采集、记账、准入与硬限额边界 |
| [amenity.md](amenity.md) | 共享设施与 Bed 状态切分 |
| [observability.md](observability.md) | 生命周期事实到日志、接口与 trace 的投影 |
| [backlog.md](backlog.md) | 尚未交付的能力与待修复项 |

使用入口见 [README](../README.md)，代码地图与开发约定见 [AGENTS.md](../AGENTS.md)。

### 关闭与清理预算

`Close(ctx)` 的 deadline 是整个实例关闭的总预算。等待已有清理 owner、停止初始化/Purge、
逐 Bed Stop/Release 和组件 Close 都使用这一边界，不再给每个 Bed 重置独立关闭时间。
请求取消后的初始化回滚和 Purge 由 Bed Manager 持有，daemon 关闭会取消并 join；失败保持
清理 owner、已完成 hook 游标和名字占位，后续 Close/Evict 可重试，不能提前复用 UID 或资源。
