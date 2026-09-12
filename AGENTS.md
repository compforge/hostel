# hostel

## 项目定位与边界

**面向 AI agent 的 sandbox runtime**：在一台机器 / 一个容器内管理多个隔离执行单元（**bed**）。资源与文件 API 以 OpenSandbox 为设计基线，执行协议由 hostel 自己拥有。形态上 daemon 组装 **Web Server、Bed Manager、Amenity Manager**；Bed Manager 以唯一 Bed 模型组合驱动各领域 Manager。可单机跑（laptop/VM/CI），也可作为多租户共享实例的 in-process runtime，由上层调度系统按 `sandbox_id → (实例, bed)` 路由驱动。

- **做**：bed 生命周期、exec / file、共享多租服务（Chromium/Jupyter/MCP…）管理。
- **不做**（留给上层调度系统）：实例调度、跨实例路由、计费配额。
- 参考 OpenSandbox execd（Apache-2.0）净重写，非其 fork；归属见 `NOTICE`。设计见 `docs/kernel.md`，未交付项见 `docs/backlog.md`。

## 概念与命名约定

以下名词全仓（代码 / 注释 / 文档）统一使用，避免同物多名、一名多物：

- **bed**：隔离执行单元，对外即一个 sandbox（workspace + 常驻 shell，状态跨命令保持）。
- **bed name**：调用方给定的不透明路由标识，支持中文；HTTP 现有的 bed/id 参数及 `BED_ID` 都表达 Name，缺省为 `default`。Hostel 不解释其上层业务语义。
- **bed id**：Hostel 生成的本地身份；本地数据保留期间跨重启恢复，Forget 后同名创建换 ID。领域资源以 ID 归属，远端快照以 Name/快照引用定位。
- **workspace-root**：所有 bed 目录的**父目录**，**可配、不写死**（`--workspace-root` / `HOSTEL_WORKSPACE_ROOT`，默认 `/workspace`）；**daemon 启动时创建一次**。
- **bed 目录**：`{workspace-root}/{bed name}`，含 `meta.json`（可移植身份）+ `data/`；由 `InitializeBed` 异步准备。只有 Store Stage-in/Restore 与 BedFS/isolation 准备全部完成后才发布 Ready，原生数据面首次请求通过 `Ensure` 加入同一初始化并等待，详见 `docs/kernel.md`。
- **BedFS**：Bed 持有的文件系统数据域；统一拥有 bed_home、workspace、客户端/宿主/Executor 三个路径空间与文件操作。Executor 替换不改变 BedFS 身份，详见 `docs/filesystem.md`。
- **bed_home（data 目录）**：BedFS 的宿主根 `{bed 目录}/data`——**客户端视角的 `/`**，任意客户端绝对路径单射 rebase 到它下面、回显对称；bed 只见它，但它不整体持久化。
- **bed workspace**：`bed_home/workspace` 真实子目录（非别名）——OpenSandbox 契约的 `/workspace`（`bedfs.WorkspacePath`）、相对路径的基准、默认 cwd、suite 下的真实挂载点，也是 `HOSTEL_PERSISTED_PATHS` 默认唯一持久化的数据子树。
- **path projection**：调用方配置的通用 `BedFS path → Executor path` 投影；可配置多个，Hostel 不解释路径的业务含义。`/workspace` 是内置投影，不通过该配置声明。
- **房型（dorm / room / suite）**：bed 的文件隔离档，与 bed 正交（见〈关键约定〉isolation）。
- **luggage**：非正常生命周期状态，只表达异常退出或旧版 Hostel 遗留的本地 Bed 目录。正常 evict 在任意 Store backend 下都删除本地目录。
- **amenity**：bed 外由 hostel 统一管理、按 bed 分配状态的共享设施（Chromium / Jupyter / MCP 连接池）。
- **executor**：某个 bed 当前的、可替换的进程承载域。bed 持久存在，executor 丢失或关闭后可用新 id 重建；Linux 默认使用 supervisor backend，非 Linux / 显式 local 使用 daemon 直接派生。
- **execution**：一次命令运行。每次有独立 id，且记录其所属 bed id 与 executor id。

**进程模型**（进程归属树；详见 `docs/kernel.md`〈进程树〉）：

