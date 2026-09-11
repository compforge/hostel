# Bed 隔离：目标、兑现与实际边界

## 一、理念与概念

### 理想的 Bed 与共享 Carrier

Hostel 面向 AI Agent，用一份重运行环境低成本承载多个 Bed。**理想状态下，每个 Bed
都是独立的执行空间：文件、进程、网络和资源相互隔离，一个 Bed 的活动不应泄漏给、
干扰或破坏另一个 Bed。** 这份目标不因宿主内核、权限或选中的机制而改变。

隔离能力都按环境条件尽量兑现。Hostel 可以运行在 laptop、VM、CI 或容器内，无法假定
每处都有同样的 namespace、LSM、用户身份和 cgroup 能力。能力不足时仍保留 Bed 的身份、
文件归属与执行入口，使用可行机制提供尽可能多的隔离，同时把实际共享与缺口告诉调用方。

共享是成本取舍：工具链、软件环境和重型设施可以在 Carrier 内复用，以应用级切分或
内核机制尽量保持 Bed 的独立性。共享实现是否影响数据、网络或故障边界，需要逐项说明，
不能因为采用了共享进程就忽略其隔离目标，也不能把逻辑切分当成完整的安全屏障。

当前信任模型是可信或半可信代码。完整独立性是设计目标，实际保证以运行时能力为准；
当前实现不承诺对抗性代码所需的 VM 或专用容器等价边界。跨实例调度与信任分档由上层负责。

### 控制面与运行边界

Bed ID、header 和 query 参数用于选择资源，不是调用方的身份凭据。HTTP 控制面可以操作
多个 Bed，入口认证与 Bed 访问授权由接入方负责；文件房型和 netns 不会自动补上这层授权。
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

### 各维度的目标与当前兑现程度

| 维度 | 理想状态 | 当前能力与缺口 |
|---|---|---|
| 文件与路径 | 每个 Bed 使用自己的文件空间，不能读写邻居数据 | BedFS 统一结构化路径；文件房型决定进程侧的访问屏障；用户态投影只改善路径体验 |
| 进程与身份 | 执行独立、可完整回收，不能观察或干扰邻居进程 | daemon 保留管理权限，BedUser 统一约束命令身份；Executor/supervisor 管理进程归属与回收；当前 suite 没有私有 PID namespace |
| 网络 | 每个 Bed 有独立网络空间，并受自己的出站约束 | 可选 netns 覆盖 Bed 命令与 shell；不可用时共享 Carrier；共享设施出站和可配置 egress policy 尚未收敛 |
| CPU / 内存 | 一个 Bed 的失控负载不挤占或拖垮邻居 | Carrier 准入与可选 per-bed cgroup 记账已有；per-bed 硬限额尚未实现 |
| 环境与凭据 | Bed 只接触属于自己或明确共享的配置、凭据 | 过滤 Hostel 保留命名空间；其余 Carrier 环境默认继承，部署方负责其中的敏感信息 |
| 软件与设施 | Bed 的状态与使用互不影响，同时复用重资产 | Carrier 软件环境共享；Chromium 按 BrowserContext、MCP 按配置和会话切分，仍共享承载进程与部分故障边界 |

Store 管持久化与恢复，和这些能力协作，但不提供隔离屏障。路径属于 Bed、能否被邻居访问、
是否进入快照是三个独立问题；增加 path projection 不会自动扩大持久化范围。

## 二、机制组合与生命周期

### 主流程与所有权

```text
实例启动：采集环境事实 → 实测候选机制 → 选择可用能力 → 实测最终命令 / shell 组合 → 发布诊断
Bed 初始化：准备或恢复 BedFS → 准备文件边界、网络资源与记账父组 → 发布 Ready
执行：解析 BedFS 路径 → 组装网络 / 文件视图 / 身份 → 启动 Execution 或 session shell
Executor 替换：重建进程承载域，保留同一 resident Bed 的 BedFS 与网络身份
Bed 回收：撤销 session、同步需持久化的数据、停止执行并释放设施与网络
```

Bed 生命周期拥有准备、发布、撤销与回收的协调权；各组件拥有自己的资源和清理。
Network Manager 管 resident Bed 的网络，Executor backend 管进程承载，文件边界与
workspace backend 共同实现 BedFS 的进程视图。流程细节由 [lifecycle.md](lifecycle.md)、
[filesystem.md](filesystem.md) 和 [network.md](network.md) 分别维护。

### 组合必须守住的约束

- **准备完成才可服务**：不能把半恢复目录或尚未分配的必需网络当成 Ready。
- **特权步骤先于最终降权**：网络进入、文件视图准备和用户身份切换须按依赖顺序完成，
  再把控制权交给用户命令。任何单一组件都不能提前丢掉后续准备仍需要的能力。
