# BedFS：Bed 的文件系统语义

> 状态：当前实现。数据隔离强度见 `data.md`，持久化与恢复见 `store.md`。

## 一、定位

`BedFS` 是 Bed 持有的数据域，不是一次请求里的路径工具。Bed 创建时建立一个 BedFS；Executor 丢失、替换或重建时，BedFS 的身份与数据不变，只重新生成进程视图。

BedFS 统一拥有以下语义：

- `bed_home`：客户端 `/` 对应的数据根；
- workspace：`bed_home/workspace`，客户端与进程的规范路径为 `/workspace`；
- client path → carrier path：file API、cwd 等结构化路径的存放位置；
- carrier path → Executor path：不同隔离机制下进程应使用的路径；
- Bed 内文件操作、属主交接与 symlink 防逃逸边界。

隔离机制不再自行解释客户端路径。它只选择 BedFS 如何投影到 Executor，并负责兑现该视图所需的 bind、Landlock 或 uid 规则。

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

Executor 与 daemon 共享 mount namespace。BedFS 的 carrier 路径就是进程路径；Landlock 或 uid 负责限制进程能访问的范围。

### suite

bwrap 先遮蔽 `<workspace-root>`，再投影同一 BedFS：

- 整个 `bed_home` bind 到机制私有路径 `/tmp/.hostel/bed`，使 `/`、`/tmp/job` 等任意结构化 cwd 都有进程视图；
- `bed_home/workspace` 额外 bind 到稳定的 `/workspace`，保持 OpenSandbox 与 agent 工具链约定；
- BedFS 的 workspace 子树优先使用 `/workspace`，其余路径使用内部 bed_home 投影。

内部挂载点不是北向协议。调用方继续传 Client path；例如 `cwd="/"` 由 BedFS 解析为 bed_home，再投影到当前 Executor。

`capabilities.workspace_mount` 只说明进程里是否存在规范 `/workspace` bind，不表示 BedFS 是否可用。BedFS 的结构化路径映射是所有房型的基础能力。

## 四、结构化路径与命令文本

Hostel 解析 file API 的 `path`、命令的 `cwd` 等结构化字段，因此这些字段在所有房型都遵守 BedFS 语义。Hostel 不改写任意 shell 文本：命令中的字面 `/tmp/x` 仍由实际进程 namespace 解释。

不要给所有操作增加隐式“BedFS 不存在就读 carrier 绝对路径”的回退：读、写、删除与执行的安全含义不同，统一回退会把拼写错误变成越过 Bed 边界。后续若有真实需求，应在 BedFS 中增加显式、按操作分类的解析策略。

## 五、生命周期与边界

- Bed owns BedFS：`bed_home`、workspace、generation 与快照身份随 Bed 存续；
- Executor owns process realm：只持有 BedFS View，可丢失和替换；
- Store consumes BedFS carrier data：快照对象仍是 Bed 目录中的 `data/`；
- isolation realizes View：不拥有数据命名和持久化规则；
- web 只做协议适配：不能拼 carrier 路径或 mount point。

daemon 文件 API 先做客户端路径规范化，再以 `bed_home` 的目录句柄执行 descriptor-relative 文件操作。路径中的 symlink 只允许解析到该根之内；逃出根目录或与并发 symlink 替换竞态的操作会失败。这条安全边界属于 BedFS，不散落到各 handler。
