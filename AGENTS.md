# hostel

## 项目定位与边界

**面向 AI agent 的 sandbox runtime**：在一台机器 / 一个容器内管理多个隔离执行单元（**bed**）。资源与文件 API 以 OpenSandbox 为设计基线，原生执行协议由 hostel 自己拥有；`/execd` 与 Bed 路径入口在 API 层适配 OpenSandbox，见 `docs/protocol.md`。形态上 daemon 组装 **Web Server、Bed Manager、Amenity Manager**；Bed Manager 以唯一 Bed 模型组合驱动各领域 Manager。可单机跑（laptop/VM/CI），也可作为多租户共享实例的 in-process runtime，由上层调度系统按 `sandbox_id → (实例, bed)` 路由驱动。

**产品背景**：Hostel / Bed 可类比轻量、尽力实现的 kubelet / Pod：在受限的 Host/Pod 上，从 API 视角提供尽可能接近 Container 的使用体验。文件与执行等 API 优先完成用户任务，底层缺少某项机制（如 cgroup）不应自动使整个 Bed 不可用；领域可通过路径解析、候选读取等方式补齐体验，并分别报告功能可用性与实际隔离保证。产品取舍见 [核心架构](docs/kernel.md#定位与目标)，文件路径的具体语义见 [Volume 与文件视图](docs/volumes.md)。

**尽力启动的边界**：Bed Spec 表达需求，Hostel 根据宿主能力落实；未要求的可选能力缺失不应阻止启动，明确诉求无法满足则必须失败，不得静默忽略。未声明部分的机制选择与尽力程度随实现演进，不构成固定隔离承诺；状态披露实际能力与缺口。按需启用与失败语义见 [核心架构](docs/kernel.md#定位与目标)。

- **做**：bed 生命周期、exec / file、共享多租服务（Chromium/Jupyter/MCP…）管理。
- **不做**（留给上层调度系统）：实例调度、跨实例路由、计费配额。
- 参考 OpenSandbox execd（Apache-2.0）净重写，非其 fork；归属见 `NOTICE`。设计见 `docs/kernel.md`，未交付项见 `docs/backlog.md`。

## 概念与命名约定

以下名词全仓（代码 / 注释 / 文档）统一使用，避免同物多名、一名多物：

- **bed**：隔离执行单元，对外即一个 sandbox（独立文件空间 + 执行环境，状态跨命令保持）。
- **bed name**：调用方给定的不透明路由标识，支持中文；HTTP 请求的 bed/id 参数及 `BED_ID` 都表达 Name；管理响应同时返回 `name` 与本地生命周期 `id`，缺省为 `default`。Hostel 不解释其上层业务语义。
- **bed id**：Hostel 生成的本地身份；本地数据保留期间跨重启恢复，Forget 后同名创建换 ID。领域资源以 ID 归属，远端快照以 Name/快照引用定位。
- **beds-root**：所有 bed 目录的**父目录**，**可配、不写死**（`--beds-root` / `HOSTEL_BEDS_ROOT`，默认 `/workspace`）；**daemon 启动时创建一次**。
- **bed 目录**：`{beds-root}/{bed name}`，含 `meta.json`（可移植身份）、`rootfs/` 与 `volumes/`；由 `InitializeBed` 异步准备。只有 Store Stage-in/Restore 与 BedFS/isolation 准备全部完成后才发布 Ready，原生数据面通过 `Ensure` 等待已经显式声明的 Bed，缺失返回 not found，详见 `docs/bed.md`。
- **BedFS**：Bed 持有的文件系统数据域；提供 Bed Rootfs、Volume 数据与各 Pillow 的 Workdir/Mounts 视图、三个路径空间及文件操作。Executor 替换不改变 BedFS 身份，详见 `docs/volumes.md`。
- **bed_home（rootfs 目录）**：BedFS 的宿主根 `{bed 目录}/rootfs`——**Bed 自身数据根**；未命中外部映射的客户端路径解析到它下面，回显保持 Bed 路径。它不参与自动持久化，也不表示进程已有独立的 `/`。
- **Workdir**：Pillow 默认工作目录，当前为 `/workspace`（`bedfs.DefaultWorkdir`），也是 API 相对路径的解析基准；具体进程的 cwd 可以覆盖。Rootfs、Workdir 与 Volumes/Mounts 的关系见 `docs/volumes.md`。
- **Volume / Mount**：Bed 声明命名数据来源（managed / host_path）；每个 Pillow 声明自己的 mounts，所属 command、session、主进程和文件 API 共用该视图。共享数据与执行视图分别管理，见 [Volume 与文件视图](docs/volumes.md#来源与挂载)。
- **房型（dorm / room / suite）**：以 Bed 文件隔离保证为代表特征的用户分档；其他领域能力独立报告，默认增强偏好不参与房型评级（见 `docs/isolation.md`）。
- **luggage**：非正常生命周期状态，只表达异常退出遗留的本地 Bed 目录。正常 evict 在任意 Store backend 下都删除本地目录。
- **amenity**：bed 外由 hostel 统一管理、按 bed 分配状态的共享设施（Chromium / Jupyter / MCP 连接池）。
- **pillow**：Bed 内的命名执行环境，拥有 env、workdir、mounts 与可选主进程，与用户命令共用 Bed Environment/Executor；创建者提交通用完整 `PillowSpec`，Carrier 镜像提供程序与依赖，Hostel 不依赖具体业务服务。端口通过 daemon 级 Port Manager 统一申请，见 `docs/pillow.md`。
- **executor**：某个 bed 当前的、可替换的进程承载域。bed 持久存在，executor 丢失或关闭后可用新 id 重建；Linux 默认使用 supervisor backend，非 Linux / 显式 local 使用 daemon 直接派生。
- **execution**：一次命令运行。每次有独立 id，且记录其所属 bed id、pillow name 与 executor id。
- **bedinit**：每次进程启动前的可信初始化路径，按 Bed Manager 绑定的方案进入环境、降权并 exec 用户程序；不是 Bed 生命周期初始化或常驻进程，详见 `docs/pillow.md`。

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
未命中当前 mounts 的客户端路径：/tmp/x → rootfs/tmp/x；相对路径 = Workdir 相对
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

Bed 的理想语义是独立执行空间，文件、进程、网络和资源按环境能力尽量隔离，并如实披露实际共享与缺口。房型归纳实际文件保证，不等于所有维度完整隔离；所有档位保持同一 BedFS 路径归属。目标、机制组合与实际保证见 `docs/isolation.md`。

独占 Dorm carrier 可显式开启只读 file API 的进程根回退：BedFS 路径不存在且客户端传入绝对路径时，Reader 回读配置的进程根；BedFS 同名路径优先，mutation 不回退。该配置会暴露进程根、共享 carrier 禁止开启，详见 `docs/volumes.md`。

## 代码地图与核心模块

```
deploy/
├── docker/Dockerfile  多阶段多架构镜像(amd64/arm64,builder 原生交叉编译免 QEMU)：静态 hostel + debian-slim（内置 bwrap/PRoot/pathshim + 可选 chromium）；tini PID1；hostel --health 做 HEALTHCHECK
└── k8s/              Kubernetes 部署示例；AppArmor 的 PSA 豁免申请见 pod-security-admission-exemption.yaml
protocol/             独立 Go module（最低 Go 1.25）；server 与 caller 共用的 Bed 数据契约，仅依赖标准库
cmd/hostel/main.go     daemon 入口：配置、组件组装、启动探测、Web 与信号；驱动 Manager 启停
tests/e2e/             真实进程/镜像 E2E：同一 suite 可在 Host 或 K8s Pod 内运行；环境配置与事实入口，见 tests/e2e/e2e-environments.md
internal/
├── bed/               唯一 Bed 模型（Spec / 分域 Status）及生命周期契约；不导入领域实现
│   ├── manager/       Composite：准入、初始化/回收顺序、operation/session、执行环境组装与诊断聚合
│   │   ├── bed.go     managedBed / Resident：请求操作句柄与并发协调，引用共享 Bed
│   │   ├── daemon.go  Start / Run / Close：组件启动回滚、后台循环取消与 join
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

**数据流**：请求 →`api/apiv1/handler` 或 `api/execd/handler` 按 `X-Hostel-Bed`(缺省 default) 解析 bed → 调 `bed`/`bedfs` 核心 → 组装响应（命令走 SSE）。核心模型与领域组件**不含任何 HTTP 类型或协议族编码**；两套 adapter 不互相依赖，复用能力进入 `internal/bed` 等 owner。换框架只动 `api/`。

## 关键约定

- 全局错误契约由 `internal/error.go` 拥有，领域按 `errors.Is/As` 传递分类和 cause；协议映射留在各 API，详见 [错误契约](docs/protocol.md)。

- **bed = 客人单元 = 对外一个 sandbox**（独立文件空间 + 执行环境，状态跨命令保持）；**房型是 Bed 文件隔离保证的用户分档**——bed 是跨档不变的基本单位，房型不替代 bed 命名（见 `docs/isolation.md`）。
  - **默认 bed 兜底**：不带 bed 的原生请求落 `default`，单租户调用方可无视 bed 概念；default bed 不暴露为 isolated session，永不被清数据、不可 purge、不占任何 bed 数量名额。
  - **生命周期事实分维度**：
    - inventory 的 `status.phase=initializing|resident|evicting|purging|dormant|failed` 表达 Bed 所处阶段，`status.readiness` 表达能否服务；Bed 详情将这组事实置于 `status.lifecycle`。
    - resident / evicting Bed 的 `status.activity=active|idle` 由 operation 数量派生。
    - 容量讨论把正交事实组合成互斥的具体状态，再投影为 `pinned_beds ⊆ resident_beds ⊆ occupied_beds`；不把 initializing 混入 `activity_counts`。
    - `generation` 表达数据版本，`retained_until` 表达最早安全回收期限。
    - Bed 级请求分 operation 与 session 两类，详见 `docs/bed.md`。
  - **luggage**：正常 evict 在 durable/noop 下都删除本地 Bed 目录；durable 可从快照恢复，noop 再次初始化则从空目录开始。luggage 扫描只处理异常退出遗留目录，详见 `docs/bed.md`。
  - **bed 容量准入**：
    - `occupied_beds` 包含 initializing 与 resident/evicting tenant Bed，`--max-beds` 是其唯一数量硬上限。
    - 初始化在任何 Store I/O 前预占名额，避免并发 Stage-in 穿透。
    - `--bed-pressure-threshold-percent` 默认 80，`occupied/max-beds` 或 `pinned/max-pinned-beds` 任一达水位即上报软 `bed_pressure`。
    - `max-pinned-beds` 不是准入限制，超过也不返回 429；CPU/内存 pressure 仍单独执行资源准入。
    - Hostel 只上报事实，不自行选择 carrier；同步 trigger 的节奏、合并与重试由 Store 同步循环统一负责，详见 `docs/resource.md` / `docs/volumes.md`。
- **执行层次是 `Bed → Executor → Execution`**：Bed 是 sandbox 的持久身份；Executor 是当前可替换的进程域；Execution 是一次运行。Executor 丢失只终结归属它的进程，不丢 Bed 数据，下一次请求在旧 Executor 清理成功后创建新 Executor。每次前台、后台或 session run 都生成 `Execution`；`execution_start` 先于输出，之后恰有一个 `execution_end`。命令准备失败时无 `ProcessOutcome`，以领域错误和 `preparation_failed` 表达；实际运行的 `ProcessOutcome` 表达 exited / signaled / lost，termination cause 独立表达 timeout / cancel / interrupt / teardown / executor_lost，禁止再用裸 EOF、`-1` 或错误字符串承载多种语义。
- **Trace 是生命周期事实的投影**：HTTP 使用路由模板 span，bed initialize/persist/evict 与 execution 使用稳定领域 span，stage 只记 event；不得把 command、env、stdout/stderr 写入 span。后台 initialization / execution 继承 trace identity 但不继承 HTTP cancel。详见 `docs/observability.md`。
- **隔离按 Bed 的统一目标尽量兑现**：各 domain 拥有 facts、能力状态、配置选择与有限回退；房型只由文件保证决定，其他能力独立增强。Component 组合生命周期和类型化 Status。启动组合验证允许有限回退，required 不丢弃，清理失败终止；live Bed 不重选。当前缺口见 `docs/isolation.md` 与 `docs/backlog.md`。
  - daemon 身份与 BedUser 正交：command/session/Pillow 主进程使用 resident Bed 的同一身份；Privilege 独立决定 shared/dedicated，Filesystem 不分配 UID。配置的 UID/GID 是建议值，不可用时可经验证继承 daemon 身份，并披露实际结果；Linux 子进程仍须通过 capability 清理与 no_new_privs 验证。见 `docs/isolation.md`。
  - BedFS 路径映射在所有档位一致；PRoot/pathshim 改善进程路径体验，不提供安全边界，也不提高文件隔离档位。
  - Volume.managed.persistence 声明完整卷的持久化策略；Store 按卷名发布 Bed 级快照，详见 `docs/volumes.md`。
- **amenity 通则**：共享设施按 Bed 分配应用状态，产物落对应 Bed 的默认 Pillow Workdir；设施状态、Bed 级凭据与 Bed 生命周期分别管理。北向使用 Bed 级动作或受限代理，不裸透传共享设施的管理协议。应用切分不等于文件、网络或资源完整隔离，当前机制与缺口见 `docs/amenity.md`。
- **常驻 shell 的坑**：一个 Shell 只能有**一个** stdout reader（否则 run 间串输出——v1 踩过）；Run 之间串行；Shell 持有启动时的 Executor View，session run 的 cwd 必须经 `RunAt` 投影并作为独立控制步骤执行，禁止 Web 拼接 `cd` 或 Executor path；`exit` 会杀死 session，非零退出码用子 shell（`sh -c "exit N"`）。**锁纪律**：`runMu` 串行化 Run 且只有 Run 碰；`mu` 只护 `dead` 标志、纳秒级持有——曾因单锁设计让「shell 死亡+未断开客户端」死锁整个 daemon（含 healthz），别往 `mu` 里加阻塞代码（见 shell.go LOCKING 注释）。
- **配置在启动入口收敛**：显式 Options > CLI > env > 默认值，组件只读确定的 Config；指针区分未指定与显式零值。按 Component → Tool → Requirements 组织选择与诊断，Tool 使用 auto/off/required，Linux Capabilities 属于实现前提。内部配置不自动扩展成公开参数，见 `docs/isolation.md`。
- **E2E owner 边界**：Hostel 的单机 suite 直接验证真实 daemon/image 的 bed runtime、隔离与 carrier userland；上层控制面只保留 placement、跨 carrier 持久化和 lifecycle 编排，不在 K8s E2E 重复证明 Hostel 内部契约。运行说明见 `tests/e2e/README.md`。
- Go 项目常规：改完 `go build ./...` + `go test ./...` + `go vet ./...` 三件套过再提交（见 `Makefile`）。仓库在 `github.com/qiankunli/hostel`，保护分支 main 走 PR。
- 根目录 `VERSION` 是二进制和镜像的唯一版本源；改变运行时行为时递增版本号，默认递增 patch。仅修改测试用例、测试 fixture、测试报告或文档时不递增。
- 通用小工具优先用 [go-stdx](https://github.com/qiankunli/go-stdx)（env 解析、随机 id、shell quote、原子写文件、目录字节数等），不要在仓内再手写它已有的操作；沉淀出的新通用件也应迁去 go-stdx 而非留在 internal。

## References

- 系统总览与 domain 阅读入口：[kernel.md](docs/kernel.md)
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
