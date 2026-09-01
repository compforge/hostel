# hostel

## 项目定位与边界

**面向 AI agent 的 sandbox runtime**：在一台机器 / 一个容器内管理多个隔离执行单元（**bed**）。资源与文件 API 以 OpenSandbox 为设计基线，执行协议由 hostel 自己拥有。形态上 hostel = **web server + bed manager + amenity manager + store** 的组合（后续可扩充更多 manager）。可单机跑（laptop/VM/CI），也可作为多租户共享实例的 in-process runtime，由上层调度系统按 `sandbox_id → (实例, bed)` 路由驱动。

- **做**：bed 生命周期、exec / file、共享多租服务（Chromium/Jupyter…）管理。
- **不做**（留给上层调度系统）：实例调度、跨实例路由、计费配额。
- 参考 OpenSandbox execd（Apache-2.0）净重写，非其 fork；归属见 `NOTICE`。设计见 `docs/kernel.md`，未交付项见 `docs/backlog.md`。

## 概念与命名约定

以下名词全仓（代码 / 注释 / 文档）统一使用，避免同物多名、一名多物：

- **bed**：隔离执行单元，对外即一个 sandbox（workspace + 常驻 shell，状态跨命令保持）。
- **bed id**：bed 的标识，**由调用方给定、对 hostel 不透明**——hostel 不解释其业务语义（不认识 conversation / tenant 等上层概念，也不据此派生任何子目录）；缺省兜底 id 为 `default`，只服务原生 API 的无 bed 路由，不属于 isolated-session 兼容视图。
- **workspace-root**：所有 bed 目录的**父目录**，**可配、不写死**（`--workspace-root` / `HOSTEL_WORKSPACE_ROOT`，默认 `/workspace`）；**daemon 启动时创建一次**。
- **bed 目录**：`{workspace-root}/{bed id}`，含 `meta.json`（可移植身份）+ `data/`；由 `InitializeBed` 异步准备。只有 Store Stage-in/Restore 与 BedFS/isolation 准备全部完成后才发布 Ready，原生数据面首次请求通过 `Ensure` 加入同一初始化并等待，详见 `docs/lifecycle.md`。
- **BedFS**：Bed 持有的文件系统数据域；统一拥有 bed_home、workspace、客户端/宿主/Executor 三个路径空间与文件操作。Executor 替换不改变 BedFS 身份，详见 `docs/filesystem.md`。
- **bed_home（data 目录）**：BedFS 的宿主根 `{bed 目录}/data`——**客户端视角的 `/`**，任意客户端绝对路径单射 rebase 到它下面、回显对称；持久化 / 快照的对象，bed 只见它。
- **bed workspace**：`bed_home/workspace` 真实子目录（非别名）——OpenSandbox 契约的 `/workspace`（`bedfs.WorkspacePath`）、相对路径的基准、默认 cwd、suite 下的真实挂载点。
- **房型（dorm / room / suite）**：bed 的隔离档，与 bed 正交（见〈关键约定〉isolation）。
- **luggage**：非正常生命周期状态，只表达异常退出或旧版 Hostel 遗留的本地 Bed 目录。正常 evict 在任意 Store backend 下都删除本地目录。
- **amenity**：bed 外由 hostel 统一管理的共享重资产设施（Chromium / Jupyter…）。
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
└─ <bed id>/                      bed 目录；InitializeBed / Ensure 首次初始化时创建
   ├─ meta.json                   可移植身份
   └─ data/                       bed_home（客户端的 /）；持久化/快照对象
      └─ workspace/               OpenSandbox workspace，真实子目录

Executor View：
  suite       → 整个 bed_home 内部挂载 + workspace 规范挂载到 /workspace
  dorm/room   → 独立探测 PRoot/pathshim，按 PRoot → pathshim → Carrier 选择 /workspace 视图；隔离机制独立负责访问边界
