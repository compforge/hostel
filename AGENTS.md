# hostel

## 项目定位与边界

**面向 AI agent 的 sandbox runtime**：在一台机器 / 一个容器内管理多个隔离执行单元（**bed**）。资源与文件 API 以 OpenSandbox 为设计基线，执行协议由 hostel 自己拥有。形态上 hostel = **web server + bed manager + amenity manager + store** 的组合（后续可扩充更多 manager）。可单机跑（laptop/VM/CI），也可作为多租户共享实例的 in-process runtime，由上层调度系统按 `sandbox_id → (实例, bed)` 路由驱动。

- **做**：bed 生命周期、exec / file、共享多租服务（Chromium/Jupyter…）管理。
- **不做**（留给上层调度系统）：实例调度、跨实例路由、计费配额。
- 参考 OpenSandbox execd（Apache-2.0）净重写，非其 fork；归属见 `NOTICE`。设计与 roadmap 见 `docs/kernel.md`。

## 概念与命名约定

以下名词全仓（代码 / 注释 / 文档）统一使用，避免同物多名、一名多物：

- **bed**：隔离执行单元，对外即一个 sandbox（workspace + 常驻 shell，状态跨命令保持）。
- **bed id**：bed 的标识，**由调用方给定、对 hostel 不透明**——hostel 不解释其业务语义（不认识 conversation / tenant 等上层概念，也不据此派生任何子目录）；缺省兜底 id 为 `default`，只服务原生 API 的无 bed 路由，不属于 isolated-session 兼容视图。
- **workspace-root**：所有 bed 目录的**父目录**，**可配、不写死**（`--workspace-root` / `HOSTEL_WORKSPACE_ROOT`，默认 `/workspace`）；**daemon 启动时创建一次**。
- **bed 目录**：`{workspace-root}/{bed id}`，含 `meta.json`（可移植身份）+ `data/`；**该 bed 首次被 Ensure（即首次收到指向它的请求）时惰性创建**。
- **bed_home（data 目录）**：`{bed 目录}/data`——**客户端视角的 `/`**，bed 表现得像独占整个 pod fs：任意客户端绝对路径单射 rebase 到它下面、回显对称；持久化 / 快照的对象，bed 只见它。
- **bed workspace**：`bed_home/workspace` 真实子目录（非别名）——OpenSandbox 契约的 `/workspace`（`fsops.VirtualPrefix`）、相对路径的基准、默认 cwd、suite 下的真实挂载点。
- **房型（dorm / room / suite）**：bed 的隔离档，与 bed 正交（见〈关键约定〉isolation）。
- **luggage**：bed evict 后留在本机的现场缓存（快照才是身份，luggage 只是加速）。
- **amenity**：bed 外由 hostel 统一管理的共享重资产设施（Chromium / Jupyter…）。
- **executor**：某个 bed 当前的、可替换的进程承载域。bed 持久存在，executor 丢失或关闭后可用新 id 重建；Linux 默认实现为 bed-init，非 Linux / 显式 local 使用 daemon 直接派生。
- **execution**：一次命令运行。每次有独立 id，且记录其所属 bed id 与 executor id。

**进程模型**（进程归属树；详见 `docs/kernel.md`〈进程树〉）：

```
tini (pid1)                       pod 级收尸兜底
└─ hostel (daemon)                内置 amenity supervisor，无独立 manager 进程
   ├─ chromium / jupyter          amenity：pod 级共享、按 bed 切租，不进任何 bed 树
   └─ bed [持久身份]
      └─ executor [当前 0/1，可替换]
         └─ bed-init              Linux backend：fork / 收尸 / 死前杀整树
            ├─ execution process  一次 command 一个
            └─ session shell      /session 显式常驻
```
`Hostel → Bed → Executor → Execution` 是领域层次；bed-init / local 只是 Executor backend。

**路径模型**（客户端 `/` = `bed_home`，映射单射、回显对称；调用方以 `capabilities.workspace_mount` 探测挂载语义）：