```
tini (pid1)                       pod 级收尸兜底
└─ hostel (daemon)                内置 amenity supervisor，无独立 manager 进程
   ├─ chromium / jupyter          amenity：pod 级共享、按 bed 切租，不进任何 bed 树
   └─ bed [持久身份]
      └─ executor [当前 0/1，可替换]
         └─ supervisor            Linux backend：fork / 收尸 / 死前杀整树
            ├─ execution process  一次 command 一个
            └─ session shell      /session 显式常驻
```
`Hostel → Bed → Executor → Execution` 是领域层次；supervisor / local 只是 Executor backend。

**路径模型**（客户端 `/` = `bed_home`，映射单射、回显对称；调用方以 `capabilities.workspace_mount` 探测挂载语义）：

```
客户端任意路径：/workspace/x → data/workspace/x；/tmp/x → data/tmp/x；相对路径 = workspace 相对
   │  BedFS.Resolve：单射 rebase（回显为其逆映射）
   ▼
<workspace-root>/                 宿主侧，所有 bed 父目录；可配 HOSTEL_WORKSPACE_ROOT，默认 /workspace，daemon 启动建
└─ <bed name>/                      bed 目录；InitializeBed / Ensure 首次初始化时创建
   ├─ meta.json                   可移植身份
   └─ data/                       bed_home（客户端的 /）；不整体进快照
      ├─ workspace/               OpenSandbox workspace；默认唯一持久化数据子树
      └─ <other>/                 运行期数据；可按配置投影，不进 Store 快照

Executor View：
  suite       → 整个 bed_home 内部挂载 + workspace 和配置项的规范投影
  dorm/room   → 独立探测 PRoot/pathshim，按 PRoot → pathshim → Carrier 选择进程视图
```

Bed 的理想语义是独立执行空间，文件、进程、网络和资源按环境能力尽量隔离，并如实披露实际共享与缺口。文件房型只描述文件边界，不代表全维度隔离；所有档位保持同一 BedFS 路径归属。目标、机制组合与实际保证见 `docs/isolation.md`。

独占 Dorm carrier 可显式开启只读 file API 的进程根回退：BedFS 路径不存在且客户端传入绝对路径时，Reader 回读配置的进程根；BedFS 同名路径优先，mutation 不回退。该配置会暴露进程根、共享 carrier 禁止开启，详见 `docs/filesystem.md`。

## 代码地图与核心模块

```
deploy/
├── docker/Dockerfile  多阶段多架构镜像(amd64/arm64,builder 原生交叉编译免 QEMU)：静态 hostel + debian-slim（内置 bwrap/PRoot/pathshim + 可选 chromium）；tini PID1；hostel --health 做 HEALTHCHECK
└── k8s/              Kubernetes 部署示例；AppArmor 的 PSA 豁免申请见 pod-security-admission-exemption.yaml
cmd/hostel/main.go     daemon 入口：配置、组件组装、启动探测、Web 与信号；驱动 Manager 启停
tests/e2e/             单机真实进程/镜像 E2E：公开 API、bed runtime/isolation 与可选 carrier userland；不经过 sandctl/K8s
internal/
├── bed/               唯一 Bed 模型（Spec / 分域 Status）及生命周期契约；不导入领域实现
│   ├── manager/       Composite：准入、初始化/回收顺序、operation/session、执行环境组装与诊断聚合
│   │   ├── bed.go     managedBed / Resident：请求操作句柄与并发协调，引用共享 Bed
│   │   ├── daemon.go  Start / Run / Close：组件启动回滚、后台循环取消与 join
│   │   ├── resident.go 初始化各领域资源，全部完成后由初始化入口发布 Ready
│   │   └── local_identity.go 本地身份与清理占位；目录删除完成后才 Forget
│   ├── filesystem/    BedFS 与文件隔离的 owner，提供文件领域诊断
│   │   ├── bedfs/     数据根、路径投影及文件 API 的实现
│   │   └── isolation/ 文件边界和进程文件视图的机制、探测与降级
│   ├── privilege/     BedUser、UID 租约、ownership、降权与权限报告
│   ├── network/       可选 per-Bed netns、出站策略与精确 allocation 句柄
│   ├── store/         Stage-in、持久化、Transfer 及自动同步循环
│   │   ├── sync/      noop/auto/cas/pack/tar/copy/restic 同步策略
│   │   └── backend/   S3 位置、共享客户端和对象操作
│   ├── executor/      可替换进程域的 Manager、local / supervisor backend
│   └── resource/      cgroup accounting、carrier admission 与采样循环；未施加 per-Bed limits
├── amenity/           daemon 直属共享设施；Bed Manager 只驱动 Bed 切片 hook
├── supervisor/        Linux executor 的 IPC、进程派生与收尸
├── config/            flags + HOSTEL_* env
├── tracing/           OpenTelemetry 与 trace/log 关联
└── web/               HTTP 薄入口；Bed 请求统一经过 Bed Manager，实例级设施操作交给 Amenity
```

