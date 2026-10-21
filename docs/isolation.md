# Bed 隔离：目标、兑现与实际边界

## 隔离语义

### 隔离目标

Bed 的目标是独立的文件、进程、网络和资源空间。当前面向可信或半可信代码，按宿主能力
兑现边界并披露缺口；共享工具链和设施降低成本，其跨 Bed 访问与故障范围分别评估。
工作负载模型见 [kernel.md](kernel.md)，具体数据和执行契约见 [volumes.md](volumes.md)、
[pillow.md](pillow.md)。实际保证取决于机制及组合验证，不承诺 VM 等价的对抗性代码边界。

### 控制面与运行边界

Bed ID、header 和 query 参数用于选择资源，不是调用方的身份凭据。HTTP 控制面可以操作
多个 Bed，入口认证与 Bed 访问授权由接入方负责；房型和 netns 不会自动补上这层授权。
同理，Bed 级代理 token 不能替代 token 下发接口的访问控制。共享设施的原始端口与管理
接口是否可达也须纳入实际边界，不能只看用户命令的 namespace。

### 三层含义

| 层次 | 回答的问题 | 例子 |
|---|---|---|
| 目标语义 | Bed 应当拥有什么独立性 | 文件归自己，网络和进程不干扰邻居 |
| 兑现机制 | 当前环境如何尽量做到 | BedFS、mount namespace、Landlock、UID、netns、cgroup |
| 实际保证 | 调用方现在能依赖什么 | 文件访问受限，但仍共享 PID 视图；命令有独立 netns，但浏览器出站仍共享 |

Bed 是跨机制不变的单元；Executor 是它当前可替换的进程承载域；Execution 是一次运行。
这些身份及主流程见 [kernel.md](kernel.md)。房型和 backend 描述兑现方式或程度，不产生
另一种 Bed，也不能反过来改变 BedFS 的文件归属和路径语义。

### 对外房型与领域等级

房型是以文件隔离保证为代表特征的用户分档，将多种能力组合概括为 dorm/room/suite。
Hostel 常在已有 Pod 内运行，Bed 的核心需求是独立工作空间；房型按实际文件保证确定，
网络、进程、身份和资源隔离在此基础上尽量增强，各 Component 分别披露实际边界。

| 房型 | 文件最低保证 |
|---|---|
| dorm | shared：文件按 Bed 组织，进程侧共享访问边界 |
| room | confined：阻止跨 Bed 文件访问，可以共享路径视图 |
| suite | private：独立文件视图，并限制跨 Bed 文件访问 |

文件等级由 Filesystem 根据机制探测和实际组合验证确定，房型在用户请求档位内映射该等级。
显式 Volumes/Mounts 表达各执行方允许共享的数据；PRoot/pathshim 只改善路径兼容性，不提高隔离等级。
共享身份、网络或 PID namespace 不降低已兑现的文件房型，也不能提高弱文件边界的房型。
例如 private 文件与共享网络、private 文件与独立网络，都可以满足 suite。

房型同时提供默认能力偏好：room/suite 尽力使用独立身份，suite/auto 尽力使用独立网络；
PID namespace 等能力按各自策略选择。`auto` 请求最高文件档位，环境不足时按实际保证降级。
组件拥有自己的能力类型、支持集合、选择和失败原因；统一 Component 契约只组合生命周期与
类型化诊断，不要求不同领域映射到房型。`auto/off/required` 的选择规则见下文。

### 各维度的目标与当前兑现程度