```
客户端任意路径：/workspace/x → data/workspace/x；/tmp/x → data/tmp/x；相对路径 = workspace 相对
   │  fsops.Resolve：单射 rebase + 拒逃逸（回显 ToClient 是其逆映射）
   ▼
<workspace-root>/                 宿主侧，所有 bed 父目录；可配 HOSTEL_WORKSPACE_ROOT，默认 /workspace，daemon 启动建
└─ <bed id>/                      bed 目录；首次 Resolve 惰性建
   ├─ meta.json                   可移植身份
   └─ data/                       bed_home（客户端的 /）；持久化/快照对象
      └─ workspace/               OpenSandbox workspace，真实子目录
           suite       → bind 挂载到沙箱内 /workspace（shell 路径 == file API 路径；bed_home 其余部分无进程视图名）
           direct/room → 无挂载，shell cwd = 宿主真实目录（整个 bed_home 可达）
```

## 代码地图与核心模块

```
deploy/docker/Dockerfile  多阶段多架构镜像(amd64/arm64,builder 原生交叉编译免 QEMU)：静态 hostel + debian-slim（内置可选 bwrap + chromium）；tini PID1；hostel --health 做 HEALTHCHECK
cmd/hostel/main.go     组装：config→isolation→amenity registry→store→bed manager→gin server；idle GC/luggage GC/持久兜底；--version/--health/__confine(landlock confiner 自 re-exec) 前置子命令；优雅关停
internal/
├── config/            flags + HOSTEL_* env
├── tracing/           OpenTelemetry 进程初始化：OTLP exporter、W3C propagation 与日志 trace/span 关联
├── isolation/         数据隔离房型档：New 按 env ceiling 路由；direct(dorm/全平台) + landlock(room/linux) + bwrap(suite/linux)
├── executor/          Executor 抽象与 local / bed-init backend；进程 identity、幂等 Start、终态与整域 Shutdown
├── bedinit/           bed-init 的可重连 IPC 协议与 Linux supervisor/reaper 实现
├── bed/               ★核心。bed=隔离单元=对外一个 sandbox
│   ├── bed.go         Bed：隔离单元本体 + Status 三维事实(state/generation/retained_until) + touch/accessor
│   ├── manager.go     Manager：bed 集合与全生命周期；Ensure(空→default，按 generation 判 luggage 新鲜)/Get/List、回收(Evict→revoke→persist→原子复核→teardown/Purge/CollectExpired)、持久化(persistBed/Checkpoint/PersistDirty)
│   ├── store_sync.go  Store 同步调度：合并 lifecycle/pressure trigger，自主串行、周期与失败退避
│   ├── operation.go   operation（无状态请求，kind=exec/file/browser/checkpoint/control）：BeginOperation + timeout 截断
│   ├── session.go     session（可撤销有状态持有，cdp 类）：OpenSession/Touch/Close；revokeSessions 供 evict 在 persist 前吊销（shell 走 shell.go 自备机制，revoke 时一并 Close）
│   ├── env.go         bed 进程环境唯一组装点：carrier allowlist + BED_* context + request overlay
│   ├── observability.go Bed 生命周期记录：activate/persist/evict 的结构化 stage 日志与最近摘要
│   ├── execution.go   Execution：前台/后台/session 统一身份、输出、进程终态、stop cause 与有界 registry
│   ├── luggage.go     luggage（evict 留下的现场缓存）：磁盘水位 GC（stale 优先→LRU）、Inventory（调度器视图）
│   ├── shell.go       常驻 bash：CreateShell/ForegroundShell；单 reader goroutine→lines chan，Run 用 marker 分帧、单消费（状态跨 run 保持）
│   └── command.go     一次性命令构建与启动；所有终态和观测事实归 execution.go
├── fsops/             bed_home rooted 文件操作；Resolve 把任意客户端路径单射 rebase 进 bed_home + 拒逃逸；新建路径按属主 chown（单一属主不变式）
├── store/             Hostel 直管的 bed 持久化与 Restore：Store 接口 + noop/s3(desync 内容寻址增量,只传变更块)，默认 auto 按 bucket 有无解析；见 docs/store.md
├── resource/          per-bed cgroup v2 记账 + carrier CPU/内存准入；只读准入不要求子树委派
├── amenity/           Amenity 接口(生命周期 State)+ Registry；chromium 实例(共享浏览器/每 bed BrowserContext)；见 docs/amenity.md
└── web/               gin 薄适配层：server(路由+bedOf 解析) / errors / sse / files / command / beds
```

