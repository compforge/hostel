# hostel

## 项目定位与边界

**Hostel 是面向 AI agent 的多用户共享运行时**：在一个 Carrier（一台机器或一个容器）内管理多个
Bed，通过 BedSpec 组织执行环境与共享资源，保护私有数据，内建工作数据同步，并根据宿主能力
兑现运行与隔离需求。可单机运行，也可由上层调度通过 `sandbox_id → (实例, bed)` 路由驱动。

**共享优先、私有保护、按需隔离。** Bed 类似同一台 Linux 上的多个用户会话：各自拥有工作数据，
复用公共程序、库和重型设施，按授权访问 Hostel 与 OS 资源，程序无需显式 sudo。Bed/Pillow 在
声明模型上可类比简化的 Pod/Container，但不承诺 Kubernetes API 兼容、每 Pillow 独立镜像或完整
容器隔离。Docker/containerd 可以承载 Carrier；namespace、cgroup 等是可利用的能力，不是所有
Bed 的启动前提。具体声明与消费边界见 [Pillow](docs/pillow.md#声明模型与职责边界)。

**Hostel 协调各 Subsystem，尽力满足 BedSpec，并按 Subsystem 如实报告落实状态；caller 根据真实状态决定是否使用。**
Hostel 的启动与持续运行独立于 caller 的使用决策，不增加验收或激活阶段；声明的 Pillow 主进程
按 Bed 生命周期启动。各领域选择实现和有限回退，功能可用性与隔离保证分别报告：缺少 cgroup
不应自动阻止命令执行，文件 API 可用也不代表进程已有完整私有根视图。房型汇总实际 fs/net 保证，
不参与机制选择或准入；共享网络规则可尽力执行并明确报告缺口，不能伪装成 enforcing。

best effort 适用于能力选择，不能掩盖已选机制的执行失败。明确 required 的工具、只读挂载等
功能契约仍须兑现；实际准备、执行、清理失败按所属生命周期处理。未声明机制的选择与尽力程度
可以演进，不构成固定隔离承诺。失败语义见 [隔离与能力选择](docs/isolation.md#启动失败与观测)。

当前面向可信或半可信代码。Hostel 负责实例内 Bed 生命周期、exec/file、工作数据同步与共享
设施；上层负责实例调度、跨实例路由、同名 Bed 的单写者归属、入口授权与计费配额。
daemon 组装 Web Server、Bed Manager、Amenity Manager，Bed Manager 组合各领域子系统。
资源与文件 API 以 OpenSandbox 为设计基线，原生执行协议由 Hostel 拥有；`/execd` 与 Bed 路径入口
在 API 层适配 OpenSandbox。参考 execd（Apache-2.0）净重写，非其 fork；归属见 [NOTICE](NOTICE)，
协议见 [protocol.md](docs/protocol.md)，未交付项见 [backlog.md](docs/backlog.md)。

## 概念与命名约定

以下名词全仓（代码 / 注释 / 文档）统一使用，避免同物多名、一名多物：

- **Subsystem（子系统）**：领域实现、资源生命周期与状态的 owner；一项 BedSpec 需求可跨子系统协作，Bed Manager 负责组合、顺序及 Ready。共享接口见 `internal/bed/subsystem.go`，共同约束见下文[所有权与子系统契约](#所有权与子系统契约)。
- **bed**：隔离执行单元，对外即一个 sandbox（独立文件空间 + 执行环境，状态跨命令保持）。
- **bed name**：调用方给定的不透明路由标识，支持中文；HTTP 请求的 bed/id 参数及 `BED_ID` 都表达 Name；管理响应同时返回 `name` 与本地生命周期 `id`，必须显式选择 Bed。Hostel 不解释其上层业务语义。
- **bed id**：Hostel 生成的本地身份；本地数据保留期间跨重启恢复，Forget 后同名创建换 ID。领域资源以 ID 归属，远端快照以 Name/快照引用定位。
- **beds-root**：所有 bed 目录的**父目录**，**可配、不写死**（`--beds-root` / `HOSTEL_BEDS_ROOT`，默认 `/workspace`）；**daemon 启动时创建一次**。
- **bed 目录**：`{beds-root}/{bed name}`，含 `meta.json`（可移植身份）、`rootfs/` 与 `volumes/`；由 `InitializeBed` 异步准备。只有 Store Stage-in/Restore 与 BedFS/isolation 准备全部完成后才发布 Ready，原生数据面通过 `Ensure` 等待已经显式声明的 Bed，缺失返回 not found，详见 `docs/bed.md`。
- **BedFS**：Bed 持有的文件系统数据域；提供 Bed Rootfs、Volume 数据与各 Pillow 的 Workdir/Mounts 视图、三个路径空间及文件操作。文件 API 由 daemon 直接调用 BedFS，限定在 Bed 数据和显式授权来源，不模拟 Bed Unix 身份或隐式导出公共运行依赖。Executor 替换不改变 BedFS 身份，详见 `docs/volumes.md`。
- **bed_home（rootfs 目录）**：BedFS 的宿主根 `{bed 目录}/rootfs`——**Bed 自身数据根**；未命中显式挂载的文件 API 路径解析到它下面，回显保持 Bed 路径。它不参与自动持久化，也不表示进程已有独立的 `/`。
- **Workdir**：Pillow 默认工作目录，当前为 `/workspace`（`bedfs.DefaultWorkdir`），也是 API 相对路径的解析基准；具体进程的 cwd 可以覆盖。Rootfs、Workdir 与 Volumes/Mounts 的关系见 `docs/volumes.md`。
- **Volume / Mount**：Bed 声明命名数据来源（managed / host_path）；每个 Pillow 声明自己的 mounts，所属 command、session、主进程和文件 API 共用该视图。共享数据与执行视图分别管理，见 [Volume 与文件视图](docs/volumes.md#来源与挂载)。
- **房型（dorm / room / suite）**：fs/net 各自将实际能力映射为房型，Bed 取较低值；房型不参与准入或机制选择，能力选择始终尽力向上（见 `docs/isolation.md`）。
- **luggage**：非正常生命周期状态，只表达异常退出遗留的本地 Bed 目录。正常 evict 在任意 Store backend 下都删除本地目录。
- **amenity**：bed 外由 hostel 统一管理、按 bed 分配状态的共享设施（Chromium / Jupyter / MCP 连接池）。
- **pillow**：Bed 内的命名执行环境，拥有 env、workdir、mounts 与可选主进程，与用户命令共用 Bed Environment/Executor；创建者提交通用完整 `PillowSpec`，Carrier 镜像提供程序与依赖，Hostel 不依赖具体业务服务。端口通过 daemon 级 Port Manager 统一申请，见 `docs/pillow.md`。
- **Pillow 端口与探针**：只在 `ports` 声明端口，由 Network 分配和回收；`readinessProbe` 是通用就绪检查。Hostel 不管理业务 HTTP 认证或 token。声明与实现 owner 的对应见 [Pillow](docs/pillow.md#声明模型与职责边界)。
- **executor**：某个 bed 当前的、可替换的进程承载域。bed 持久存在，executor 丢失或关闭后可用新 id 重建；Linux 默认使用 supervisor backend，非 Linux / 显式 local 使用 daemon 直接派生。
- **execution**：一次命令运行。每次有独立 id，且记录其所属 bed id、pillow name 与 executor id。
- **bedinit**：每次进程启动前的可信初始化路径，按 Bed Manager 绑定的方案进入环境、降权并 exec 用户程序；不是 Bed 生命周期初始化或常驻进程，详见 `docs/pillow.md`。

### 工作负载关系

```text
Hostel / Carrier
├── Bed                         路由、资源归属与生命周期单位
│   ├── Rootfs、Volumes         数据由 Bed 拥有，挂载视图由 Pillow 组合
│   ├── 身份、网络、资源         各 Pillow 共用
│   └── Pillows                 命名执行环境
│       └── Command / Session / 可选主进程
└── Amenities                   按 Bed 绑定 Tenant 状态
```

Bed 声明包含非空、有序 Pillows，首项为默认 Pillow，顺序不表达启动依赖。Pillow 名和 Volume 名
在 Bed 内唯一；多个 Pillow 可把同一卷挂到不同路径，卷身份独立于路径。Pillow 共享 Bed 的
身份、网络与资源，环境和挂载视图不同不构成独立容器的安全边界。

BedFS 实现根数据、卷与路径视图；Executor 是可替换的进程承载域，替换它保留 Bed 资源与数据。
Store 跨准备、运行和回收阶段承担恢复、同步与最终持久化；子系统按领域组织，不按生命周期
阶段拆成两套实现。具体声明见 [Bed](docs/bed.md#身份与声明)和 [Pillow](docs/pillow.md#声明与消费)。

**进程模型**（进程归属树；详见 `docs/pillow.md`〈命令、Session 与进程承载〉）：

```
tini (pid1)                       pod 级收尸兜底
└─ hostel (daemon)                内置 amenity supervisor，无独立 manager 进程
   ├─ chromium / jupyter          amenity：pod 级共享、按 bed 切租，不进任何 bed 树
   └─ bed [持久身份]
      └─ executor [当前 0/1，可替换]
         └─ supervisor            Linux backend：fork / 收尸 / 死前杀整树
            ├─ execution process  一次 command 一个，绑定 Pillow
            ├─ session shell      /session 显式常驻，固定 Pillow
            └─ main process       每个 Pillow 可选的自动启动进程
```
`Hostel → Bed → Executor → Execution` 是领域层次；supervisor / local 只是 Executor backend。

**路径模型**（Rootfs、Volumes 归属 Bed，Workdir、Mounts 归属 Pillow；进程视图与 API 补偿分别兑现，详见 `docs/volumes.md`）：

```
未命中当前 mounts 的文件 API 数据路径：/tmp/x → rootfs/tmp/x；相对路径 = Workdir 相对
   │  BedFS.Resolve：单射 rebase（回显为其逆映射）
   ▼
<beds-root>/                 宿主侧，所有 bed 父目录；可配 HOSTEL_BEDS_ROOT，默认 /workspace，daemon 启动建
└─ <bed name>/                      bed 目录；InitializeBed 接受显式 Pillow 声明后创建
   ├─ meta.json                   可移植身份
   ├─ rootfs/                     临时根数据
   └─ volumes/
      ├─ workspace/               默认工作区卷；persistence 决定是否同步
      └─ runtime/                 临时共享卷

Executor View：
  private files          → Bed 数据根作为 / + 共享运行依赖 + 当前执行方的卷挂载
  shared/confined files  → PRoot 根视图 → pathshim 局部映射 → Carrier；按实际能力选择并披露
```

Bed 的目标是共享优先、私有保护、按需隔离：保护各自数据和策略归属，复用公共程序与设施，按环境能力采用保护与隔离机制，并如实披露缺口。房型归纳 fs/net 实际保证的较低值，不等于所有维度完整隔离；所有档位保持同一 BedFS 路径归属。目标、机制组合与实际保证见 `docs/isolation.md`。

独占 Dorm carrier 可显式开启只读 file API 的进程根回退：BedFS 路径不存在且客户端传入绝对路径时，Reader 回读配置的进程根；BedFS 同名路径优先，mutation 不回退。该配置会暴露进程根、共享 carrier 禁止开启，详见 `docs/volumes.md`。

## 代码地图与核心模块

```
deploy/
├── docker/Dockerfile  多阶段多架构镜像(amd64/arm64,builder 原生交叉编译免 QEMU)：静态 hostel + debian-slim（内置 bwrap/PRoot/pathshim + 可选 chromium）；tini PID1；hostel --health 做 HEALTHCHECK
└── k8s/              Kubernetes 部署示例；AppArmor 的 PSA 豁免申请见 pod-security-admission-exemption.yaml
protocol/             独立 Go module（最低 Go 1.25）；server 与 caller 共用的 Bed 数据契约，仅依赖标准库
cmd/hostel/main.go     daemon 入口：配置、子系统组装、启动探测、Web 与信号；驱动 Manager 启停
tests/e2e/             真实进程/镜像 E2E：同一 suite 可在 Host 或 K8s Pod 内运行；环境配置与事实入口，见 tests/e2e/e2e-environments.md
internal/
├── bed/               唯一 Bed 模型（Spec / 分域 Status）及生命周期契约；不导入领域实现
│   ├── manager/       Composite：准入、初始化/回收顺序、operation/session、执行环境组装与诊断聚合
│   │   ├── bed.go     managedBed / Resident：请求操作句柄与并发协调，引用共享 Bed
│   │   ├── daemon.go  Start / Run / Close：子系统启动回滚、后台循环取消与 join
│   │   ├── resident.go 初始化各领域资源，全部完成后由初始化入口发布 Ready
│   │   └── local_identity.go 本地身份与清理占位；目录删除完成后才 Forget
│   ├── filesystem/    BedFS 与文件隔离的 owner，提供文件领域诊断
│   │   ├── bedfs/     数据根、路径投影及文件 API 的实现
│   │   └── isolation/ Bed 文件边界、进程视图的组合探测与降级
│   ├── privilege/     BedUser、UID 租约、ownership 策略、降权顺序与权限报告
│   ├── network/       可选 per-Bed netns、出站策略与精确 allocation 句柄
│   ├── store/         Stage-in、持久化、Transfer 及自动同步循环
│   │   ├── sync/      noop/auto/cas/pack/tar/copy/restic 同步策略
│   │   └── backend/   S3 位置、共享客户端和对象操作
│   ├── executor/      可替换进程域的 Manager、local / supervisor backend
│   │   └── supervisor/ Linux Executor 的 IPC、进程派生与收尸
│   ├── configuration/ 进程配置来源的宿主观测、解析与 Bed 生命周期内的值管理
│   ├── pillow/       Bed Pillow 声明、环境状态与可选主进程的就绪、监督、端口发布和停止
│   ├── tool/          各领域可选工具的 Policy、Requirements 与 Status；领域自行选择、组合及管理生命周期
│   └── resource/      cgroup accounting、carrier admission 与采样循环；未施加 per-Bed limits
├── amenity/           daemon 直属独立设施；Manager 维护 Bed ID 到 Tenant ID 的绑定，设施隐藏资源实现
├── host/              通用宿主能力；不依赖 Bed/Amenity 模型，不预设使用方
│   ├── network/       namespace、地址、DNS、出站规则与统一 TCP Port Manager
│   ├── process/       持有进程身份期间的服务进程组清理
│   ├── filesystem/    mount、Landlock、pathshim/PRoot 进程视图与 ptrace 探测
│   ├── cgroup/        组层次、放置句柄、用量与释放；无 Bed/Executor 组织策略
│   ├── privilege/     UID/GID、capability 清除与 ownership 操作
│   └── facts/         只读系统事实与执行探测记录
├── error.go           全局领域错误契约，不含 HTTP 或协议编码
├── config/            flags + HOSTEL_* env
├── tracing/           OpenTelemetry 与 trace/log 关联
└── api/               HTTP server、中间件与路由组装
    ├── apiv1/         Hostel 原生管理与数据协议
    │   ├── handler/   请求适配与状态查询
    │   └── view/      原生响应适配与纯转换；公开 Bed 结构定义在 protocol/
    └── execd/         OpenSandbox Execd 协议，双入口共用 adapter
        ├── handler/   请求适配与本地鉴权
        └── view/      OpenSandbox 响应、权限与 SSE 编码
```

**数据流**：请求 →`api/apiv1/handler` 或 `api/execd/handler` 按 显式 Bed 标识（`X-Hostel-Bed` 等） 解析 bed → 调 `bed`/`bedfs` 核心 → 组装响应（命令走 SSE）。核心模型与领域子系统**不含任何 HTTP 类型或协议族编码**；两套 adapter 不互相依赖，复用能力进入 `internal/bed` 等 owner。换框架只动 `api/`。

## 所有权与子系统契约

### 领域与 Host 机制

依赖方向为入口 → 领域模型与流程 → 通用宿主机制。`internal/host` 提供 namespace、挂载、进程、
身份、cgroup 与事实采集能力，不依赖 Bed/Amenity，不解释房型或工作区。领域赋予资源身份、
策略和生命周期，决定是否允许降级，并发布实际状态。API handler 负责寻址、准入与协议转换，
不能拼接内部路径或直接操作资源句柄；原生与 Execd adapter 直接复用领域能力，彼此不依赖。

BedSpec 的显式需求与工作负载语义的隐式需求按领域分治；Subsystem 拥有类型化配置、机制、
资源和状态，少量跨领域需求通过显式接口协作，不引入统一需求字典或通用满足度接口。
机制选择依据 OS/kernel、Node 配置、Carrier/Kubernetes 权限与实际操作验证；单个工具探测
成功不代表完整执行组合可运行。领域自行选择和回退，Hostel 不枚举跨领域候选组合。

### 生命周期与状态

`bed.Subsystem[S]` 统一 daemon 生命周期、单 Bed hooks 与实例级 `Status() S`。Bed Manager
拥有 Spec、Lifecycle 和跨领域协调，各领域唯一写入自己的资源与状态。一个本地生命周期只有
一个共享 Bed；初始化重试、Executor 替换只替换所属资源，请求句柄不另造领域 Spec/Status。
Amenity Manager 直属 daemon，通过 Bed 生命周期适配释放 Tenant。

子系统有三个驱动面：同步 Start/Close、单 Bed 生命周期 hooks、可选 `Runnable.Run` 后台循环。
Start 完成准备，Run 自行管理周期、合并与退避，Close 取消并等待退出。Stop 停止活动，Release
释放运行资源，Forget 在进程、资源、本地数据清理完成后遗忘身份。Bed Manager 显式安排依赖
顺序并统一发布 Ready，局部 Prepare 成功不代表整个 Bed 已准备完成；阶段与重试见
[Bed 生命周期](docs/bed.md#子系统参与生命周期)。

`Status()` 返回实例级探测/选择快照，Bed 级事实由领域 `StatusWriter` 发布。查询不执行探测、
分配、修复或远端 I/O，也不通过共享 map/slice/指针暴露第二个写入口。声明、机制支持、实际采用
与 Bed 落实结果分别表达；未准备或已释放的资源不报告为已落实，各领域状态不要求相同等级或结构。

跨领域协作保留显式接口和顺序。例如 Network 拥有 resolver 与网络 allocation，Filesystem 将其
纳入进程视图，Bed Manager 先准备网络再准备视图，释放时反序执行；不为统一 hook 隐藏依赖。
整体验证失败如实报告并清理，准备/清理失败保留 owner；部分释放时先撤销无法确认的保证。

### 资源分配与持有

资源是被分配的对象；allocation 是某个 owner 在特定作用域内的一次具体分配，包含资源值与
实际状态；lease/handle 是持有、使用和释放 allocation 的句柄。Allocator 选择资源、登记占用并
判定冲突，持有句柄的领域负责使用、交接和最终清理。一次 Bed 活动可持有不同生命周期的 allocation。

- **归属明确**：记录 owner、作用域和资源值，按原句柄清理；旧句柄不能释放同名或同值的新分配。
- **状态真实**：逻辑预留与实际生效分开，例如 port 的 reserved 不等于 listening，也不代表消费者就绪。
- **按依赖释放**：先停止使用者、撤销依赖，再释放 allocation；进程停止是否结束预留由所属领域决定。
- **失败保留责任**：准备失败可能留下部分资源；清理失败保留占用，重试成功前不交给新使用者；重复释放同一 allocation 应安全。
- **恢复由领域负责**：需跨重启保留的身份先记录再使用，按领域记录和宿主事实 reconcile；持久化格式与恢复由对应 owner 维护。

| 资源 | 分配与持有责任 | 释放边界 |
|---|---|---|
| UID / 主 GID | Privilege 管理租约；Bed Manager 保存身份记录并协调 Forget | 运行资源、本地数据与身份记录清理完成后释放，见 [Bed 身份](docs/bed.md#身份与声明) |
| TCP port | PortManager 分配；Network 或服务领域持有句柄，按网络作用域与监听地址判定冲突 | 监听器、进程、转发清理后结束占用，见 [端口归属](docs/network.md#发现归属与回收) |
| netns、网络地址与规则 | Network 管理 allocation 与恢复记录 | 依赖进程、文件视图释放后回收，见 [网络恢复](docs/network.md#allocation-归属与崩溃恢复) |

共同契约由各领域的类型化 allocator、句柄与状态兑现。Resource Subsystem 专门负责 CPU/内存、
cgroup 记账与容量准入，不接管所有资源的分配，见 [资源治理](docs/resource.md)。

## 跨领域主流程

### 实例启动

合并配置 → 构造子系统 → 恢复本地身份与清理责任 → 探测并验证执行组合 → 开放 API。
配置入口按显式 Options > CLI > HOSTEL_* env > 默认值生成确定的 Config，区分未指定与显式零值；
子系统只读自己的配置，完整配置不进入状态响应。启动失败逆序关闭已准备子系统，正常运行由各
领域维护同步、采样与回收循环。工具策略见 [隔离](docs/isolation.md#能力选择与工具策略)，
Pillow 的业务环境配置见 [配置来源](docs/pillow.md#配置来源)。

### Bed 初始化与执行

```text
声明 Bed → 预占容量、合并同名初始化
  → 恢复并验证卷数据 → 准备身份、网络、资源与文件视图
  → 解析 Pillow 配置、启动必需主进程 → Ready
  → operation / session 准入 → 领域操作 → 输出与终态
```

数据访问只等待已声明的 Bed；准备完成前不开放数据面，远端恢复错误不能当作空 Bed。
执行使用所属 Pillow 环境，可信准备完成并降权后才运行用户程序。已接纳工作受生命周期保护，
状态查询不保活。详细准入见 [Bed](docs/bed.md#bed-初始化与请求准入)，进程模型见 [Pillow](docs/pillow.md#命令session-与进程承载)。

### 同步、回收与恢复

```text
本地读写 → Store 合并同步触发 → 发布持久卷快照
Bed 回收 → 关闭准入 → 停止写入 → 最终同步
  ├─ 失败：保留本地数据与清理责任
  └─ 成功：释放资源 → 删除本地数据 → 释放本地身份
再次创建 → 恢复持久卷、重建临时数据与运行环境
```

持久卷共同发布一个 generation；环境配置、进程和共享设施运行态不进入快照。同步不是实时共享
文件系统，上层须保证同名 Bed 的单写者归属，数据契约见 [Volume](docs/volumes.md#自动持久化)。
Purge 显式销毁本地及远端数据；daemon 正常关闭保留本地数据供恢复。

实例关闭先撤销准入，取消并等待后台与初始化任务，再清理 Bed 运行资源、逆序关闭子系统和设施。
上层根据实例事实决定释放 Carrier，Hostel 不因空闲自行退出；保留期限与失败重试见 [Bed](docs/bed.md#关闭与实例释放)。

## 关键约定

- 全局错误契约由 `internal/error.go` 拥有，领域按 `errors.Is/As` 传递分类和 cause；协议映射留在各 API，详见 [错误契约](docs/protocol.md)。

- **bed = 客人单元 = 对外一个 sandbox**（独立文件空间 + 执行环境，状态跨命令保持）；**房型是 Bed 文件与网络隔离保证的综合分档**——bed 是跨档不变的基本单位，房型不替代 bed 命名（见 `docs/isolation.md`）。
  - **显式 Bed 生命周期**：daemon 空载启动，由调用方声明 Bed；数据请求必须显式选择 Bed。容量、inventory、准入和回收统一以 Bed 生命周期为依据。
  - **生命周期事实分维度**：
    - inventory 的 `status.phase=initializing|resident|evicting|purging|dormant|failed` 表达 Bed 所处阶段，`status.readiness` 表达能否服务；Bed 详情将这组事实置于 `status.lifecycle`。
    - resident / evicting Bed 的 `status.activity=active|idle` 由 operation 数量派生。
    - 容量讨论把正交事实组合成互斥的具体状态，再投影为 `pinned_beds ⊆ resident_beds ⊆ occupied_beds`；不把 initializing 混入 `activity_counts`。
    - `generation` 表达数据版本，`retained_until` 表达最早安全回收期限。
    - Bed 级请求分 operation 与 session 两类，详见 `docs/bed.md`。
  - **luggage**：正常 evict 在 durable/noop 下都删除本地 Bed 目录；durable 可从快照恢复，noop 再次初始化则从空目录开始。luggage 扫描只处理异常退出遗留目录，详见 `docs/bed.md`。
  - **bed 容量准入**：
    - `occupied_beds` 包含 initializing 与 resident/evicting Bed，`--max-beds` 是其唯一数量硬上限。
    - 初始化在任何 Store I/O 前预占名额，避免并发 Stage-in 穿透。
    - `--bed-pressure-threshold-percent` 默认 80，`occupied/max-beds` 或 `pinned/max-pinned-beds` 任一达水位即上报软 `bed_pressure`。
    - `max-pinned-beds` 不是准入限制，超过也不返回 429；CPU/内存 pressure 仍单独执行资源准入。
    - Hostel 只上报事实，不自行选择 carrier；同步 trigger 的节奏、合并与重试由 Store 同步循环统一负责，详见 `docs/resource.md` / `docs/volumes.md`。
- **FS 访问规则与状态**：默认临时数据归 BedHome，尽量映射为 `/tmp`；无映射能力时 TMPDIR 指向私有宿主路径。显式 Volume 可替换或共享临时空间，HostPath 权限与清理仍归提供方。FS 当前以 Bed 文件视图为基础引入公共资源，来源权限由提供方准备；`bedfs.CommonDirectories/ProcessDirectories` 拥有公共路径与访问方式声明；Volume/Mount 继续拥有显式来源授权。各机制尽力兑现同一需求，不能通过宽泛授权父目录补救私有保护失败。实例 `Subsystem.Status()` 报告探测/选择，Bed `filesystem.realization` 报告实际准备/清理快照；部分释放时撤销具体保证并保留 owner。见 `docs/isolation.md`。
- **执行层次是 `Bed → Executor → Execution`**：Bed 是 sandbox 的持久身份；Executor 是当前可替换的进程域；Execution 是一次运行。Executor 丢失只终结归属它的进程，不丢 Bed 数据，下一次请求在旧 Executor 清理成功后创建新 Executor。每次前台、后台或 session run 都生成 `Execution`；`execution_start` 先于输出，之后恰有一个 `execution_end`。命令准备失败时无 `ProcessOutcome`，以领域错误和 `preparation_failed` 表达；实际运行的 `ProcessOutcome` 表达 exited / signaled / lost，termination cause 独立表达 timeout / cancel / interrupt / teardown / executor_lost，禁止再用裸 EOF、`-1` 或错误字符串承载多种语义。
- **Trace 是生命周期事实的投影**：HTTP 使用路由模板 span，bed initialize/persist/evict 与 execution 使用稳定领域 span，stage 只记 event；不得把 command、env、stdout/stderr 写入 span。后台 initialization / execution 继承 trace identity 但不继承 HTTP cancel。详见 `docs/observability.md`。
- **隔离按 Bed 的统一目标尽量兑现**：各 domain 拥有 facts、能力状态、配置选择与有限回退；房型由 fs/net 的实际等级较低值决定，其他能力独立增强。Subsystem 组合生命周期和类型化 Status。各子系统自行选择和回退，Hostel 只协调依赖及验证实际执行，不枚举跨领域组合；required 不丢弃，清理失败终止；live Bed 不重选。当前缺口见 `docs/isolation.md` 与 `docs/backlog.md`。
  - daemon 身份与 BedUser 正交：command/session/Pillow 主进程使用 resident Bed 的同一身份；Privilege 独立决定 shared/dedicated，Filesystem 不分配 UID。优先分配独立 UID/主 GID，条件不足时经验证回退并披露实际结果；内部共享软件组仅使用提供方已授予的权限，FS 不递归修改公共目录权限，无固定 UID/GID 或 extra GIDs 配置入口。Linux 子进程仍须通过 capability 清理与 no_new_privs 验证。见 `docs/isolation.md`。
  - BedFS 路径映射在所有档位一致；PRoot/pathshim 改善进程路径体验，不提供安全边界，也不提高文件隔离档位。
  - Volume.managed.persistence 声明完整卷的持久化策略；Store 按卷名发布 Bed 级快照，详见 `docs/volumes.md`。
- **amenity 通则**：共享设施按 Bed 分配应用状态，产物落对应 Bed 的默认 Pillow Workdir；设施状态、Bed 级凭据与 Bed 生命周期分别管理。北向使用 Bed 级动作或受限代理，不裸透传共享设施的管理协议。应用切分不等于文件、网络或资源完整隔离，当前机制与缺口见 `docs/amenity.md`。
- **常驻 shell 的坑**：一个 Shell 只能有**一个** stdout reader（否则 run 间串输出——v1 踩过）；Run 之间串行；Shell 持有启动时的 Executor View，session run 的 cwd 必须经 `RunAt` 投影并作为独立控制步骤执行，禁止 Web 拼接 `cd` 或 Executor path；`exit` 会杀死 session，非零退出码用子 shell（`sh -c "exit N"`）。**锁纪律**：`runMu` 串行化 Run 且只有 Run 碰；`mu` 只护 `dead` 标志、纳秒级持有——曾因单锁设计让「shell 死亡+未断开客户端」死锁整个 daemon（含 healthz），别往 `mu` 里加阻塞代码（见 shell.go LOCKING 注释）。
- **配置在启动入口收敛**：显式 Options > CLI > env > 默认值，子系统只读确定的 Config；指针区分未指定与显式零值。按 Subsystem → Tool → Requirements 组织选择与诊断，Tool 使用 auto/off/required，Linux Capabilities 属于实现前提。内部配置不自动扩展成公开参数，见 `docs/isolation.md`。
- **E2E owner 边界**：Hostel 的单机 suite 直接验证真实 daemon/image 的 bed runtime、隔离与 carrier userland；上层控制面只保留 placement、跨 carrier 持久化和 lifecycle 编排，不在 K8s E2E 重复证明 Hostel 内部契约。运行说明见 `tests/e2e/README.md`。
- Go 项目常规：改完 `go build ./...` + `go test ./...` + `go vet ./...` 三件套过再提交（见 `Makefile`）。仓库在 `github.com/qiankunli/hostel`，保护分支 main 走 PR。
- 根目录 `VERSION` 是二进制和镜像的唯一版本源；改变运行时行为时递增版本号，默认递增 patch。仅修改测试用例、测试 fixture、测试报告或文档时不递增。
- **S3 上传源稳定性**：最终回收同步先停止受管写入者；自动持久化上传已完成且传输期间不再修改的 pack/chunk 或临时归档。显式 Transfer 由调用方保证源文件稳定。Hostel 不处理边上传边改写源文件，不为此实现通用 S3 传输保护；详见 [Volume](docs/volumes.md#pack-object-大小与-s3-上传分片)。
- 通用小工具优先用 [go-stdx](https://github.com/qiankunli/go-stdx)（env 解析、随机 id、shell quote、原子写文件、目录字节数等），不要在仓内再手写它已有的操作；沉淀出的新通用件也应迁去 go-stdx 而非留在 internal。

## References

全局模型、职责和主流程由本文件维护；领域细节在以下文档维护，避免重复定义同一契约。

- Bed 身份、声明、准入、保活与回收：[bed.md](docs/bed.md)
- Pillow 配置、命令、Session、主进程与 Executor：[pillow.md](docs/pillow.md)
- Volume、文件视图、持久化与显式传输：[volumes.md](docs/volumes.md)
- 隔离、权限、Tool 策略与能力降级：[isolation.md](docs/isolation.md)
- 网络、端口发现与出站策略：[network.md](docs/network.md)
- 容量、资源记账、准入与限制：[resource.md](docs/resource.md)
- 共享设施、Chromium 与 MCP：[amenity.md](docs/amenity.md)
- 对外协议、Execd 与错误映射：[protocol.md](docs/protocol.md)
- 状态、日志、Trace 与 Metric：[observability.md](docs/observability.md)
- 未交付能力：[backlog.md](docs/backlog.md)
- 使用入口：[README.md](README.md)
- 单机 E2E 与覆盖边界：[tests/e2e/README.md](tests/e2e/README.md)
- E2E Environment 与 runner：[e2e-environments.md](tests/e2e/e2e-environments.md)
- OpenSandbox 参考实现归属：[NOTICE](NOTICE)