| 维度 | 理想状态 | 当前能力与缺口 |
|---|---|---|
| 文件与路径 | 每个 Bed 使用自己的文件空间，不能读写邻居数据 | BedFS 统一结构化路径；Filesystem Level 决定进程侧的访问屏障；用户态投影只改善路径体验 |
| 进程与身份 | 执行独立、可完整回收，不能观察或干扰邻居进程 | daemon 保留管理权限，BedUser 统一约束命令身份；Executor/supervisor 管理进程归属与回收；可独立要求 Executor 私有 PID namespace/procfs；未要求时共享 carrier 进程视图，suite 本身不承诺进程不可见 |
| 网络 | 每个 Bed 有独立网络空间，并受自己的出站约束 | 可选 netns 与按 Bed 配置的出站策略覆盖命令和 shell；不可用时共享 Carrier；共享设施出站尚未纳入该边界 |
| CPU / 内存 | 一个 Bed 的失控负载不挤占或拖垮邻居 | Carrier 准入与可选 per-bed cgroup 记账已有；per-bed 硬限额尚未实现 |
| 环境与凭据 | Bed 只接触属于自己或明确共享的配置、凭据 | 过滤 Hostel 保留命名空间；其余 Carrier 环境默认继承，部署方负责其中的敏感信息 |
| 软件与设施 | Bed 的状态与使用互不影响，同时复用重资产 | Carrier 软件环境共享；Chromium 按 BrowserContext、MCP 按配置和会话切分，仍共享承载进程与部分故障边界 |

Store 管持久化与恢复，和这些能力协作，但不提供隔离屏障。路径属于 Bed、能否被邻居访问、
是否进入快照是三个独立问题；增加 path projection 不会自动扩大持久化范围。

## 能力选择与工具策略

- Component 拥有领域行为、生命周期、配置和状态。
- 组件拥有自己的能力类型与支持集合，结合 Config 选择机制，并在类型化 Status 中报告实际结果。
- 房型由文件隔离保证决定，其他组件的能力分别报告。
- Tool 是组件为兑现 Bed 能力而选择、组合的工具，既包括外部程序，也包括内核机制和身份技术，例如 Filesystem 的 Bwrap、Landlock、UID、PRoot、Pathshim，
  Network 的 NetNS，以及 Resource 的 cgroup accounting。
- Requirements 是使用该 Tool 的环境前提，包含 Linux Capabilities、外部程序和系统条件。
  CAP_NET_ADMIN、CAP_SYS_ADMIN 保留 Linux Capability 的含义。

Tool 使用统一的 Policy：

| Policy | 语义 |
|---|---|
| auto | 允许探测和选择；可用不代表一定被选择，不可用按组件原有契约降级 |
| off | 排除该工具，不执行其运行探测，不创建其运行资源 |
| required | 必须可用且实际采用，不允许其他工具替代 |

`--isolation` / `HOSTEL_ISOLATION` 和内部 `BedOptions.RoomType` 设置实例的文件房型目标和默认增强偏好。
这是实例级配置，Filesystem 的内部预期等级由房型推导。dorm 请求 shared 文件、shared 身份、shared 网络；
room 请求 confined 文件，偏好 dedicated 身份、shared 网络；suite/auto 请求 private 文件，偏好 dedicated 身份、private 网络。
房型是否达标只取决于文件保证；身份和网络偏好未兑现时由各组件解释原因。
Tool Policy 控制采用哪个实现。例如 room、UID=required 要求使用 UID/DAC 文件机制。互斥功能同时 required，
或房型与 required 实现冲突，在探测前拒绝配置。Required process path helper 会排除互斥的 Bwrap 挂载视图。

基础 BedFS 路径归属和必要的执行降权没有通用关闭开关。关闭可选隔离不绕过这些契约。
Requirements 由实现声明，caller 不给环境前提设置 Policy；实际准备顺序和依赖校验由所属组件负责。
共享 `internal/bed/tool` 包只提供策略和观测类型，不执行通用依赖调度，也不新增工具箱 Manager。
各领域 Manager 根据 Bed 的需求和宿主条件，从工具箱中选择并组合工具；工具更多不代表每次全部启用，也不自动提供更高的隔离保证。

### 启动、失败与观测

启动入口合并配置并校验冲突，各组件按策略探测、选择和准备，再以临时 Bed 验证 command/session/Pillow 主进程的组合。
组合失败时先保留文件保证，依次尝试可选进程、网络和身份组合；耗尽后才更换文件候选，并恢复原始增强偏好重新探测。
整体选择、单次组合执行与清理都有独立的有界预算；候选耗尽或选择预算耗尽则失败。
Required 功能启动时无法满足就启动失败；清理失败也终止回退，保留资源 owner 和错误，不继续尝试下一组。
启动后具体 Bed 准备失败则该 Bed 不进入 Ready。已经采用的隔离实现执行失败，不能静默降级或放开边界。
组件准备与失败清理继续走同一生命周期。