```

Bedbox 给 caller 的北向进程契约是一个 Bed 独占 Pod 的 `/workspace`；BedFS 另外统一映射 file API 与 cwd 等结构化路径。isolation 只决定内核是否真正兑现独占视图：suite 看不到兄弟路径，room 可见但访问 EACCES，dorm 共享 Carrier 视图且可能操作；不得让房型反向改变 Client → Carrier 主映射。`/tmp` 等其他 opaque 命令绝对路径仍是 Carrier 语义，当前不承诺按 Bed 虚拟化。

三档共享 BedFS 数据底座，对外保证单调增强，但实现机制不单调叠加：Dorm 使用共享 mount view；Room 沿用它并增加 Landlock/UID 访问控制；Suite 改走私有 mount view，使其他 Bed 的路径不存在，不再依赖 Room 的权限判断。请求档位不可达时诚实降级，但不能因为权限不足连低档能做的收口也放弃。

独占 Dorm carrier 可显式开启只读 file API 的进程根回退：BedFS 路径不存在且客户端传入绝对路径时，Reader 回读配置的进程根；BedFS 同名路径优先，mutation 不回退。该配置会暴露进程根、共享 carrier 禁止开启，详见 `docs/filesystem.md`。

## 代码地图与核心模块

```
deploy/
├── docker/Dockerfile  多阶段多架构镜像(amd64/arm64,builder 原生交叉编译免 QEMU)：静态 hostel + debian-slim（内置可选 bwrap + chromium）；tini PID1；hostel --health 做 HEALTHCHECK
└── k8s/              Kubernetes 部署示例；AppArmor 的 PSA 豁免申请见 pod-security-admission-exemption.yaml
cmd/hostel/main.go     组装：config→isolation→amenity registry→store→bed manager→gin server；idle GC/luggage GC/持久兜底；--version/--health/__confine(landlock confiner 自 re-exec) 前置子命令；优雅关停
tests/e2e/             单机真实进程/镜像 E2E：公开 API、bed runtime/isolation 与可选 carrier userland；不经过 sandctl/K8s
internal/
├── config/            flags + HOSTEL_* env
├── tracing/           OpenTelemetry 进程初始化：OTLP exporter、W3C propagation 与日志 trace/span 关联
├── isolation/         执行环境组装：Boundary 解析 direct/landlock/uid/bwrap 隔离档，workspace backend 独立解析 mount/PRoot/pathshim/carrier 进程视图
├── executor/          Executor 抽象与 local / supervisor backend；进程 identity、幂等 Start、终态与整域 Shutdown
├── supervisor/        supervisor backend 的可重连 IPC 协议与 Linux supervisor/reaper 实现
├── bed/               ★核心。bed=隔离单元=对外一个 sandbox
│   ├── bed.go         Bed：隔离单元本体 + Status（phase/readiness/activity + generation/retained_until）+ touch/accessor
│   ├── manager.go     Manager：resident bed 集合、初始化后的 Bed 组装、回收(Evict→revoke→persist→原子复核→teardown/Purge/CollectExpired)、持久化(persistBed/Checkpoint/PersistDirty)
│   ├── initialization.go InitializeBed singleflight、phase/readiness、后台 Stage-in、容量预占与 Ready 发布；Ensure 复用并等待
│   ├── store_sync.go  Store 同步调度：合并 lifecycle/pressure trigger，自主串行、周期与失败退避
│   ├── operation.go   operation（无状态请求，kind=exec/file/browser/checkpoint/control）：BeginOperation + timeout 截断
│   ├── session.go     session（可撤销有状态持有，cdp 类）：OpenSession/Touch/Close；revokeSessions 供 evict 在 persist 前吊销（shell 走 shell.go 自备机制，revoke 时一并 Close）
│   ├── env.go         bed 进程环境唯一组装点：过滤 Hostel 保留命名空间 + Carrier env + BED_* context + request overlay
│   ├── observability.go Bed 生命周期记录：initialize/persist/evict 的结构化 stage 日志与最近摘要
│   ├── execution.go   Execution：前台/后台/session 统一身份、输出、进程终态、stop cause 与有界 registry
│   ├── luggage.go     异常退出/旧版遗留目录：磁盘水位 GC（stale 优先→LRU）、Inventory（调度器视图）
│   ├── shell.go       常驻 bash：CreateShell/ForegroundShell；持有 Executor View；Run 用 marker 分帧、单消费，RunAt 以独立控制步骤投影 cwd（状态跨 run 保持）
│   └── command.go     一次性命令构建与启动；所有终态和观测事实归 execution.go
├── bedfs/             BedFS 数据域：bed_home/workspace、client/carrier/Executor 路径投影与文件操作；新建路径按属主 chown
├── store/             Hostel 直管的 bed 持久化与 Stage-in：router 按配置/bed 布局选 noop/s3(CAS)/pack(聚合增量)/tar(全量单对象)，Restore 旁路完成后原子发布；见 docs/store.md
├── resource/          per-bed cgroup v2 记账 + carrier CPU/内存准入；只读准入不要求子树委派
├── amenity/           Amenity 接口(生命周期 State)+ Registry；chromium 实例(共享浏览器/每 bed BrowserContext)；见 docs/amenity.md
└── web/               gin 薄适配层：server(路由+bedOf 解析) / errors / sse / files / command / beds
```

**数据流**：请求 →`web` 按 `X-Hostel-Bed`(缺省 default) 解析 bed → 调 `bed`/`bedfs` 核心 → 响应（命令走 SSE）。核心层（bed/bedfs/isolation/service）**不含任何 HTTP 类型**，换框架只动 `web/`。

## 关键约定

- **bed = 客人单元 = 对外一个 sandbox**（workspace + 常驻 shell，状态跨命令保持）；**房型(dorm/room/suite)是这张床的隔离档、与 bed 正交**——bed 是跨档不变的基本单位，房型只描述"床周围的墙"有多严，不替代 bed 命名（见 `docs/data.md`）。
  - **默认 bed 兜底**：不带 bed 的原生请求落 `default`，单租户调用方可无视 bed 概念；default bed 不暴露为 isolated session，永不被清数据、不可 purge、不占任何 bed 数量名额。
  - **生命周期事实分维度**：
    - `status.phase=initializing|resident|evicting|purging|dormant|failed` 表达 Bed 所处阶段，`status.readiness` 表达能否服务。
    - resident / evicting Bed 的 `status.activity=active|idle` 由 operation 数量派生。
    - 容量讨论把正交事实组合成互斥的具体状态，再投影为 `pinned_beds ⊆ resident_beds ⊆ occupied_beds`；不把 initializing 混入 `activity_counts`。
    - `generation` 表达数据版本，`retained_until` 表达最早安全回收期限。
    - Bed 级请求分 operation 与 session 两类，详见 `docs/lifecycle.md`。
  - **luggage**：正常 evict 在 durable/noop 下都删除本地 Bed 目录；durable 可从快照恢复，noop 再次初始化则从空目录开始。luggage 扫描只处理异常退出/旧版遗留目录，详见 `docs/store.md` §四。
  - **bed 容量准入**：
    - `occupied_beds` 包含 initializing 与 resident/evicting tenant Bed，`--max-beds` 是其唯一数量硬上限。
    - 初始化在任何 Store I/O 前预占名额，避免并发 Stage-in 穿透。
    - `--bed-pressure-threshold-percent` 默认 80，`occupied/max-beds` 或 `pinned/max-pinned-beds` 任一达水位即上报软 `bed_pressure`。
    - `max-pinned-beds` 不是准入限制，超过也不返回 429；CPU/内存 pressure 仍单独执行资源准入。
    - Hostel 只上报事实，不自行选择 carrier；同步 trigger 的节奏、合并与重试由 Store 同步循环统一负责，详见 `docs/resource.md` / `docs/store.md`。
- **执行层次是 `Bed → Executor → Execution`**：Bed 是 workspace / sandbox 的持久身份；Executor 是当前可替换的进程域；Execution 是一次运行。Executor 丢失只终结归属它的进程，不丢 Bed 数据，下一次请求创建新 Executor。每次前台、后台或 session run 都生成 `Execution`；`execution_start` 先于输出，之后恰有一个 `execution_end`。`ProcessOutcome` 表达 exited / signaled / lost，termination cause 独立表达 timeout / cancel / interrupt / teardown / executor_lost，禁止再用裸 EOF、`-1` 或错误字符串承载多种语义。
- **Trace 是生命周期事实的投影**：HTTP 使用路由模板 span，bed initialize/persist/evict 与 execution 使用稳定领域 span，stage 只记 event；不得把 command、env、stdout/stderr 写入 span。后台 initialization / execution 继承 trace identity 但不继承 HTTP cancel。详见 `docs/observability.md`。
- **isolation 按「青年旅社房型」分档**（对外保证，非机制名）：`dorm`（通铺，无屏障=direct）/ `room`（单间锁门、厕所公用，数据 EACCES 但兄弟可见、系统路径共享=landlock，自 re-exec `hostel __confine`）/ `suite`（套房全私有，兄弟不可见+私有 mount 视图+`/workspace` 规范挂载=bwrap）/ `auto`（顶格取 env 上限）。`effective=min(requested,ceiling)`，请求超上限诚实降级。进程 env 与隔离机制正交：`HOSTEL_*` 只属 daemon、`BED_*` 只属 bed，三档统一由 `internal/bed/env.go` 显式组装。机制（direct/bwrap/landlock/uid）是内部细节，全走 `Isolator` 接口。详见 `docs/data.md`。
  - 最终执行环境由安全 `Boundary` 与 workspace backend 组合；dorm/room 先独立探测满足运行前提的 helper，再按 PRoot → pathshim → Carrier 选择。PRoot 对 `/workspace` 路径 syscall 的覆盖更完整但需要 ptrace，pathshim 是无需 ptrace 的次选；helper 不改变 isolation requested/effective/ceiling/mechanism，也不把 `workspace_mount` 置真，见 `workspace_view`。
- **amenity 通则**：重资产、自带多租的共享设施由 hostel 在 bed 外管一份，用应用原生机制切租（Chromium→BrowserContext、Jupyter→kernel），产物落对应 bed 的 workspace。amenity 有自己的生命周期（idle→running 按需启停）。新增实例 = 实现 `Amenity` + 注册，bed evict/purge 已接 `ReleaseAll` 钩子。北向只暴露 bed 级动作，**不透传 CDP/协议 socket**（会跨租户）。见 `docs/amenity.md`。
- **常驻 shell 的坑**：一个 Shell 只能有**一个** stdout reader（否则 run 间串输出——v1 踩过）；Run 之间串行；Shell 持有启动时的 Executor View，session run 的 cwd 必须经 `RunAt` 投影并作为独立控制步骤执行，禁止 Web 拼接 `cd` 或 Executor path；`exit` 会杀死 session，非零退出码用子 shell（`sh -c "exit N"`）。**锁纪律**：`runMu` 串行化 Run 且只有 Run 碰；`mu` 只护 `dead` 标志、纳秒级持有——曾因单锁设计让「shell 死亡+未断开客户端」死锁整个 daemon（含 healthz），别往 `mu` 里加阻塞代码（见 shell.go LOCKING 注释）。
- **E2E owner 边界**：Hostel 的单机 suite 直接验证真实 daemon/image 的 bed runtime、隔离与 carrier userland；上层控制面只保留 placement、跨 carrier 持久化和 lifecycle 编排，不在 K8s E2E 重复证明 Hostel 内部契约。运行说明见 `tests/e2e/README.md`。
- Go 项目常规：改完 `go build ./...` + `go test ./...` + `go vet ./...` 三件套过再提交（见 `Makefile`）。仓库在 `github.com/qiankunli/hostel`，保护分支 main 走 PR。
- 根目录 `VERSION` 是二进制和镜像的唯一版本源；每次改动都必须同步递增版本号，默认递增 patch 版本。
- 通用小工具优先用 [go-stdx](https://github.com/qiankunli/go-stdx)（env 解析、随机 id、shell quote、原子写文件、目录字节数等），不要在仓内再手写它已有的操作；沉淀出的新通用件也应迁去 go-stdx 而非留在 internal。

## References

- 设计文档（定位、bed 模型、managed-service 框架、决策表与 v1 范围）：`docs/kernel.md`
- 待办清单（尚未交付的演进项）：`docs/backlog.md`
- 生命周期（request / bed / hostel 三粒度、operation 与 session 两类请求、status 推导链）：`docs/lifecycle.md`
- BedFS（bed_home、workspace、client/carrier/Executor 路径空间与职责边界）：`docs/filesystem.md`
- 数据治理方案（tmpfs 遮蔽兄弟 bed、`/workspace` 规范挂载统一两套路径语义、降级与测试策略）：`docs/data.md`
- Store（Hostel 直管各 bed 的持久化与 Restore；本地 workspace=工作副本、S3 快照=持久身份）：`docs/store.md`
- 资源治理方案（carrier 采集/汇报/admission + per-bed accounting 已落地，per-bed limits 待实现）：`docs/resource.md`
- 可观测性设计（统一生命周期事实，并投影到日志、接口和 metric）：`docs/observability.md`
- 快速上手 / API 一览 / 配置：`README.md`
- 单机 E2E（binary/image profiles、环境契约与覆盖边界）：`tests/e2e/README.md`
- 归属（execd 参考的具体设计点）：`NOTICE`
- API 契约来源：上游 OpenSandbox 仓库的 `specs/execd-api.yaml`（https://github.com/alibaba/opensandbox）
