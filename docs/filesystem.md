# BedFS：Bed 的文件系统语义

> 状态：当前实现。数据隔离强度见 `data.md`，持久化与恢复见 `store.md`。

## 一、定位

`BedFS` 是 Bed 持有的数据域，不是一次请求里的路径工具。Bed 创建时建立一个 BedFS；Executor 丢失、替换或重建时，BedFS 的身份与数据不变，只重新生成进程视图。

BedFS 统一拥有以下语义：

- `bed_home`：客户端 `/` 对应的数据根；
- workspace：`bed_home/workspace`，客户端与进程的规范路径为 `/workspace`；
- path projections：调用方可配置多个 `BedFS path → Executor path`，Hostel 不解释其业务含义；
- client path → carrier path：file API、cwd 等结构化路径的存放位置；
- carrier path → Executor path：不同隔离机制下进程应使用的路径；
- Bed 内文件操作、属主交接与 symlink 防逃逸边界。

隔离机制不再自行解释客户端路径。它只选择 BedFS 如何投影到 Executor，并负责兑现该视图所需的 bind、Landlock 或 uid 规则。

Bedbox 向 caller 提供的契约是一个 Bed 独占整个 Pod；BedFS 负责让这份路径契约在三档下保持同义。isolation 只决定内核是否真正阻止进程越过该视图：不能把 suite/room/dorm 的可见性或权限差异写进 Client → Carrier 的映射规则。

BedFS 也是三档共同的 best-effort 数据底座：Dorm 没有安全墙，仍须完成逻辑分床与路径映射；Room 沿用 Dorm 的共享 mount view，并叠加访问控制；Suite 改用私有 mount view，直接让其他 Bed 的路径不可见，不再经过 Room 的权限判断。Store 的 `HOSTEL_PERSISTED_PATHS` 默认仅 `/workspace`，其他 BedFS 路径保持运行期语义。降级只能减少安全保证，不能降掉 BedFS 的正确性。

## 二、三个路径空间

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

## 三、Executor 视图

### dorm / room

Executor 与 daemon 共享 mount namespace。Hostel 启动时会通过选中的隔离机制探测 pathshim；探测集合包含内置 `bed_home/workspace → /workspace` 和 `HOSTEL_PROJECTED_PATHS` 声明的全部投影。前者与配置项是同一种 `BedFS source → Executor target` 投影，只因 `/workspace` 是 Hostel 基础契约而内置，不在 env 中重复声明；配置项只承载额外投影。完整集合通过后整组启用；任一项不可用时整组退回 Carrier 语义，禁止部分生效。Landlock 或 uid 仍独立负责访问边界，pathshim 不参与 isolation level 判定。

进程链保持职责顺序：

```text
dorm: pathshim → command
room: __confine / __asuser → pathshim → command
suite: bwrap → command
```

pathshim 对每项 projection 使用 replace bind：目标内受支持的文件系统操作只落到对应 BedFS source，不读取 Carrier 原目标；它没有 COW、whiteout 或 invocation 私有状态，因此多个 command/session 共享同一 source 时并发语义就是普通底层文件系统并发。它是 syscall 覆盖不完整的 best effort 兼容层，不是 mount、安全边界或完整 guest root。

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

`capabilities.workspace_mount` 只说明进程里是否存在 suite 的真实 `/workspace` mount，不表示 BedFS 是否可用。`workspace_view.mode` 报告实际进程视图：`mount`、`pathshim` 或 `carrier`；`available=false` 与 `reason` 表示配置了 pathshim 但启动探测没有通过。BedFS 的结构化路径映射是所有房型的基础能力。

## 四、结构化路径与命令文本

Hostel 解析 file API 的 `path`、命令的 `cwd` 等结构化字段，因此这些字段在所有房型都遵守 BedFS 语义。BedFS 先把 cwd 解析为 Carrier path；新进程由 isolation 投影到 Executor View，已启动的常驻 Shell 则持有启动时的 View，在执行用户命令前通过独立、带终态分帧的 shell 控制步骤切换目录。Web 层不构造 Executor path，也不把 `cd` 拼进用户命令；heredoc、多行脚本等命令文本保持原样。命令中的字面 `/tmp/x` 仍由实际进程 namespace 解释。

pathshim 可用时，dorm/room 的命令字面 `/workspace/x` 及配置目标会尽力指向对应 BedFS source；映射外绝对路径仍由 Carrier 进程视图解释。pathshim 降级时命令仍会启动，Hostel 在启动探测阶段关闭整组进程视图并通过能力接口如实上报。

Dorm 与 carrier 共享 mount namespace，命令中的字面绝对路径可能成功写到进程根，而不是 BedFS。独占 carrier 可显式配置 `--dorm-read-fallback-root /`：只读 file API 在 BedFS 主映射不存在时，把客户端绝对路径按该进程根作为第二候选重试；两处都存在时始终以 BedFS 为准。相对路径不回退，因为它本来就以 bed workspace 为执行与 API 基准。

这是一条默认关闭的只读候选策略，不是第二套路径映射或写入语义。上传、替换、改权限、移动和删除始终只操作 BedFS；room / suite 也不启用回退。配置的 root 会暴露给 file API 读取，Dorm 本身又不提供数据访问屏障，因此共享 carrier 不得开启，也不得把它理解为隔离保证。

## 五、生命周期与边界

- Bed owns BedFS：`bed_home`、workspace、generation 与快照身份随 Bed 存续；
- Executor owns process realm：只持有 BedFS View，可丢失和替换；
- Shell owns its Executor View：session run 的结构化 cwd 由 Shell 投影并更新持久 cwd；
- Store consumes configured durable roots：快照始终包含 `meta.json`，并包含 `HOSTEL_PERSISTED_PATHS` 选择的 BedFS 子树（默认 `/workspace`）；
- isolation realizes View：不拥有数据命名和持久化规则；
- web 只选择与房型、部署配置匹配的 BedFS 读取策略：不能自行拼 carrier 路径或 mount point。

daemon 文件 API 先做客户端路径规范化，再以 `bed_home` 的目录句柄执行 descriptor-relative 文件操作。路径中的 symlink 只允许解析到该根之内；逃出根目录或与并发 symlink 替换竞态的操作会失败。这条安全边界属于 BedFS，不散落到各 handler。