**数据流**：请求 →`web` 按 `X-Hostel-Bed`(缺省 default) 解析 bed → 调 `bed`/`bedfs` 核心 → 响应（命令走 SSE）。核心模型与领域组件**不含任何 HTTP 类型**，换框架只动 `web/`。

## 关键约定

- **bed = 客人单元 = 对外一个 sandbox**（workspace + 常驻 shell，状态跨命令保持）；**房型(dorm/room/suite)是这张床的文件隔离档、与 bed 正交**——bed 是跨档不变的基本单位，房型只描述"床周围的墙"有多严，不替代 bed 命名（见 `docs/isolation.md`）。
  - **默认 bed 兜底**：不带 bed 的原生请求落 `default`，单租户调用方可无视 bed 概念；default bed 不暴露为 isolated session，永不被清数据、不可 purge、不占任何 bed 数量名额。
  - **生命周期事实分维度**：
    - `status.phase=initializing|resident|evicting|purging|dormant|failed` 表达 Bed 所处阶段，`status.readiness` 表达能否服务。
    - resident / evicting Bed 的 `status.activity=active|idle` 由 operation 数量派生。
    - 容量讨论把正交事实组合成互斥的具体状态，再投影为 `pinned_beds ⊆ resident_beds ⊆ occupied_beds`；不把 initializing 混入 `activity_counts`。
    - `generation` 表达数据版本，`retained_until` 表达最早安全回收期限。
    - Bed 级请求分 operation 与 session 两类，详见 `docs/kernel.md`。
  - **luggage**：正常 evict 在 durable/noop 下都删除本地 Bed 目录；durable 可从快照恢复，noop 再次初始化则从空目录开始。luggage 扫描只处理异常退出/旧版遗留目录，详见 `docs/store.md` §四。
  - **bed 容量准入**：
    - `occupied_beds` 包含 initializing 与 resident/evicting tenant Bed，`--max-beds` 是其唯一数量硬上限。
    - 初始化在任何 Store I/O 前预占名额，避免并发 Stage-in 穿透。
    - `--bed-pressure-threshold-percent` 默认 80，`occupied/max-beds` 或 `pinned/max-pinned-beds` 任一达水位即上报软 `bed_pressure`。
    - `max-pinned-beds` 不是准入限制，超过也不返回 429；CPU/内存 pressure 仍单独执行资源准入。
    - Hostel 只上报事实，不自行选择 carrier；同步 trigger 的节奏、合并与重试由 Store 同步循环统一负责，详见 `docs/resource.md` / `docs/store.md`。
- **执行层次是 `Bed → Executor → Execution`**：Bed 是 workspace / sandbox 的持久身份；Executor 是当前可替换的进程域；Execution 是一次运行。Executor 丢失只终结归属它的进程，不丢 Bed 数据，下一次请求在旧 Executor 清理成功后创建新 Executor。每次前台、后台或 session run 都生成 `Execution`；`execution_start` 先于输出，之后恰有一个 `execution_end`。`ProcessOutcome` 表达 exited / signaled / lost，termination cause 独立表达 timeout / cancel / interrupt / teardown / executor_lost，禁止再用裸 EOF、`-1` 或错误字符串承载多种语义。
- **Trace 是生命周期事实的投影**：HTTP 使用路由模板 span，bed initialize/persist/evict 与 execution 使用稳定领域 span，stage 只记 event；不得把 command、env、stdout/stderr 写入 span。后台 initialization / execution 继承 trace identity 但不继承 HTTP cancel。详见 `docs/observability.md`。
- **隔离按 Bed 的统一目标尽量兑现**：dorm/room/suite 是文件数据隔离档位；进程视图、网络与资源能力分别选择和披露。环境能力不足可以降级，已选机制执行失败不能静默放开边界。组合必须满足准备、降权与回收的顺序约束；当前缺口见 `docs/isolation.md` 与 `docs/backlog.md`。
  - daemon 身份与 BedUser 正交：daemon 可保留资源管理权限，所有 command/session 使用 resident Bed 已解析的 BedUser；普通房型使用实例固定用户，uid 机制使用稳定的 per-Bed 用户。身份生命周期、capability 与降权顺序见 `docs/privilege.md`。
  - BedFS 路径映射在所有档位一致；PRoot/pathshim 改善进程路径体验，不提供安全边界，也不提高文件隔离档位。
  - Store 独立选择需持久化的 BedFS 子树；新增 projection 不自动获得耐久性，详见 `docs/store.md`。
