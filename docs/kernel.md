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
| Bed | 调用方指定 ID 的 sandbox 单元，持有文件数据、配置与生命周期 |
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
Status 分为 Lifecycle、Filesystem、Privilege、Network、Store、Executor、Resource、Amenity 八个
类型化部分。各领域能读取完整快照，只获得自己部分的 `StatusWriter`；Bed Manager 拥有 Spec 和
Lifecycle 的更新权。读写都复制可变成员，不能通过返回的 map、slice 或指针绕过写入边界。

资源句柄、锁、后台任务和客户端由对应领域 Manager 持有。Bed Manager 内部的 `managedBed`
承载 operation/session、版本水位与清理重试等协调状态，通过 `Resident` 操作句柄供入口层使用；
它引用共享 Bed，不另建一份领域 Spec/Status。调度 API 的 Status 是这些生命周期事实的投影。

身份分三个寿命：调用方的 Bed ID、本 daemon 保留数据期间的 LocalID、一次初始化的 InstanceID。
LocalID 覆盖 resident、冷目录和待删除目录；InstanceID 区分同名 Bed 的前后两次资源分配。
这些内部标识不改变对外 API 或磁盘格式。清理必须针对原 allocation，不能让旧 hook 按 ID 清掉替代实例。

领域 Manager 有三个驱动面：daemon 的 `Start/Close`、带 `*bed.Bed` 的单 Bed hooks，以及可选的
`Run(ctx)` 后台循环。Start 完成同步初始化后返回，Run 阻塞到取消或终止错误；定时节奏、合并与退避
由领域自己决定。`Component[R]` 汇合启停、Bed lifecycle 和类型化诊断，允许未参与的阶段嵌入 Noop。
Bed Manager 明确安排跨领域顺序；不使用动态注册顺序推导依赖，也不统一抽象 Tick。

Amenity 的全局启停直属 daemon；Bed Manager 只驱动其 Bed 切片。Web handler 负责协议适配，Bed 级
文件、网络、浏览器、MCP 与执行都经过 Bed Manager 的准入。隔离是多个领域共同实现的结果，
文件隔离机制归 Filesystem，网络、身份与执行环境的组合顺序归 Bed Manager。

## 三、主流程

```text
请求选择 Bed
  → 加入初始化，或取得已 Ready 的 Bed
  → Bed operation / session 管理使用与回收边界
  → BedFS 文件操作，或经组合后的执行环境交给 Executor
  → Execution 输出与终态，或共享设施的 Bed 级结果
  → 数据同步、空闲回收与资源释放
```

管理 API 异步接受初始化，数据面的 Ensure 加入同一任务并等待 Ready，不能看到半成品
目录。原生请求通过 header/query 选择 Bed，省略时使用 default；isolated-session 视图
映射非 default Bed，不维护平行的 sandbox 生命周期。

operation 是有界的使用承诺，session 是显式持有、可被回收撤销的状态。Bed 生命周期
协调初始化、执行与回收，Store 在同步边界保存数据，各资源组件负责释放自身资源。
具体并发、状态与保留期契约见 [lifecycle.md](lifecycle.md)。

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

## 四、Bed 独立性与共享实现

隔离沿 Bed 的统一目标演进，各维度按环境尽量兑现。文件房型 dorm/room/suite 只表示文件
边界的兑现程度，不代表网络、进程或资源已经全面隔离。BedFS 的路径归属在所有档位一致，
具体进程视图与强制访问控制由选中的机制提供。完整模型、组合约束和能力缺口见
[isolation.md](isolation.md)。

BedFS 负责逻辑数据根及路径投影；Store 管理自动持久化，也提供无业务含义的 [Bed ↔ S3 文件传输](transfers.md)。
自动持久化负责数据在生命周期之外能否恢复。当前默认只
持久化 workspace 子树和 Bed 元数据。正常 evict 删除本地工作副本，durable 策略可从快照
恢复，noop 不保留数据。配置归属与恢复契约见 [store.md](store.md)。

Amenity 共享重型进程或连接管理，按 Bed 分配应用状态。Chromium 使用 BrowserContext，
MCP 使用独立配置、凭据与会话；Jupyter 实例仍在待办。共享设施没有自动进入 Bed 命令的
netns，其出站与故障边界必须单独说明。设施协议不得裸透传跨 Bed 能力，具体代理与
过滤边界见 [amenity.md](amenity.md) 和 [mcp.md](mcp.md)。

## 五、模块边界与深入阅读

`cmd/hostel` 组装配置与组件；`web` 负责 HTTP 路由、协议适配和结果投影；Bed、BedFS、
Executor、Store 与 Network 保持领域及资源操作职责，不依赖 HTTP 类型。新增机制应在所属
边界内扩展，跨机制顺序由执行与生命周期入口协调，不能依赖调用方自行拼接内部细节。

| 文档 | 所有权与内容 |
|---|---|
| [isolation.md](isolation.md) | Bed 理想隔离语义、尽力兑现、机制组合与实际保证 |
| [filesystem.md](filesystem.md) | BedFS、路径空间和进程投影 |
| [lifecycle.md](lifecycle.md) | 初始化、operation/session、回收与状态 |
| [network.md](network.md) | 网络资源、连通性与出站边界 |
| [store.md](store.md) | 策略选择、快照与恢复 |
| [resource.md](resource.md) | 资源采集、记账、准入与硬限额边界 |
| [amenity.md](amenity.md) | 共享设施与 Bed 状态切分 |
| [observability.md](observability.md) | 生命周期事实到日志、接口与 trace 的投影 |
| [backlog.md](backlog.md) | 尚未交付的能力与待修复项 |

使用入口见 [README](../README.md)，代码地图与开发约定见 [AGENTS.md](../AGENTS.md)。