**数据流**：请求 →`web` 按 `X-Hostel-Bed`(缺省 default) 解析 bed → 调 `bed`/`fsops` 核心 → 响应（命令走 SSE）。核心层（bed/fsops/isolation/service）**不含任何 HTTP 类型**，换框架只动 `web/`。

## 关键约定

- **bed = 客人单元 = 对外一个 sandbox**（workspace + 常驻 shell，状态跨命令保持）；**房型(dorm/room/suite)是这张床的隔离档、与 bed 正交**——bed 是跨档不变的基本单位，房型只描述"床周围的墙"有多严，不替代 bed 命名（见 `docs/data.md`）。
  - **默认 bed 兜底**：不带 bed 的原生请求落 `default`，单租户调用方可无视 bed 概念；default bed 不暴露为 isolated session，永不被清数据、不可 purge、不占任何 bed 数量名额。
  - **生命周期事实分维度**：`state=active|idle|evicting|dormant` 只表达互斥操作态，`generation` 表达数据版本，`retained_until` 表达最早安全回收期限；`pinned` 不是新状态，而是“有 operation 或 durable store 尚有未同步数据”的复合容量事实（noop 只看 operation）。Bed 级请求分两类（`docs/lifecycle.md`）：operation 无状态、超时被系统截断，经 `BeginOperation` 持有、不可被普通 Evict 杀死；session 有状态、客户端显式开闭、不抬高 status，evict 以 revoke 主动终结。
  - **luggage**：共享快照存在时只是本机缓存；同机 resume 按 generation 判新鲜，落后则整目录丢弃重拉；noop store 下 luggage 是唯一副本并会阻止 carrier 回收。`GET /v1/beds` 向调度器如实上报实例容量、各 state 数量和每个本机 Bed（含 dormant luggage）的事实。详见 `docs/store.md` §四。
  - **bed 容量准入**：`--max-beds` 限 resident tenant bed；pinned 接近 `--max-pinned-beds` 时上报软 `bed_pressure` 供上游提前扩容，达到硬上限才以 `INSUFFICIENT_BED` 拒绝新的 carrier 归属。新 resident、dormant restore、未 pinned 的 idle bed 重新激活时检查；pinned bed 继续由当前 carrier 承接。CPU/内存 pressure 仍单独执行资源准入。Hostel 通过 `pinned` / `data_synced` 上报事实，不自行选择 carrier；同步 trigger 只表达“尽快同步”，节奏、合并与重试由 Store 同步循环统一负责（详见 `docs/resource.md` / `docs/store.md`）。
