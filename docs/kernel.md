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

Bed 是上层路由与归属的单位，不等于某个 OS 进程。Executor 丢失后可为同一 Bed 重建，
不会替换其 BedFS；resident Bed 的网络身份也独立于 Executor。Store Manager 与
Network Manager 是实例级组件，分别拥有持久化与网络资源的实现和调度。
Privilege Manager 拥有权限策略、UID 分配与诊断。组件为具体 allocation 绑定生命周期参与者，
Bed Manager 通过统一的 `Lifecycle` hooks 驱动它们；`Component[R]` 另外提供类型化诊断。
Bed Manager 决定执行顺序与何时发布 Ready、释放身份，各组件实现自己的资源动作。
本地身份覆盖 resident、luggage 和待删除目录，寿命可以长于一次 resident allocation。

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
[observability.md](observability.md)，实现锚点在 `internal/executor`、`internal/supervisor`
与 `internal/bed/execution.go`。

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