实例状态按组件报告 Tool 的最终 Policy、Requirements、探测状态、是否选中及原因，原有执行探测
证据继续保留，JSON 字段为 `tools`。off 报告 disabled_by_config 和 not_probed；auto 工具可以探测成功却因优先级未被选中。
Requirements 是声明，不能仅凭权限位或工具存在就声称可用，执行探测仍是最终证据。
Filesystem 的 ceiling 表达已探测候选的最高可用档位；禁用候选后不能用它推断未探测的物理环境上限。
房型变低不删除已探测出的支持等级；Tool off 仍不执行探测，所以 Supported 是已知支持集合，不是未经探测的能力推断。
状态读取不触发探测或分配。

## 权限与执行身份

### Daemon 与 BedUser

官方镜像默认以 root 启动 daemon，使它能跨 Bed 准备目录、切换进程身份并回收不同 UID 的进程。
配置的建议 `BedUser` 拒绝 UID/GID 0。身份切换不是启动前提：建议身份缺少跨 UID 管理能力或探测失败时，可沿用 daemon
身份；继承 UID 0 时必须实际证明子进程的 active 与 bounding capability 集合均为空、`NoNewPrivs=1`。
因此 root + drop ALL 有可工作的弱基线路径，但不能保证任意部分 capability 组合都可用。
Hostel 不尝试获取缺失权限，也不把 UID 0 等同于具备某个 capability。

每个 resident Bed 在初始化时解析一个 `BedUser`，目录属主和 command/session 的最终身份共用同一个值：

- **shared / fixed**：所有 Bed 共用身份。`HOSTEL_BED_UID/GID`、`--bed-uid/gid` 与 Go 配置提供建议值；可用时优先采用，
  不可用时尝试经过完整权限与文件访问探测的 daemon 身份。未配置建议值时，root daemon 具备完整跨 UID 管理能力便优先使用 `1000:1000`。
  降级原因与实际 UID/GID 通过 privilege 状态和启动日志披露；非法建议值、继承身份验证失败或探测清理失败仍终止启动。
- **dedicated / per_bed**：room/suite 期望每个 Bed 独立的高位 UID/GID，不依赖采用的是 bwrap、Landlock 还是 UID/DAC。
  支持性由 Privilege 独立探测；Filesystem 的 UID/DAC 只有在已选 dedicated 身份时才可采用。相同 Bed ID 从稳定槽位开始，冲突时
  在预留范围内寻找空闲值，不能因散列碰撞让两个 Bed 共享身份。