- **同一 Bed 的执行入口一致**：一次性命令和常驻 shell 应使用同一套能力组合；共享设施
  没有进入该进程树时，必须单独说明其边界，不能自动继承 Bed 命令的隔离声明。
- **释放中的资源不再服务新执行**：失败清理仍需有人负责，但不能因此继续被视为可用。
  同 ID 的重新初始化不得复用残缺资源，也不得被上一轮清理回收。

`isolation.Environment` 绑定一个 resident Bed 的文件视图、具体网络 allocation 和最终
`BedUser`，命令和 shell 都通过它组装。普通文件房型采用实例配置的固定 BedUser：root
daemon 默认使用 1000:1000，非 root daemon 默认沿用自身身份。UID 房型按 Bed 目录稳定派生
专属的高位 UID/GID，并以同一个 BedUser 值驱动目录 ownership 和进程身份，不维护第二套
执行路径。执行顺序为网络进入 → 身份切换与最终降权 → 文件边界和进程视图 → 用户程序：
Network 先使用 daemon 权限完成 netns entry，UID 切换和 capability 丢弃交给同一个
`setpriv` 操作，bwrap/Landlock/路径 helper 再以 BedUser 运行。Store、Network 和 Executor
仍各自提供能力，Bed 协调其生命周期。共享 Chromium
位于 Bed 进程树之外，不从某个 BedUser 派生运行身份。

实例选定能力后，以临时 Bed 走真实命令和常驻 shell 的子目录读写路径，并核对 file API
可见同一产物，同时核对最终 UID/GID、实际 capability 集合与 `NoNewPrivs`；root daemon
还会清空并核对 capability bounding set。组合探测失败会阻止
HTTP 服务启动，不在运行时降级。这个探测证明选中路径可执行，跨 Bed 拒绝访问、权限
集合与失败回收仍由对应机制 probe 和回归测试验证。

回收经过最终活动复核后进入待清理集合，停止数据面准入。旧 Executor、设施、cgroup、
网络及本地目录全部清理成功，才允许同 ID 重建。失败保留原来的资源 owner 和容量名额，
通过 Evict/Purge 或实例关闭重试；待清理目录不属于 luggage。资源句柄对应具体分配，
旧句柄不能按可复用的 Bed ID 删除新资源。

## 三、关键设计

### 文件房型只描述文件隔离程度

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
命令文本保持不透明，映射之外的绝对字面量由实际进程视图解释。完整语义见
[filesystem.md](filesystem.md)。

### 文件机制的取舍

**suite 遮蔽后再挂回本 Bed**。只把 Carrier 根挂成只读仍允许读取邻居数据，因此先遮蔽
工作区父目录和存在的宿主敏感路径，再挂回当前 BedFS。工具链继续共享，`/usr/local`
保留 Carrier 级可写软件环境。当前 bwrap 每次启动的 `/tmp` 是独立 tmpfs，不能当作
跨命令持久的 Bed 数据；需要延续的数据应放在 BedFS 中。

bwrap 使用 user namespace 完成挂载准备，并绑定已有 `/proc`，以适应容器内受限的 procfs。
当前没有私有 PID namespace，因此私有文件视图不等于完整的进程不可见性。机制及参数顺序
锚点在 `internal/isolation/bwrap_args.go`；部署权限示例见
[deploy/k8s/README.md](../deploy/k8s/README.md)。

**room 用访问控制兑现数据边界**。Landlock 在子进程中应用规则，UID backend 在子进程中
切换身份；daemon 必须继续服务所有 Bed，不能对自身套用某个 Bed 的边界。两者都必须在
用户代码开始前生效，已有描述符和公共路径的可达性不能仅靠路径名称推断。

UID backend 的目录属主与进程身份保持一致，身份准备后刷新 BedFS 的属主记录，
file API 新建文件及目录也交给该属主。
当前 UID 由 Manager 在固定高位范围内分配：先按 Bed ID 选择稳定起点，冲突时探测空闲 UID，
同一进程管理的 Bed 不会因散列碰撞共享身份；启动时从冷 Bed 数据目录的 owner 恢复预留，
只有本地 Bed 目录删除成功后才释放租约。luggage GC 删除期间保留 Bed ID 栅栏，同 ID 初始化等待
目录清理和 UID 释放共同完成；清理失败继续保留 UID 占用。该范围仍可能与宿主账号或 userns
映射重叠，部署须预留。属主交接跳过多硬链接普通文件，
以免把 Bed 外的 inode 改属主；部署应保留 `fs.protected_hardlinks=1`。具体规则靠
`internal/isolation/uid_linux.go` 选择 per-Bed 用户，`internal/privilege` 与 BedFS
共同承接进程 credentials 和文件属主操作。

