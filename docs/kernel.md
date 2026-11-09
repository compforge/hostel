# Hostel 核心架构

## 定位与目标

**Hostel 协调各 Subsystem，尽力满足 BedSpec，并按 Subsystem 如实报告落实状态；caller 根据真实状态决定是否使用。**

Hostel 的启动与持续运行独立于 caller 的使用决策。各 Subsystem 在宿主条件允许的范围内
尽力提供能力并报告实际状态；caller 发现能力不满足需求时可以不用，Hostel 仍继续运行。
caller 的选择不构成验收或激活阶段，声明的 Pillow 主进程按 Bed 生命周期启动。
能力等级不足本身不阻止运行；实际准备、执行或清理失败仍按所属生命周期报告和处理，
显式 required 工具与只读挂载等功能契约的失败语义见 [隔离与能力选择](isolation.md#启动失败与观测)。

**Hostel 是面向 Agent 的 Pod 式工作负载运行时，通过 BedSpec 组织执行环境与共享资源，内建工作数据同步，并根据宿主能力分级兑现运行与隔离需求。**

Hostel 根据 BedSpec 为各 Pillow 准备执行环境，并在 Bed 的整个生命周期内提供基础设施、
进程监督、数据持久化与资源回收。准备执行现场与持续照料运行是同一生命周期的两个方面：

- 执行现场由 Bed 拥有根数据、身份、网络、资源和 Volumes，各 Pillow 组合自己的挂载视图、环境与工作目录。
- 运行照料由 Hostel 的领域子系统持续承担：监督进程、维护共享设施、同步数据、观测状态，并在恢复或回收时完成资源交接。

子系统按领域拥有状态与资源，跨准备、运行和回收阶段协作。例如 Store 在启动前恢复数据、运行中同步、
回收前最终同步；无需按阶段拆成两套子系统。用户程序执行前的可信准备与最终权限切换，见
[权限与执行身份](isolation.md#权限与执行身份)。

Hostel 在一个 Carrier（一台机器或一个容器）内管理多个 Bed，复用工具链与重型设施。
调用方通过 BedSpec 声明工作负载，使用文件、命令、Session 和常驻服务；各领域根据宿主
实际能力选择实现。Docker/containerd 可以承载 Carrier，完整 namespace 和 cgroup 则是
可利用的能力，而非所有 Bed 的启动前提。

Hostel 的价值是利用可用条件补齐工作负载语义，提高共享 Carrier 的密度，而非重复提供一套 Pod/Container 对象。
原生机制不足时，路径映射、API 补偿与替代机制应尽量保持可用性；功能体验和隔离保证分别报告。
工作负载语义先于实现机制：房型汇总 fs/net 实际保证，不参与机制选择或准入；
共享网络规则可尽力执行并显式报告缺口，不能声称已执行。只读挂载等功能契约仍须兑现。API 的任务可用性与隔离保证分别判断。例如缺少 cgroup 时仍可执行命令，
但不能声称已有资源硬限额；文件 API 可用也不代表进程拥有完整私有根视图。

当前面向可信或半可信代码。Hostel 负责实例内运行、资源管理与工作数据同步；上层负责
实例调度、跨实例路由、单写者归属与入口授权。各维度的信任边界见 [isolation.md](isolation.md)。

## 工作负载模型

Bed 声明必须包含非空 Pillows；首项为默认 Pillow。创建入口拥有声明，数据访问只等待已声明 Bed 就绪。

| 对象 | 身份与职责 |
|---|---|
| Bed | 路由、资源归属和生命周期单位；拥有 Pillows、Volumes、根数据、网络与资源需求 |
| Pillow | Bed 内的命名执行环境；持有 env、配置引用、workdir、mounts、ports、readinessProbe 和可选主进程声明 |
| Volume | Bed 内命名的数据来源；由 Hostel 管理或引用外部目录，可声明自动持久化 |
| Execution | 一次运行；属于 Pillow，由当前 Executor 承载，具有输出和结构化终态 |
| Amenity | Carrier 共享设施；按 Bed 分配 Tenant 状态，如 Chromium BrowserContext、MCP 连接 |

```text
Hostel / Carrier
├── Bed
│   ├── Volumes、Rootfs、网络、资源
│   └── Pillows
│       ├── env、workdir、mounts
│       └── Command / Session / 可选主进程
└── Amenities → 按 Bed 绑定 Tenant
```

Bed 的 Name 由调用方给定，用于寻址和定位远端快照；本地 ID 由 Hostel 生成，用于运行资源
归属。Pillow 名和 Volume 名在 Bed 内唯一。第一项 Pillow 是默认执行环境，顺序不表达启动依赖。
多个 Pillow 可把同一卷挂到不同路径；卷身份独立于挂载路径。Pillow 共享 Bed 的用户身份、
网络和资源，不因环境或挂载不同而获得独立容器的安全边界。

Executor 是 Bed 当前可替换的进程承载域；BedFS 是根数据、卷和各 Pillow 路径视图的实现。
替换 Executor 保留 Bed 资源与数据，重新绑定执行视图。Store 实现卷的同步、恢复和显式传输。
这些子系统服务于工作负载模型，各自拥有实现资源与状态。

## Pod / Container 式声明与职责边界

Bed / Pillow 在工作负载配置模型上对应简化的 Pod / Container：Bed 拥有共同生命周期、
网络和命名卷，Pillow 声明进程、环境、工作目录、挂载、端口和就绪条件。这个对应帮助调用方
组织配置，不承诺 Kubernetes API 兼容、每 Pillow 独立镜像或完整容器隔离。

| 需求 | 声明位置 | 执行与状态 owner |
|---|---|---|
| 共同数据来源 | Bed `volumes` | Filesystem / Store |
| 命令、环境与挂载视图 | Pillow `command/args/env/env_from/workdir/mounts` | Configuration / Filesystem / Executor |
| 监听端口与是否发布 | Pillow `ports` | Network 分配、发布、撤销和回收；状态保存实际端口 |
| 就绪条件 | Pillow `readinessProbe` | Pillow 调度通用探针，决定工作准入 |
| 业务 HTTP 协议与认证 | 业务程序和调用方配置 | 业务程序验证，调用方持有凭据 |

声明归属与实现归属分开：Pillow 声明需要哪些端口，不意味着 Pillow 自行实现网络分配。
同一端口只声明一次，不要求额外的 Bed `port_mappings` 或 `network.ports`。
`ports[].publish` 是 Hostel 的发布扩展，尚不需要独立 Service 对象；它不提供路由、
负载均衡或业务鉴权，也不是防火墙规则。

配置按作用对象分层。`HOSTEL_*` 是 Carrier/daemon 的基础设施配置，不自动转成业务变量。
Carrier 中其余可继承环境作为所有 Bed/Pillow 的默认值，包括部署方通过 Pod envFrom
提供的 Secret/ConfigMap。Pillow 可以再选择自己的命名文件来源；显式变量覆盖来源导入，
来源导入覆盖 Carrier 默认值。不同 Pillow 不隐式继承彼此的显式配置。

共享环境继承是 Hostel 的额外能力，不是 Kubernetes Pod 的顶层 env。部署方决定哪些业务
配置可被整个 Carrier 共享。来源注册与环境注入也不等于把文件挂入 Pillow；文件视图仍通过
Volume/Mount 表达。完整配置规则与 main + worker 示例见 [Pillow](pillow.md)。

`readinessProbe.httpGet` 表达健康检查，不把 Pillow 定义成一种 HTTP 服务。Hostel 不生成、
发现、旋转或返回业务 token，也不按 token/AWS 等业务变量名判断配置优先级。进程收到
配置后自行解释；需要认证的业务接口可以与公开健康路径分开，或选择 TCP 就绪检查。

## 所有权与子系统契约

按 Subsystem 划分 BedSpec 的显式需求与隐式需求，是对运行时需求的分治：显式需求来自
Spec 声明，隐式需求来自既定的工作负载语义与默认契约；通常按领域归属到对应 Subsystem，
由它负责落实。Subsystem 拥有领域配置、实现机制、资源生命周期和实际状态；少量跨领域需求
通过显式接口协作兑现。各领域保留类型化配置、操作与状态；Hostel 不引入统一需求字典或
通用满足度接口。

需求由什么机制落实，取决于 OS/kernel 能力、Node 配置及容器/Kubernetes 授予的实际权限等
宿主条件。各 Subsystem 在自身领域内选择、组合可用机制，并报告实际落实程度与限制；
独立 namespace 等是实现手段，是否需要它取决于具体需求。宿主事实与操作验证见
[领域与 Host 机制](#领域与-host-机制)。

`bed.Subsystem[S]` 统一 daemon 生命周期、单 Bed 生命周期和实例级 `Status()` 契约。
资源与状态由所属子系统唯一写入，分配与持有的共同契约见下文。
`Stop` 停止活动，`Release` 释放运行资源，`Forget` 在进程、资源与本地数据清理完成后遗忘身份。
Hostel 通过 Bed Manager 协调顺序并统一发布 Ready，
局部 Prepare 成功不代表整个 Bed 已准备完成。后台循环使用可选 `Runnable`，不强制所有子系统运行后台任务。

`Status()` 返回实例级快照，Bed 级事实通过所属状态分区发布；查询不执行探测、分配或远端 I/O，
也不通过共享 map/slice 暴露第二个写入口。声明、机制支持、实际采用和 Bed 落实结果分别表达，
未准备或已释放的资源不报告为已落实。各子系统状态不要求具有同一等级或同一结构。

跨领域依赖通过显式接口连接。例如 Network 拥有 resolver 文件和网络 allocation，Filesystem
将其纳入进程视图，Bed Manager 协调先准备网络再准备视图，并在释放视图后回收网络。
领域专用步骤保留自己的接口，不为统一 hook 隐藏协作顺序。机制选择和回退属于领域 owner；
Hostel 不枚举跨领域候选组合，整体验证失败时如实报告并清理。

`internal/bed` 定义共享模型与生命周期契约。Bed Manager 拥有 Spec、Lifecycle 和跨领域
协调；Filesystem、Privilege、Network、Resource、Configuration、Executor、Pillow、Store
各自持有资源句柄、任务及状态。Amenity Manager 直属 daemon，通过 Bed 生命周期适配释放 Tenant。

一个本地生命周期只有一个共享 Bed，初始化重试和 Executor 替换只替换所属资源。
状态按领域授权写入、按值提供快照；查询不能通过共享 map、slice 或指针获得第二个写入口。
Bed Manager 的请求句柄协调 operation/session 和清理进度，不另造一份领域 Spec/Status。

领域子系统有三个驱动面：daemon 级 Start/Close、单 Bed 生命周期 hooks、可选后台 Run 循环。
Start 同步完成准备，Run 自己管理周期、合并与退避，Close 取消并等待退出。Bed Manager
显式安排跨领域依赖顺序；资源按具体 allocation 回收，失败保留清理责任。具体阶段与重试见
[bed.md](bed.md#子系统参与生命周期)。

### 资源分配与持有

Subsystem 在准备和持续运行过程中按需申请资源、持有 allocation，并在所属生命周期结束时
负责释放；Hostel 协调跨 Subsystem 的依赖和回收顺序。资源包括 port、UID/GID、网络地址、
netns 和 cgroup 等，一次 Bed 初始化或运行活动可以持有多个不同生命周期的 allocation。

**资源**是被分配的对象；**allocation** 是某个 owner 在特定作用域内对资源的一次具体分配，
包含资源值与实际状态；**lease / handle** 是持有、使用和释放该 allocation 的具体句柄。
Allocator 负责选择资源、登记占用及判定冲突；持有句柄的领域负责使用、交接和最终清理。
排他性由资源的作用域与策略决定，例如 TCP port 按网络作用域和监听地址判断冲突，Privilege
区分独立身份与显式共享身份。

各领域遵守以下共同契约：

- **归属明确**：记录 allocation 的 owner、作用域和资源值；清理使用原 allocation 的句柄，旧句柄不能释放同名或同值的新分配。
- **状态真实**：区分逻辑预留和实际生效。例如 port 的 `reserved` 表示已登记占用，`listening` 表示监听已经落实；申请成功不等于消费者已就绪。
- **按依赖释放**：先停止使用者、撤销依赖，再释放 allocation；进程停止是否结束预留，由所属领域的生命周期决定。
- **失败保留责任**：准备失败也可能留下部分资源；清理失败继续持有占用与清理责任，后续重试不得提前将资源交给新使用者。重复释放同一 allocation 应安全。
- **恢复由领域负责**：需要跨重启保留的身份先记录再使用；遗留运行资源按领域记录与宿主事实 reconcile，决定复用、重建或回收。持久化格式和恢复机制由对应 owner 维护。

| 资源 | 分配与持有责任 | 存续与释放边界 |
|---|---|---|
| UID / 主 GID | Privilege 管理租约，Bed Manager 保存本地身份记录并协调 Forget | 随 Bed 本地身份保留；运行停止后仍可能占用，资源、本地数据和身份记录清理完成后释放 |
| TCP port | PortManager 管理分配，Network 或服务领域持有句柄 | 随所属服务或网络 allocation 保留；使用它的监听器、进程和转发清理后才能结束占用 |
| netns、网络地址与规则 | Network 管理 allocation 与恢复记录 | 随 Bed 网络 allocation 存续；依赖它的进程、文件视图等释放后回收 |

这些契约由各领域的类型化 allocator、句柄和状态兑现。现有 Resource Subsystem 负责
CPU/内存、cgroup 记账与容量准入，职责见 [资源治理](resource.md)；具体身份与网络恢复分别见
[Bed 生命周期](bed.md#身份与声明)和[网络](network.md)。

### 领域与 Host 机制

依赖方向为：入口 → 领域模型与流程 → 通用宿主机制。`internal/host` 提供 namespace、
挂载、进程、身份、cgroup 和事实采集能力，不依赖 Bed/Amenity，不解释房型或工作区。
领域赋予资源身份、策略与生命周期，决定是否允许降级，并发布实际状态。

Filesystem.Start 先准备 Carrier 明确授权的共享软件目录（组权限、setgid、default ACL），
Bed 级 Prepare 只接入视图和身份；共享目录配置和结果见 [权限模型](isolation.md#权限与执行身份)。

启动采集 Host facts，领域再用真实操作验证可用性；单个工具通过不等于组合可运行。
状态读取只投影已观测事实。API handler 负责寻址、准入与协议转换，不能拼接内部路径或
操作资源句柄；原生和 Execd adapter 直接复用领域能力，彼此不依赖。代码地图见
[AGENTS.md](../AGENTS.md)，协议边界见 [protocol.md](protocol.md)。

## 跨领域主流程

### 实例启动

合并配置 → 构造领域子系统 → 恢复本地身份与清理责任 → 探测并验证执行组合 → 开放 API。
启动失败逆序关闭已准备子系统；正常运行由领域维护同步、采样和回收循环。

配置入口按 `显式 Options > CLI > HOSTEL_* env > 默认值` 生成确定的 Config，区分未指定
与显式零值。各子系统只读自己的配置；参数按部署需求暴露，完整配置不进入状态响应。
工具策略与组合验证见 [isolation.md](isolation.md#能力选择与工具策略)，Pillow 配置见
[pillow.md](pillow.md#配置来源)。

### Bed 初始化与执行

```text
声明 Bed → 预占容量、合并同名初始化
  → 恢复并验证卷数据 → 准备身份、网络、资源与文件视图
  → 解析 Pillow 配置、启动必需主进程 → Ready
  → operation / session 准入 → 领域操作 → 输出与终态
```

准备完成前不开放数据面，远端恢复错误不能当作空 Bed。执行使用所属 Pillow 的环境，
在可信准备完成并降权后运行用户程序。已接纳的工作受生命周期保护，状态查询不保活。
详细准入和状态语义见 [bed.md](bed.md)，进程模型见 [pillow.md](pillow.md)。

### 同步、回收与恢复

```text
本地读写 → Store 合并同步触发 → 发布持久卷快照
Bed 回收 → 关闭准入 → 停止写入 → 最终同步
  ├─ 失败：保留本地数据与清理责任
  └─ 成功：释放资源 → 删除本地数据 → 释放本地身份
再次创建 → 恢复持久卷、重建临时数据与运行环境
```

Bed 内持久卷共同发布一个 generation；环境配置、进程状态和共享设施运行态不进入卷快照。
同步不是实时共享文件系统，上层须保证同名 Bed 的单写者归属。Purge 显式销毁本地及远端数据；
daemon 正常关闭停止工作和资源，保留本地数据供恢复。数据契约见 [volumes.md](volumes.md)。

实例停止先撤销准入、取消并等待后台及初始化任务，再清理 Bed 运行资源、逆序关闭子系统和设施。
上层根据实例事实决定释放 Carrier；Hostel 不因空闲自行退出。完整回收、保留期限与失败重试由
[bed.md](bed.md) 定义。

## 领域文档

| 文档 | 回答的问题 |
|---|---|
| [bed.md](bed.md) | Bed 如何声明、准备、服务、保活、回收与恢复？ |
| [pillow.md](pillow.md) | 环境配置如何供命令、Session 和主进程使用，进程如何监督和替换？ |
| [volumes.md](volumes.md) | 数据属于谁、如何挂载访问、怎样持久化与传输？ |
| [isolation.md](isolation.md) | 信任、权限和能力选择如何兑现并披露实际保证？ |
| [network.md](network.md) | 网络、端口发现与出站策略如何管理？ |
| [resource.md](resource.md) | 容量、资源记账、准入与限制分别保证什么？ |
| [amenity.md](amenity.md) | 共享设施如何按 Bed 切分状态、凭据和生命周期？ |
| [protocol.md](protocol.md) | 如何寻址和表达请求、结果、错误及协议差异？ |
| [observability.md](observability.md) | 当前状态和执行过程怎样投影到诊断接口、日志与 Trace？ |
| [backlog.md](backlog.md) | 哪些能力尚未交付？ |

使用入口见项目 [README](../README.md)，验证入口见 [单机 E2E](../tests/e2e/README.md)。
