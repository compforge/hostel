# Filesystem：BedFS、进程视图与文件隔离

通用挂载、Landlock、pathshim/PRoot 和 ptrace 机制位于 `internal/host/filesystem`；BedFS 路径语义、房型选择与组合探测由 Bed Filesystem 负责。分层边界见 [核心架构](kernel.md#领域与-host-机制)。

> 状态：当前实现。隔离目标与实际边界见 [isolation.md](isolation.md)，持久化与恢复见 `store.md`。

## 一、定位

`filesystem.Manager` 统一拥有 BedFS 与文件隔离机制，负责准备、释放和文件领域诊断。
数据模型、进程视图和访问屏障在同一领域内协作；跨领域的网络进入、身份切换与执行组合
由 Bed Manager 协调。能力不足时可以选择可用档位，已选机制执行失败则明确报错。

`BedFS` 是 Bed 持有的数据域，不是一次请求里的路径工具。Bed 创建时建立一个 BedFS；Executor 丢失、替换或重建时，BedFS 的身份与数据不变，只重新生成进程视图。

BedFS 统一拥有以下语义：

- `bed_home`：客户端 `/` 对应的数据根；
- workspace：`bed_home/workspace`，客户端与进程的规范路径为 `/workspace`；
- path projections：调用方可配置多个 `BedFS path → Executor path`，Hostel 不解释其业务含义；
- client path → carrier path：file API、cwd 等结构化路径的存放位置；
- carrier path → Executor path：不同隔离机制下进程应使用的路径；
- Bed 内文件操作、属主交接与 symlink 防逃逸边界。

隔离机制不再自行解释客户端路径。它只选择 BedFS 如何投影到 Executor，并负责兑现该视图所需的 bind、Landlock 或 uid 规则。

Bed 的理想语义是独立执行空间。BedFS 负责让文件归属与路径含义在所有房型一致；
isolation 根据环境能力尽量兑现进程侧的访问屏障。Dorm 没有安全墙，仍须完成逻辑分床
与路径映射；Room 增加访问控制，Suite 使用私有 mount 视图。降级不能改变同一 Client path
的主映射。Store 另行选择需要持久化的 BedFS 子树。

面向调用方的文件操作与传输 API 统一见 [文件操作与传输](transfers.md)。

## 二、文件隔离档位与机制

`dorm / room / suite` 是文件数据隔离的三档兑现程度。请求档位与环境上限共同决定实际档位；
`auto` 选择环境可达的最高档。文件档位不替网络、PID 或 CPU/内存边界作保证。

| 房型 | 进程侧的文件边界 | 当前机制 |
|---|---|---|
| dorm | 逻辑分床，按 Carrier 进程权限访问，没有强制跨 Bed 屏障 | direct |
| room | 限制跨 Bed 数据访问，但目录存在性及公共路径仍可见 | 优先 Landlock，其次独立 UID |
| suite | 私有 mount 视图遮蔽兄弟 Bed，挂回自己的工作区和配置投影 | bwrap |

三档复用同一 BedFS 数据模型，机制不按档位逐层叠加：room 在共享视图上增加访问控制，
suite 改用私有 mount 视图。UID 与 Landlock 也不完全等价：UID 额外带来部分进程身份保护，
却依赖 UID 分配与宿主权限。房型表示文件边界的层级，细节必须结合实际机制理解。

结构化 file API 与 cwd 始终先解析到当前 BedFS，降级不改变同一客户端路径的数据落点。
PRoot/pathshim 尽量让命令中的工作区路径也指向 BedFS，但它们不是安全边界，不能抬高房型。
命令文本保持不透明，映射之外的绝对字面量由实际进程视图解释。

### 机制的取舍

**suite 遮蔽后再挂回本 Bed**。只把 Carrier 根挂成只读仍允许读取邻居数据，因此先遮蔽
工作区父目录和存在的宿主敏感路径（如 `/root`、`/home`、`/run/secrets`、
`/var/run/secrets`），再挂回当前 BedFS；较低档位不能假定具有同样的遮蔽。工具链继续共享，`/usr/local`
保留 Carrier 级可写软件环境。当前 bwrap 每次启动的 `/tmp` 是独立 tmpfs，不能当作
跨命令持久的 Bed 数据；需要延续的数据应放在 BedFS 中。

bwrap 使用 user namespace 完成挂载准备，并绑定已有 `/proc`，以适应容器内受限的 procfs。
当前没有私有 PID namespace，因此私有文件视图不等于完整的进程不可见性。机制及参数顺序
锚点在 `internal/bed/filesystem/isolation/bwrap_args.go`；部署权限示例见
[deploy/k8s/README.md](../deploy/k8s/README.md)。

**room 用访问控制兑现数据边界**。Landlock 在子进程中应用规则，UID backend 在子进程中
切换身份；daemon 必须继续服务所有 Bed，不能对自身套用某个 Bed 的边界。两者都必须在
用户代码开始前生效，已有描述符和公共路径的可达性不能仅靠路径名称推断。

UID backend 以独立进程身份兑现 room 的数据访问边界，目录属主、进程身份和 UID 租约必须保持
一致。具体的分配、重启恢复、失败清理和硬链接约束见 [privilege.md](privilege.md)；
`internal/bed/filesystem/isolation/uid_linux.go` 只负责选择该文件机制，不拥有通用身份生命周期。

## 三、三个路径空间

| 空间 | 示例 | 所有者 |
|---|---|---|
| Client | `/`、`/workspace/a`、`/tmp/job`、相对路径 `a` | BedFS；`/` 是 bed_home，相对路径以 workspace 为基准 |
| Carrier | `<workspace-root>/<bed-id>/data/...` | BedFS；daemon 的文件操作和 Store 使用 |
| Executor | direct/room 下为 carrier 路径；suite 下为内部 bed_home 挂载或 `/workspace` | BedFS `View` 定义语义，isolation 实现投影 |

映射规则只有一套：

| Client path | Carrier path |
|---|---|
| `/` | `<bed_home>` |
| `/workspace/a` | `<bed_home>/workspace/a` |
| `/tmp/job` | `<bed_home>/tmp/job` |
| `a` | `<bed_home>/workspace/a` |

绝对路径在客户端命名空间中先规范化，再单射 rebase 到 bed_home；返回路径使用逆映射。房型只改变访问屏障和进程投影，不改变数据落点。

## 四、Executor 视图

### dorm / room

Executor 与 daemon 共享 mount namespace。Hostel 启动时从 `PATH` 发现固定命令名 `proot`、`pathshim`，先记录文件是否存在、可执行及实际解析路径，再探测包含内置 `bed_home/workspace → /workspace` 和 `HOSTEL_PROJECTED_PATHS` 全部条目的进程视图。前者与配置项是同一种 `BedFS source → Executor target` 投影，只因 `/workspace` 是 Hostel 基础契约而内置，不在 env 中重复声明；配置项只承载额外投影。每个候选只有完整集合通过才可用，禁止部分生效；PRoot、pathshim 均失败时退回 Carrier 语义。

PRoot 的路径 syscall 覆盖更完整，但依赖 ptrace；pathshim 不依赖 ptrace，作为次选。两者都可用时选择 PRoot。Landlock 或 uid 始终独立负责访问边界，workspace helper 不参与 isolation level 判定。

仅展开文件视图与文件边界时，进程链保持以下职责顺序；网络及最终降权的组合约束见 [isolation.md](isolation.md)：

```text
dorm: BedUser → proot / pathshim → command
room (Landlock): fixed BedUser → __confine → proot / pathshim → command
room (UID): per-Bed BedUser → proot / pathshim → command
suite: BedUser → bwrap → command
```

PRoot 与 pathshim 都把每项 projection 指向对应 BedFS source；它们没有 COW、whiteout 或 invocation 私有状态，因此多个 command/session 共享同一 source 时并发语义就是普通底层文件系统并发。二者都是用户态进程视图，不是真实 mount、安全边界或完整 guest root。

配置格式是逗号分隔的 `BED_PATH=PROCESS_PATH`，例如：

```text
HOSTEL_PROJECTED_PATHS=/memory=/mnt/memory,/cache=/mnt/cache
```

两侧必须是绝对非根路径；配置项之间以及与内置 `/workspace` 不得重叠，`/dev`、`/proc`、`/sys` 不能作为目标。source 目录由 Hostel 按 Bed 创建，但不会因 projection 自动进入 Store 快照。

### suite

bwrap 先遮蔽 `<workspace-root>`，再投影同一 BedFS：

- 整个 `bed_home` bind 到机制私有路径 `/tmp/.hostel/bed`，使 `/`、`/tmp/job` 等任意结构化 cwd 都有进程视图；
- `bed_home/workspace` 额外 bind 到稳定的 `/workspace`，保持 OpenSandbox 与 agent 工具链约定；
- 每个配置 projection 把对应 BedFS source bind 到声明的 Executor path；
- BedFS 的 workspace 子树优先使用 `/workspace`，其余路径使用内部 bed_home 投影。

内部挂载点不是北向协议。调用方继续传 Client path；例如 `cwd="/"` 由 BedFS 解析为 bed_home，再投影到当前 Executor。

`capabilities.workspace_mount` 只说明进程里是否存在 suite 的真实 `/workspace` mount，不表示 BedFS 是否可用。`workspace_view.mode` 报告实际进程视图：`mount`、`proot`、`pathshim` 或 `carrier`；`available=false` 与 `reason` 表示没有用户态 helper 通过启动探测。BedFS 的结构化路径映射是所有房型的基础能力。

## 五、结构化路径与命令文本

Hostel 解析 file API 的 `path`、命令的 `cwd` 等结构化字段，因此这些字段在所有房型都遵守 BedFS 语义。BedFS 先把 cwd 解析为 Carrier path；新进程由 isolation 投影到 Executor View，已启动的常驻 Shell 则持有启动时的 View，在执行用户命令前通过独立、带终态分帧的 shell 控制步骤切换目录。Web 层不构造 Executor path，也不把 `cd` 拼进用户命令；heredoc、多行脚本等命令文本保持原样。命令中的字面 `/tmp/x` 仍由实际进程 namespace 解释。

PRoot 或 pathshim 可用时，dorm/room 的命令字面 `/workspace/x` 及配置目标会指向对应 BedFS source；映射外绝对路径仍由 Carrier 进程视图解释。helper 全部失败时命令仍会启动，Hostel 通过能力与 diagnostics 接口如实上报 Carrier 降级和各项原始探测记录。

Dorm 与 carrier 共享 mount namespace，命令中的字面绝对路径可能成功写到进程根，而不是 BedFS。独占 carrier 可显式配置 `--dorm-read-fallback-root /`：只读 file API 在 BedFS 主映射不存在时，把客户端绝对路径按该进程根作为第二候选重试；两处都存在时始终以 BedFS 为准。相对路径不回退，因为它本来就以 bed workspace 为执行与 API 基准。

这是一条默认关闭的只读候选策略，不是第二套路径映射或写入语义。上传、替换、改权限、移动和删除始终只操作 BedFS；room / suite 也不启用回退。配置的 root 会暴露给 file API 读取，Dorm 本身又不提供数据访问屏障，因此共享 carrier 不得开启，也不得把它理解为隔离保证。

## 六、生命周期与边界

- Bed owns BedFS：`bed_home`、workspace、generation 与快照身份随 Bed 存续；
- Executor owns process realm：只持有 BedFS View，可丢失和替换；
- Shell owns its Executor View：session run 的结构化 cwd 由 Shell 投影并更新持久 cwd；
- Store consumes configured durable roots：快照始终包含 `meta.json`，并包含 `HOSTEL_PERSISTED_PATHS` 选择的 BedFS 子树（默认 `/workspace`）；
- isolation realizes View：不拥有数据命名和持久化规则；
- web 只选择与房型、部署配置匹配的 BedFS 读取策略：不能自行拼 carrier 路径或 mount point。

daemon 文件 API 先做客户端路径规范化，再以 `bed_home` 的目录句柄执行 descriptor-relative 文件操作。路径中的 symlink 只允许解析到该根之内；逃出根目录或与并发 symlink 替换竞态的操作会失败。这条安全边界属于 BedFS，不散落到各 handler。