### 软件共享与敏感信息

Carrier 共享工具链、解释器和全局安装的软件。局部版本冲突优先在 BedFS 内使用 venv、
conda、本地 node_modules 等生态机制解决。共享软件的更改可能影响其他 Bed；需要独占
整个软件环境时由上层选择更强的 runtime。这个成本取舍不改变用户文件与产物按 Bed 归属
的目标，也不意味着当前所有共享路径都已受到租户级保护。

文件遮蔽和环境继承是两条边界。suite 遮蔽存在的 `/root`、`/home`、`/run/secrets`、
`/var/run/secrets` 等敏感路径；低档不能据此假定也具备同样的遮蔽。
进程环境在 `internal/bed/env.go` 统一组装，与文件房型正交：

- 过滤 Carrier 中的 `HOSTEL_*`、外部 `BED_*` 和 Hostel 管理的 CDP endpoint；
- 其余 Carrier 环境默认继承，由部署方承担其中凭据和配置的共享风险；
- 注入 Bed 身份、路径和设施上下文，再叠加本次请求的环境；请求不能占用保留命名空间。

过滤只约束子进程继承，不等于禁止经 `/proc` 观察其他进程，也不是凭据代理。共享设施的
凭据、协议过滤与会话归属见 [amenity.md](amenity.md) 和 [mcp.md](mcp.md)。

### 网络与资源独立演进

网络能力不从文件房型推导。实例探测 netns 可用时为 Bed 分配网络，命令和 shell 共用，
Executor 替换不改变该 resident Bed 的网络身份；能力不可用时共享 Carrier 网络。
当前 netns 作用域是 Bed 进程，Chromium/MCP 的出站仍来自 Carrier。BrowserContext
切分浏览器状态，不构成 OS 网络边界；机制、DNS、诊断与出站约束见 [network.md](network.md)。

资源记账回答“谁用了多少”，准入回答“是否继续接纳”，硬限额才约束已接纳的负载。
当前具备前两者不等于已经避免吵闹邻居，per-bed 硬限额仍是缺口。详见
[resource.md](resource.md)。这些维度沿同一个 Bed 隔离目标发展，分别披露兑现程度。

### 尽力兑现与失败语义

**能力不足时可以降级；已选机制执行失败时必须明确失败。** 启动预检只能排除不具备前提
的候选，不能证明机制真正生效；实际选择依赖 smoke，避免把存在的二进制、内核版本或
capability 当成隔离成功。不同机制分别通过，还需要检验它们的组合。

- 文件请求超过环境上限时选择可达档位并披露；明确请求较低档位时尊重调用方选择。
- workspace helper 不可用时退回 Carrier 视图；BedFS 结构化路径仍保持正确。
- 网络启动探测失败时披露共享网络；已启用后单个 Bed 网络准备失败则初始化失败。
- 已选的文件或网络机制在运行时失败，不静默改成更弱的执行路径。
- 某个 API 明确不支持的请求应返回不支持，不能借“尽量”把参数忽略。

调用方据实际能力决定接受边界还是改选 Carrier。Hostel 不自行获得额外部署权限，也不把
缺席能力伪装成成功隔离。

## 四、能力披露与验证

诊断接口报告文件请求、有效档位、环境上限与机制、workspace 进程视图和网络作用域；
health / capabilities 还报告资源记账、准入与设施可用性。各投影的边界见
[observability.md](observability.md)，具体字段由 API 和配置代码维护。当前没有一个
“已隔离”布尔值能代表上述全部保证。Bed Ready 表示初始化完成，不等于所有维度达到理想状态。

验证按契约与组合组织，使用项目既有单元测试和 [单机 E2E](../tests/e2e/README.md)：

- 三档使用相同的结构化路径用例，验证数据落点与回显一致；
- 两个 Bed 验证自己的数据可用、邻居访问符合实际机制，并检查共享路径的边界；
- 一次性命令、后台执行和 session 验证同一套身份、网络与文件视图；
- 在候选机制及其组合上验证真实准备、降权和执行，纯 argv 测试不代表内核隔离已生效；
- 验证初始化取消、部分清理失败、同 ID 重建与 Executor 替换，不把残缺资源发布为可用。

未完成的能力统一记录在 [backlog.md](backlog.md)。理想目标用于约束演进方向，
实际保证由实现、探测结果和已执行的验证共同支撑。