per-Bed UID 是本地数据身份的一部分。Bed Manager 识别本地目录及 `.gc-*` 清理占位，
通过绑定到本地身份的 Recover / Prepare / Forget 驱动 Privilege Manager；权限组件不推测磁盘目录布局。
启动先根据目录 owner 恢复租约，资源与本地目录全部清理后才调用 Forget。luggage 和失败清理继续
保留 UID，具体重试与准入契约见 [Bed 生命周期](bed.md#组件参与生命周期)。
UID 范围须由部署侧预留，避免与宿主账号或 user namespace 映射重叠。

Bed 数据交接给新属主时跳过多硬链接的普通文件，防止改变 Bed 外共享 inode 的 owner；Linux 部署应
保持 `fs.protected_hardlinks=1`。BedFS 在 daemon 通过 file API 新建文件后继续把 owner 交还给同一
`BedUser`。

### 特权操作顺序

Bed 初始化先恢复或创建 BedFS，再解析并准备 `BedUser`，随后获取网络、文件边界和 Executor 所需的
资源；全部完成后才发布 Ready。command、session 和 Pillow 主进程统一通过 `manager.Environment` 组装执行环境：

```text
Bed Manager 协调各组件准备 BedFS、身份、namespace 与资源限制
  → 准备完成后发布 Ready
  → 执行入口进入已准备的环境
  → 切换到 BedUser，清空 capability 集合、设置 no_new_privs
  → 应用用户环境与 cwd，启动 command / session / Pillow 主进程
```

资源准备和降级决策属于 Bed Manager 及其协调的组件，不属于 Executor。Hostel 优先使用实际拥有的
权限完成准备，权限不足才尝试无特权机制。最终 BedUser 的低权限不应反过来限制 daemon 的准备能力。

rootful bwrap 在 Bed 初始化时准备 mount namespace，Filesystem 持有 namespace/root 句柄；准备
进程退出后这些句柄仍保留视图。command、Session 和主进程进入所属 Pillow 的卷挂载视图，
并最终降权；普通执行不重复准备挂载。释放顺序是先停止所有执行，再关闭句柄。Bed 的视图模板
在 Executor 替换时保留；若 Executor 要求私有 PID namespace，则重新派生匹配 procfs 的最终视图，
见 [Filesystem](volumes.md#进程视图与访问保证)。

受信任的进入程序使用静态 Hostel 二进制，不从 Bed rootfs 加载 helper 或动态库。用户环境作为不参与
程序初始化的载荷传递，降权后才交给用户程序，防止 LD_PRELOAD、PATH 等影响特权准备。
rootful 准备要求静态构建（`CGO_ENABLED=0`，正式镜像已使用）；还须通过真实准备和完整 Bed 执行探测。

没有所需宿主权限时，bwrap 保留降权后使用 user namespace 的路径；Landlock、PRoot、pathshim
仍在 Bed 身份下应用。任何已选机制执行失败都应终止本次初始化或执行，不能在运行途中静默放开边界。

回收时 daemon 先撤销数据面并停止 Bed 的进程，再清理设施、资源、网络和本地目录。跨 UID 终止进程
和修改目录需要的权限必须保留到清理完成；清理失败时资源 owner 与 UID 租约继续保留，供后续重试。

### 能力与部署条件

所有 Linux Bed command/session/Pillow 主进程使用 `bedinit` 的身份初始化步骤，不再依赖外部
`setpriv`。即使 BedUser 与 daemon 相同，也清空进程 capability 并设置 `no_new_privs`；有
CAP_SETPCAP 时清空 bounding set，没有时仅允许非 root 身份保留 ceiling，或 root 的 ceiling
已经为空。有 CAP_SETGID 时清除 supplementary groups。启动探测核对实际身份和 capability，
不能仅凭 wrapper 参数声称降权成功。

用户环境在整个 `bedinit` 链上作为不生效的载荷传递；最终执行步骤在边界及降权完成后
才恢复环境、切换 cwd、按 Bed 内 PATH 查找程序。rootful 先进入准备好的 namespace 再降权；
无特权机制先降权再进入视图。两条路径均不让用户环境参与特权 helper 的初始化。

当 Bed 身份与 daemon 不同时，Hostel 还需要以下 Linux capability 完成目录交接、身份切换、
进程监督和最终 capability 清理：

| Capability | 用途 |
| --- | --- |
| `CAP_CHOWN` | 把 BedFS 交给最终 BedUser |
| `CAP_DAC_OVERRIDE` | daemon 跨 Bed 检查、更新和清理数据 |
| `CAP_FOWNER` | 管理非 daemon 属主的文件元数据 |
| `CAP_KILL` | 停止不同 UID 的 Bed 进程 |
| `CAP_SETGID` | 设置 Bed GID 与 supplementary groups |
| `CAP_SETUID` | 设置 Bed UID |
| `CAP_SETPCAP` | 管理最终进程的 capability 集合 |

`drop: ["ALL"]` 会移除包括 `CAP_KILL`、`CAP_SETPCAP` 在内的默认 capability。只有希望使用跨 UID 管理时，
才需按 `/v1/status.components.privilege.requirements.capabilities` 的实际结果配置；不是 Hostel 启动的统一前提。
daemon 与 BedUser 相同时，这组身份切换要求为空，子进程凭据验证仍不可省略。

隔离和进程视图在 Bed identity 基础上还有各自的运行条件。下表区分实际操作要求与容器中常见的
放行方式；最终以机制 smoke 和 Bed 完整组合探测为准，不能仅凭某个 capability 或 profile 判断可用。

| 能力 | 实际操作要求 | 容器中常见的放行方式 | 缺失后的行为 |
| --- | --- | --- | --- |
| shared 文件 / direct | Bed identity 前提满足，无额外文件机制要求 | 继承身份基线不要求身份切换权限 | 作为文件隔离最低档继续运行 |
| confined 文件 / Landlock | 内核提供 Landlock LSM，允许所需 syscall | seccomp/LSM 允许操作，无额外 capability | 继续尝试 UID/DAC，最终可降至 shared 文件 |
| confined 文件 / UID | 完整 Bed identity capability；允许 setgroups/setgid/setuid/chown，并预留 UID 段 | 配置身份切换 capability 和允许相关操作的 seccomp/LSM 策略 | 不选择 UID/DAC |
| private 文件 / bwrap | 优先使用已有 SYS_ADMIN、SYS_CHROOT 和身份切换权限准备 mount namespace；否则要求无特权 user namespace | 两种路径均须允许实际 namespace/mount 操作，seccomp/LSM 可继续拒绝 | 完整探测后选择可用路径或较低文件等级 |
| PRoot workspace view | PRoot 可执行，允许其跟踪 Bed 子进程，ptrace 与 PRoot smoke 成功 | 按实际阻断调整 seccomp/LSM；部分容器策略可通过声明 `CAP_SYS_PTRACE` 放行，但它不是通用必需条件 | 继续尝试 pathshim 或 Carrier view |
| per-Bed netns | named netns 创建/进入、veth、route、nft 可用；`ip`、`nft` 可执行且 IPv4 forwarding 已开启 | 当前 backend 需要 `CAP_SYS_ADMIN`、`CAP_NET_ADMIN`，seccomp/LSM 也须允许相关操作 | 启动探测失败时使用共享 Carrier 网络 |

`CAP_NET_ADMIN` 单独不能完成当前 named netns backend；bwrap 可使用宿主已有权限或 user namespace
完成 mount 准备，不统一要求部署授予宿主级 `CAP_SYS_ADMIN`；可选 private 网络另有 named netns 的权限要求。各机制的隔离语义分别见
[volumes.md](volumes.md) 和 [network.md](network.md)。

部署的 admission、securityContext 与节点实际能力分别验证，RBAC 通过不代表运行时权限可用。
具体模板和排障步骤见 [Kubernetes examples](../deploy/k8s/README.md)。

## 机制组合与生命周期

### 所有权

Bed Manager 组合各领域能力，拥有准备、发布、撤销与回收的协调权。Filesystem 管文件边界
与 BedFS 进程视图，Privilege 管身份，Network 管 Bed 网络，Executor 管进程承载域，
Resource 管资源记账与准入。各领域持有并清理自己的资源。

Bed 初始化、回收与失败重试见 [bed.md](bed.md)，Executor 替换见 [pillow.md](pillow.md)。
文件机制与路径语义见 [volumes.md](volumes.md)，本篇只约束这些能力组合后的隔离保证。

### 组合必须守住的约束

- **准备完成才可服务**：不能把半恢复目录或尚未分配的必需网络当成 Ready。
- **特权步骤先于最终降权**：网络进入、文件视图准备和用户身份切换须按依赖顺序完成，
  再把控制权交给用户命令。任何单一组件都不能提前丢掉后续准备仍需要的能力。
- **同一 Bed 的执行入口一致**：一次性命令、常驻 shell 和 Pillow 主进程应使用同一套能力组合；共享设施
  没有进入该进程树时，必须单独说明其边界，不能自动继承 Bed 命令的隔离声明。
- **释放中的资源不再服务新执行**：失败清理仍需有人负责，但不能因此继续被视为可用。
  同 ID 的重新初始化不得复用残缺资源，也不得被上一轮清理回收。

`manager.Environment` 绑定一个 resident Bed 的文件视图、具体网络 allocation 和最终
`BedUser`，命令、shell 和 Pillow 主进程都通过它组装。Bed Manager 协调资源准备；rootful 文件视图在 Bed
初始化时建立，启用私有 PID namespace 时由每个 Executor 派生并持有最终文件视图；执行时只进入已准备的网络和文件环境，再切换身份、清除 capability 并运行用户程序。
无特权文件机制在 Bed 身份下应用；Executor 不负责选择、准备或降级资源。BedUser 的选择、UID
租约与 capability 要求见下文权限章节。Store、Network 和
Executor 仍各自提供能力，Bed 协调其生命周期。共享 Chromium 位于 Bed 进程树之外，不从某个
BedUser 派生运行身份。

## 共享边界

### 软件共享与敏感信息

Carrier 共享工具链、解释器和全局安装的软件。局部版本冲突优先在 BedFS 内使用 venv、
conda、本地 node_modules 等生态机制解决。共享软件的更改可能影响其他 Bed；需要独占
整个软件环境时由上层选择更强的 runtime。这个成本取舍不改变用户文件与产物按 Bed 归属
的目标，也不意味着当前所有共享路径都已受到租户级保护。

文件视图与进程环境分别控制可见性。文件遮蔽及共享软件目录见 [volumes.md](volumes.md#进程视图与访问保证)，
Carrier 环境继承与配置来源见 [pillow.md](pillow.md#配置来源)。环境变量过滤不能阻止经 /proc
观察同身份进程；设施凭据和代理协议边界见 [amenity.md](amenity.md)。

### 网络与资源独立演进

网络由 Network 按默认偏好和显式策略选择，不从实际文件 backend 推导。suite/auto 尽力使用 private 网络；dorm/room 默认 shared。
实例探测 netns 可用且实际选用时为 Bed 分配网络，命令、shell 和 Pillow 主进程共用，
Executor 替换不改变该 resident Bed 的网络身份；能力不可用时共享 Carrier 网络。
当前 netns 作用域是 Bed 进程，Chromium/MCP 的出站仍来自 Carrier。BrowserContext
切分浏览器状态，不构成 OS 网络边界；机制、DNS、诊断与出站约束见 [network.md](network.md)。

资源记账回答“谁用了多少”，准入回答“是否继续接纳”，硬限额才约束已接纳的负载。
当前具备前两者不等于已经避免吵闹邻居，per-bed 硬限额仍是缺口。详见
[resource.md](resource.md)。这些维度沿同一个 Bed 隔离目标发展，分别披露兑现程度。

## 能力披露与验证

`/healthz.isolation` 和 `/v1/status.isolation` 报告文件保证对应的房型的 requested/effective/reasons；
`/v1/status.components.filesystem` 报告 shared/confined/private 文件等级、上限、机制及视图；
Privilege 与 Network 分别报告自己的 supported 和最终选择结果；启动组合失败原因通过日志排查。
状态 schema 见 [observability.md](observability.md)；文件机制从 `components.filesystem` 查询。
诊断接口还报告 workspace 进程视图和网络作用域；
health / capabilities 还报告资源记账、准入与设施可用性。各投影的边界见
[observability.md](observability.md)，具体字段由 API 和配置代码维护。当前没有一个
“已隔离”布尔值能代表上述全部保证。Bed Ready 表示初始化完成，不等于所有维度达到理想状态。

验证按契约与组合组织，使用项目既有单元测试和 [单机 E2E](../tests/e2e/README.md)：

- 三档使用相同的结构化路径用例，验证数据落点与回显一致；
- 两个 Bed 验证自己的数据可用、邻居访问符合实际机制，并检查共享路径的边界；
- 一次性命令、后台执行、session 和 Pillow 主进程验证同一套身份、网络与文件视图；
- 在候选机制及其组合上验证真实准备、降权和执行，纯 argv 测试不代表内核隔离已生效；
- 验证初始化取消、部分清理失败、同 ID 重建与 Executor 替换，不把残缺资源发布为可用。

未完成的能力统一记录在 [backlog.md](backlog.md)。理想目标用于约束演进方向，
实际保证由实现、探测结果和已执行的验证共同支撑。