- **amenity 通则**：共享设施按 Bed 分配应用状态，产物落对应 workspace；设施状态、Bed 级凭据与 Bed 生命周期分别管理。北向使用 Bed 级动作或受限代理，不裸透传共享设施的管理协议。应用切分不等于文件、网络或资源完整隔离，当前机制与缺口见 `docs/amenity.md`。
- **常驻 shell 的坑**：一个 Shell 只能有**一个** stdout reader（否则 run 间串输出——v1 踩过）；Run 之间串行；Shell 持有启动时的 Executor View，session run 的 cwd 必须经 `RunAt` 投影并作为独立控制步骤执行，禁止 Web 拼接 `cd` 或 Executor path；`exit` 会杀死 session，非零退出码用子 shell（`sh -c "exit N"`）。**锁纪律**：`runMu` 串行化 Run 且只有 Run 碰；`mu` 只护 `dead` 标志、纳秒级持有——曾因单锁设计让「shell 死亡+未断开客户端」死锁整个 daemon（含 healthz），别往 `mu` 里加阻塞代码（见 shell.go LOCKING 注释）。
- **E2E owner 边界**：Hostel 的单机 suite 直接验证真实 daemon/image 的 bed runtime、隔离与 carrier userland；上层控制面只保留 placement、跨 carrier 持久化和 lifecycle 编排，不在 K8s E2E 重复证明 Hostel 内部契约。运行说明见 `tests/e2e/README.md`。
- Go 项目常规：改完 `go build ./...` + `go test ./...` + `go vet ./...` 三件套过再提交（见 `Makefile`）。仓库在 `github.com/qiankunli/hostel`，保护分支 main 走 PR。
- 根目录 `VERSION` 是二进制和镜像的唯一版本源；每次改动都必须同步递增版本号，默认递增 patch 版本。
- 通用小工具优先用 [go-stdx](https://github.com/qiankunli/go-stdx)（env 解析、随机 id、shell quote、原子写文件、目录字节数等），不要在仓内再手写它已有的操作；沉淀出的新通用件也应迁去 go-stdx 而非留在 internal。

## References

- 文件操作与传输：`docs/transfers.md`（files API 入口、Bed ↔ S3 Copy / Restic、操作状态与自动持久化边界）

- 网络管理：`docs/network.md`（自动探测、命令作用域与诊断；共享 Chromium 代理待支持）

- 核心架构（定位、Bed 模型、请求与状态、组件契约及生命周期主流程）：`docs/kernel.md`
- 待办清单（尚未交付的演进项）：`docs/backlog.md`
- Filesystem（BedFS、路径空间、进程视图与文件隔离机制）：`docs/filesystem.md`
- 隔离总览（跨领域隔离目标、机制组合、降级与实际保证）：`docs/isolation.md`
- 权限模型（daemon / BedUser、UID 租约、capability、降权顺序与部署前提）：`docs/privilege.md`
- Store（Hostel 直管各 bed 的持久化与 Restore；本地 workspace=工作副本、S3 快照=持久身份）：`docs/store.md`
- 资源治理方案（carrier 采集/汇报/admission + per-bed accounting 已落地，per-bed limits 待实现）：`docs/resource.md`
- 可观测性设计（统一生命周期事实，并投影到日志、接口和 metric）：`docs/observability.md`
- 快速上手 / API 一览 / 配置：`README.md`
- 单机 E2E（binary/image profiles、环境契约与覆盖边界）：`tests/e2e/README.md`
- 归属（execd 参考的具体设计点）：`NOTICE`
- API 契约来源：上游 OpenSandbox 仓库的 `specs/execd-api.yaml`（https://github.com/alibaba/opensandbox）

- 共享设施、Bed 切片与凭据、CDP 代理及实际边界：`docs/amenity.md`
- MCP 远程工具与连接生命周期、公共 Go 嵌入入口：`docs/mcp.md`