- **执行层次是 `Bed → Executor → Execution`**：Bed 是 workspace / sandbox 的持久身份；Executor 是当前可替换的进程域；Execution 是一次运行。Executor 丢失只终结归属它的进程，不丢 Bed 数据，下一次请求创建新 Executor。每次前台、后台或 session run 都生成 `Execution`；`execution_start` 先于输出，之后恰有一个 `execution_end`。`ProcessOutcome` 表达 exited / signaled / lost，termination cause 独立表达 timeout / cancel / interrupt / teardown / executor_lost，禁止再用裸 EOF、`-1` 或错误字符串承载多种语义。
- **Trace 是生命周期事实的投影**：HTTP 使用路由模板 span，bed activate/persist/evict 与 execution 使用稳定领域 span，stage 只记 event；不得把 command、env、stdout/stderr 写入 span。后台 execution 继承 trace identity 但不继承 HTTP cancel。详见 `docs/observability.md`。
- **isolation 按「青年旅社房型」分档**（对外保证，非机制名）：`dorm`（通铺，无屏障=direct）/ `room`（单间锁门、厕所公用，数据 EACCES 但兄弟可见、系统路径共享=landlock，自 re-exec `hostel __confine`）/ `suite`（套房全私有，兄弟不可见+私有 mount 视图+`/workspace` 规范挂载=bwrap）/ `auto`（顶格取 env 上限）。`effective=min(requested,ceiling)`，请求超上限诚实降级。进程 env 与隔离机制正交：`HOSTEL_*` 只属 daemon、`BED_*` 只属 bed，三档统一由 `internal/bed/env.go` 显式组装。机制（direct/bwrap/landlock/uid）是内部细节，全走 `Isolator` 接口。详见 `docs/data.md`。
- **amenity 通则**：重资产、自带多租的共享设施由 hostel 在 bed 外管一份，用应用原生机制切租（Chromium→BrowserContext、Jupyter→kernel），产物落对应 bed 的 workspace。amenity 有自己的生命周期（idle→running 按需启停）。新增实例 = 实现 `Amenity` + 注册，bed evict/purge 已接 `ReleaseAll` 钩子。北向只暴露 bed 级动作，**不透传 CDP/协议 socket**（会跨租户）。见 `docs/amenity.md`。
- **常驻 shell 的坑**：一个 Shell 只能有**一个** stdout reader（否则 run 间串输出——v1 踩过）；Run 之间串行；`exit` 会杀死 session，非零退出码用子 shell（`sh -c "exit N"`）。**锁纪律**：`runMu` 串行化 Run 且只有 Run 碰；`mu` 只护 `dead` 标志、纳秒级持有——曾因单锁设计让「shell 死亡+未断开客户端」死锁整个 daemon（含 healthz），别往 `mu` 里加阻塞代码（见 shell.go LOCKING 注释）。
- Go 项目常规：改完 `go build ./...` + `go test ./...` + `go vet ./...` 三件套过再提交（见 `Makefile`）。仓库在 `github.com/qiankunli/hostel`，保护分支 main 走 PR。
- 根目录 `VERSION` 是二进制和镜像的唯一版本源；每次改动都必须同步递增版本号，默认递增 patch 版本。
- 通用小工具优先用 [go-stdx](https://github.com/qiankunli/go-stdx)（env 解析、随机 id、shell quote、原子写文件、目录字节数等），不要在仓内再手写它已有的操作；沉淀出的新通用件也应迁去 go-stdx 而非留在 internal。

## References

- 设计文档（定位、bed 模型、managed-service 框架、决策表、v1 范围与 roadmap）：`docs/kernel.md`
- 生命周期（request / bed / hostel 三粒度、operation 与 session 两类请求、status 推导链）：`docs/lifecycle.md`
- 数据治理方案（tmpfs 遮蔽兄弟 bed、`/workspace` 规范挂载统一两套路径语义、降级与测试策略）：`docs/data.md`
- Store（Hostel 直管各 bed 的持久化与 Restore；本地 workspace=工作副本、S3 快照=持久身份）：`docs/store.md`
- 资源治理方案（carrier 采集/汇报/admission + per-bed accounting 已落地，per-bed limits 待实现）：`docs/resource.md`
- 可观测性设计（统一生命周期事实，并投影到日志、接口和 metric）：`docs/observability.md`
- 快速上手 / API 一览 / 配置：`README.md`
- 归属（execd 参考的具体设计点）：`NOTICE`
- API 契约来源：上游 OpenSandbox 仓库的 `specs/execd-api.yaml`（https://github.com/alibaba/opensandbox）
