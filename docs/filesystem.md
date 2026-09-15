# Filesystem：BedFS、进程视图与文件隔离

通用挂载、Landlock、pathshim/PRoot 和 ptrace 机制位于 `internal/host/filesystem`；BedFS 路径语义、文件等级选择与领域内组合探测由 Bed Filesystem 负责。分层边界见 [核心架构](kernel.md#领域与-host-机制)。

> 本文区分文件系统的目标语义与当前兑现程度。跨领域隔离边界见 [isolation.md](isolation.md)，持久化与恢复见 `store.md`。

## 一、定位

Bed 在文件系统领域以 Rootfs、Workdir 和 PathMappings 提供类似 Container 的 API 体验：
拥有自己的根文件空间与默认工作目录，并显式接入 Carrier（Host/Pod）的数据。
这些语义先于具体隔离机制成立；产品背景见 [核心架构](kernel.md#一定位与目标)。

### Rootfs

每个 Bed 期望拥有自己的根文件视图：Bed A 的 `/` 与 Bed B 的 `/` 属于不同的路径空间，
各自的数据按 Bed 归属。`bed_home` 是每个 Bed 在 Carrier 上的数据落点；共享系统程序与
工具链不改变 Bed 的文件归属，也不意味着各 Bed 共享同一个根目录身份或私有可写数据。

当前 BedFS 已统一数据归属与结构化路径解析，进程侧仍按机制提供局部路径视图，尚未完整
兑现每 Bed rootfs。即使 private 档，也是在 Carrier 根视图上遮蔽和挂载指定路径，不能
据此宣称进程的 `/` 已整体成为 Bed 自己的根。数据路径解析规则见「三个路径空间」，
进程侧的具体保证见「Executor 视图」。

### Workdir

Workdir 是 Bed 的默认工作目录，当前为 `/workspace`，也是 API 相对路径的解析基准。
它是 Bed 根文件空间中的普通目录；具体进程的 cwd 可以覆盖默认值，常驻 shell 也可通过
`cd` 改变当前目录。Store 默认同步 `/workspace` 是独立策略，不由 Workdir 的身份推导。

### PathMappings

Bed 需要接入任务输入或与 Carrier 共享数据，因此通过 PathMappings 声明将 Carrier 已有
文件或目录接入指定 Bed 路径。Docker 将这类接入称为
[bind mount](https://docs.docker.com/engine/storage/bind-mounts/)；Hostel 使用 PathMapping
表达声明，使其不依赖是否具备真实 mount 能力。当前实现支持目录映射。

每项映射声明 HostPath、BedPath 和只读要求，数据的准备、归属与回收仍由提供方负责。
多个 Bed 可以显式接入同一份 Carrier 数据，这种共享不改变各自 rootfs 的身份。
`SyncPaths` 另行决定 Store 自动同步哪些 Bed 数据；路径映射本身不承诺持久化。

### API 体验与进程视图

设计目标是即使 Carrier 无法完整建立类似 Container 的进程文件视图，Bed 文件 API 仍尽力
完成操作。PathMapping 在进程中未生效，不应直接等同于对应数据在 API 层不可读取。

例如声明 `/volumes/project → /project`，映射未在所选进程视图中兑现时，调用
`read_file("/project/a.txt")` 先读取当前 Bed 数据根下的 `project/a.txt`，读取失败时再尝试
声明对应的 `/volumes/project/a.txt`。返回首个成功读取的结果，两次读取都受各自目录句柄的路径边界约束；
空映射占位目录不遮蔽源目录。BedPath 不直接作为 daemon 全局路径读取。

映射在进程视图中已兑现时，API 直接读取声明的源，保持与进程视图一致。本地候选数据会
保留以支持同一 Bed 的恢复。候选读取不合并多个目录树；写入与 Transfer 仍使用声明的
HostPath，API 始终检查 ReadOnly。进程侧未兑现只读时，不宣称外部数据已被进程只读保护。

Reader 根据所选文件视图的映射支持选择读取策略；这与独占 Carrier 的显式只读 fallback
分别处理，命中 PathMapping 的请求不会继续尝试全局 fallback root。具体进程根回退条件
见「结构化路径与命令文本」。

### 目标与兑现程度

| 层次 | 文件领域的含义 |
|---|---|
| 目标／声明 | Bed 自己的 rootfs，以及需要接入的 HostPath、BedPath 和只读要求 |
| Fact | Carrier 实际具备的内核能力、权限、工具与目录访问条件 |
| Feature | 根据事实和配置采用的机制，如 bwrap、PRoot、pathshim、Landlock 或 UID/DAC |
| Level 与实际状态 | 经组合验证后提供的隔离保证，以及路径视图、映射和只读要求的支持情况 |

路径视图的完整性与隔离强度分别报告：PRoot/pathshim 可以改善路径兼容性，但不提高安全
隔离 Level；具有访问控制也不代表进程拥有独立的 `/`。选中机制须通过实际组合验证，
不能仅凭工具存在或内核支持就宣称目标已兑现。

### 领域职责

`filesystem.Manager` 统一拥有 BedFS 与文件隔离机制，负责准备、释放和文件领域诊断。
数据模型、进程视图和访问屏障在同一领域内协作；跨领域的网络进入、身份切换与执行组合
由 Bed Manager 协调。能力不足时可以选择可用档位，已选机制执行失败则明确报错。

`BedFS` 是 Bed 持有的数据域，不是一次请求里的路径工具。Bed 创建时建立一个 BedFS；Executor 丢失、替换或重建时，BedFS 的身份与数据不变，只重新生成进程视图。

BedFS 统一拥有以下语义：

- `bed_home`：客户端 `/` 对应的数据根；
- Workdir：Bed 默认工作目录 `/workspace`，Carrier 数据路径为 `bed_home/workspace`；
- PathMappings：调用方为 Bed 声明的 `HostPath → BedPath` 共享数据映射；未命中时，结构化路径归属当前 Bed 的 `bed_home`；
- client path → carrier path：file API、cwd 等结构化路径的存放位置；
- carrier path → Executor path：不同隔离机制下进程应使用的路径；
- Bed 内文件操作、属主交接与 symlink 防逃逸边界。

隔离机制不再自行解释客户端路径。它只选择 BedFS 如何投影到 Executor，并负责兑现该视图所需的 bind、Landlock 或 uid 规则。

Bed 的理想语义是独立执行空间。BedFS 负责让文件归属与路径含义在所有房型一致；
isolation 根据环境能力尽量兑现进程侧的访问屏障。shared 没有安全墙，仍须完成逻辑分床
与路径映射；confined 增加访问控制，private 使用私有 mount 视图。降级不能改变同一 Client path
的主映射。Store 另行选择需要持久化的 BedFS 子树。

面向调用方的文件操作与传输 API 统一见 [文件操作与传输](transfers.md)。

## 二、文件隔离档位与机制

Filesystem 定义 `shared < confined < private`。外部 dorm/room/suite 分别映射为这三个文件等级的预期，
但外部房型还需结合 Privilege 和 Network；文件 private 不等于整个 Bed 达到 suite。请求档位与已知支持集合
共同决定候选档位，再经运行环境组合验证。文件档位不替网络、PID 或 CPU/内存边界作保证。

| 文件 Level | 进程侧的文件边界 | 当前机制 |
|---|---|---|
| shared | 逻辑分床，按 Carrier 进程权限访问，没有强制跨 Bed 屏障 | direct |
| confined | 限制跨 Bed 数据访问，但目录存在性及公共路径仍可见 | 优先 Landlock，其次 UID/DAC |
| private | 私有 mount 视图遮蔽兄弟 Bed，挂回自己的工作区和配置投影 | bwrap |

三档复用同一 BedFS 数据模型，机制不按档位逐层叠加：confined 在共享视图上增加访问控制，
private 改用私有 mount 视图。UID/DAC 依赖 Privilege 已选 dedicated 身份，不再由文件机制分配 UID。
Landlock/bwrap 同样可以组合 dedicated 身份；不能由文件 backend 反推身份策略。

结构化 file API 与 cwd 始终先解析到当前 BedFS，降级不改变同一客户端路径的数据落点。
PRoot/pathshim 尽量让命令中的工作区路径也指向 BedFS，但它们不是安全边界，不能抬高房型。
命令文本保持不透明，映射之外的绝对字面量由实际进程视图解释。

### 机制的取舍

**private 遮蔽后再挂回本 Bed**。只把 Carrier 根挂成只读仍允许读取邻居数据，因此先遮蔽
工作区父目录和存在的宿主敏感路径（如 `/root`、`/home`、`/run/secrets`、
`/var/run/secrets`），再挂回当前 BedFS；较低档位不能假定具有同样的遮蔽。工具链继续共享，`/usr/local`
保留 Carrier 级可写软件环境。当前 bwrap 每次启动的 `/tmp` 是独立 tmpfs，不能当作
跨命令持久的 Bed 数据；需要延续的数据应放在 BedFS 中。

bwrap 使用 user namespace 完成挂载准备，并绑定已有 `/proc`，以适应容器内受限的 procfs。
当前没有私有 PID namespace，因此私有文件视图不等于完整的进程不可见性。机制及参数顺序
锚点在 `internal/bed/filesystem/isolation/bwrap_args.go`；部署权限示例见
[deploy/k8s/README.md](../deploy/k8s/README.md)。

**confined 用访问控制兑现数据边界**。Landlock 在子进程中应用规则，UID/DAC 使用 Privilege 分配的身份和目录 ownership；
daemon 必须继续服务所有 Bed，不能对自身套用某个 Bed 的边界。两者都必须在
用户代码开始前生效，已有描述符和公共路径的可达性不能仅靠路径名称推断。

UID backend 以独立进程身份兑现 confined 的数据访问边界，目录属主、进程身份和 UID 租约必须保持
一致。具体的分配、重启恢复、失败清理和硬链接约束见 [privilege.md](privilege.md)；
`internal/bed/filesystem/isolation/uid_linux.go` 只负责选择该文件机制，不拥有通用身份生命周期。

## 三、三个路径空间

| 空间 | 示例 | 所有者 |
|---|---|---|
| Client | `/`、`/workspace/a`、`/tmp/job`、相对路径 `a` | BedFS；`/` 是 bed_home，相对路径以 workspace 为基准 |
| Carrier | `<workspace-root>/<bed-id>/data/...` | BedFS；daemon 的文件操作和 Store 使用 |
| Executor | shared/confined 下由 helper 或 Carrier 提供视图；private 下为内部 bed_home 挂载或 `/workspace` | BedFS `ProcessView` 定义语义，isolation 实现投影 |

映射规则只有一套：

| Client path | Carrier path |
|---|---|
| `/` | `<bed_home>` |
| `/workspace/a` | `<bed_home>/workspace/a` |
| `/tmp/job` | `<bed_home>/tmp/job` |
| `a` | `<bed_home>/workspace/a` |

上表描述当前 BedFS 的数据路径解析规则：路径先在当前 Bed 的客户端路径空间中规范化，
命中 PathMapping 时使用声明的 Carrier 目录，否则解析到该 Bed 自己的 `bed_home` 下。
返回路径使用对应的逆转换。`BedPath=/ → HostPath=<bed_home>` 只是这条默认解析规则的
简写：Bed A 和 Bed B 分别使用自己的 bed_home，不会修改 Carrier 全局的 `/`，也不表示
进程的 `/` 已整体替换为 bed_home。rootfs 的目标语义与这条数据落点规则应分别理解。

Bed 创建请求示例：

```json
{
  "id": "example",
  "path_mappings": [
    {"host_path": "/volumes/project", "bed_path": "/project"},
    {"host_path": "/volumes/reference", "bed_path": "/reference", "read_only": true}
  ],
  "sync_paths": ["/workspace", "/memory"]
}
```

`HostPath` 是 daemon 可见的已有目录，例如 Pod 级 PVC 挂载点；Hostel 不创建、接管属主或删除它。调用方负责授权来源目录、挂载和访问权限。额外映射在文件 API、command/session/Service 的结构化路径与执行视图中指向同一份数据；映射本身不声明自动持久化。执行后端只读取当前 BedFS 的映射，不再持有独立的 projection 声明或配置 Option。

额外 `BedPath` 必须绝对、非根，不能与其他映射、内置 `/workspace`、内核目录或内部挂载点重叠。`HostPath` 不得覆盖 Hostel 工作区或彼此重叠。映射保留目标位置原有的 BedFS 目录数据；进程映射生效时源目录优先，未生效时 API 可读取本地候选。映射不能与 SyncPaths 重叠。默认根由 Hostel 建立，不在 Spec 中重复提交。

文件 API 为每个选中目录持有独立的 `os.Root` 句柄，禁止 symlink 逃出该目录；删除或重命名映射根及其父目录会失败。跨映射 rename 不支持；Transfer 应显式选择一个映射目录，跨映射父目录的导入/导出会失败。删除 Bed 只删除默认根的数据，不触碰额外映射的目录。

## 四、Executor 视图

### shared / confined 文件

Executor 与 daemon 共享 mount namespace。Hostel 启动时从 `PATH` 发现 `proot`、`pathshim`，探测内置工作区路径视图，并按 PRoot → pathshim → Carrier 选择后端。额外 PathMappings 在 Bed 初始化时接入。Carrier 视图不能重定向进程路径，Landlock 当前也未允许外部数据进入其规则；这些组合保留 API 映射，报告进程映射不可用，仍允许 Bed 就绪。实际目录不存在或无法打开等准备错误仍会失败。

PRoot 的路径 syscall 覆盖更完整，但依赖 ptrace；pathshim 不依赖 ptrace，作为次选。两者都可用时选择 PRoot。Landlock 或 uid 始终独立负责访问边界，workspace helper 不参与 isolation level 判定。

仅展开文件视图与文件边界时，进程链保持以下职责顺序；网络及最终降权的组合约束见 [isolation.md](isolation.md)：

```text
shared: selected BedUser → proot / pathshim → command
confined (Landlock): selected BedUser → __confine → proot / pathshim → command
confined (UID): dedicated BedUser → proot / pathshim → command
private: selected BedUser → bwrap → command
```

PRoot 与 pathshim 将 Bed 的映射目录接入声明路径，没有 COW、whiteout 或每次调用的私有副本。多个 command/session 访问同一目录时，遵守底层文件系统的并发语义。它们提供用户态路径视图，不提供 mount namespace 或安全边界；当前只在访问边界允许时应用读写映射，只读映射的进程保护由 bwrap 提供。不支持的声明保留为 API 映射，helper 不把它当作已兑现的进程路径。

### private 文件

bwrap 先遮蔽 `<workspace-root>`，再投影同一 BedFS：

- 整个 `bed_home` bind 到机制私有路径 `/tmp/.hostel/bed`，使 `/`、`/tmp/job` 等任意结构化 cwd 都有进程视图；
- `bed_home/workspace` 额外 bind 到稳定的 `/workspace`，保持 OpenSandbox 与 agent 工具链约定；
- 每个 PathMapping 将已有 HostPath bind 到 BedPath；ReadOnly 使用只读 bind；
- BedFS 的 workspace 子树优先使用 `/workspace`，其余路径使用内部 bed_home 投影。

内部挂载点不是北向协议。调用方继续传 Client path；例如 `cwd="/"` 由 BedFS 解析为 bed_home，再投影到当前 Executor。

`capabilities.workspace_mount` 只说明进程里是否存在 private 文件视图的真实 `/workspace` mount，不表示整个 Bed 为 suite 或 BedFS 是否可用。内部 `ProcessView` 负责 Carrier 数据路径到进程路径的转换，不单独拥有文件数据。HTTP 保留 `workspace_view` 字段名；`workspace_view.path_mappings` 分别报告进程侧读写映射与只读映射支持，二者独立于 file API 的可用性。`workspace_view.mode` 报告实际进程视图：`mount`、`proot`、`pathshim` 或 `carrier`；`available=false` 与 `reason` 表示没有用户态 helper 通过选择验证。BedFS 的结构化路径映射是所有房型的基础能力。

## 五、结构化路径与命令文本

Hostel 解析 file API 的 `path`、命令的 `cwd` 等结构化字段，因此这些字段在所有房型都遵守 BedFS 语义。BedFS 先把 cwd 解析为 Carrier path；新进程由 isolation 投影到 Executor View，已启动的常驻 Shell 则持有启动时的 View，在执行用户命令前通过独立、带终态分帧的 shell 控制步骤切换目录。Web 层不构造 Executor path，也不把 `cd` 拼进用户命令；heredoc、多行脚本等命令文本保持原样。命令中的字面 `/tmp/x` 仍由实际进程 namespace 解释。

PRoot 或 pathshim 可用时，shared/confined 文件视图的命令字面 `/workspace/x` 及配置目标会指向对应 BedFS source；映射外绝对路径仍由 Carrier 进程视图解释。helper 全部失败时可尝试 Carrier 视图，Hostel 通过能力与 diagnostics 接口如实上报降级和各项原始探测记录；完整组合仍须可执行。

shared 文件视图与 carrier 共享 mount namespace，命令中的字面绝对路径可能成功写到进程根，而不是 BedFS。独占 carrier 可显式配置沿用原名称的 `--dorm-read-fallback-root /`：只读 file API 在 未命中额外映射且 BedFS 路径不存在时，把客户端绝对路径按该进程根作为第二候选重试；两处都存在时始终以 BedFS 为准。相对路径不回退，因为它本来就以 bed workspace 为执行与 API 基准。

这是一条默认关闭的只读候选策略，不是第二套路径映射或写入语义。上传、替换、改权限、移动和删除始终只操作 BedFS；confined/private 文件也不启用回退。配置的 root 会暴露给 file API 读取，因此共享 carrier 不得开启，也不得把它理解为隔离保证。

## 六、生命周期与边界

- Bed owns BedFS：`bed_home`、workspace、generation 与快照身份随 Bed 存续；
- Executor owns process realm：只持有 BedFS View，可丢失和替换；
- Shell owns its Executor View：session run 的结构化 cwd 由 Shell 投影并更新持久 cwd；
- Store consumes Bed SyncPaths：快照始终包含 `meta.json`，并包含 `Bed.Spec.SyncPaths` 选择的默认映射子树（默认 `/workspace`）；
- isolation realizes View：不拥有数据命名和持久化规则；
- web 只选择与房型、部署配置匹配的 BedFS 读取策略：不能自行拼 carrier 路径或 mount point。

daemon 文件 API 先做客户端路径规范化，再以选中映射的目录句柄执行 descriptor-relative 文件操作。路径中的 symlink 只允许解析到该根之内；逃出根目录或与并发 symlink 替换竞态的操作会失败。这条安全边界属于 BedFS，不散落到各 handler。
