# hostel 核心架构

> 状态：当前实现及演进边界。未交付项见 `backlog.md`，专题细节以同目录文档为准。

## 一、定位与边界

**hostel = 面向 AI agent 的 sandbox runtime**，可以理解为单个 carrier 内的“小型 kubelet”：管理一台机器 / 一个容器内的多个隔离执行单元。资源与文件能力以 OpenSandbox 为设计基线，执行协议由 hostel 自己拥有。可单机跑（laptop / VM / CI），也可作为多租户共享实例的 in-process runtime，由上层调度系统按 `sandbox_id → (实例, bed)` 路由驱动。

| hostel 做 | 不做（留给上层调度系统） |
|---|---|
| bed 生命周期、exec / file、共享多租服务（Chromium/Jupyter…）管理 | 调度选 pod、workspace 供给、信任分档路由、计费 / 配额 |

OpenSandbox execd 是主要设计参考。

## 二、核心模型：bed

- **bed = 隔离单元 = 对外一个 sandbox**：持有 workspace 数据身份、隔离配置与生命周期；它不等于某个具体进程。
- **BedFS = bed 的文件系统数据域**：持有 bed_home、workspace 与 client/carrier/Executor 路径语义；Executor 替换只重建 View，不改变数据身份。细节见 `filesystem.md`。
- **executor = bed 当前的进程承载域**：一个 resident bed 同时至多有一个 current Executor。Executor 是短于 Bed 的可替换身份，负责派生、查询、停止和回收进程；supervisor 与 local 是 backend，不是领域对象。
- **execution = 一次运行**：属于一个明确的 bed id 与 executor id，拥有独立 process id、输出和结构化终态。
- **默认 bed 兜底**：原生请求不带 bed id → 落到 `default` bed。调用方可完全无视 bed 概念（单租户体验）；它不属于 `/v1/isolated/*` 的 session 视图。
- **bed 路由**：HTTP header `X-Hostel-Bed`（或 query `bed`），缺省 default。
- **bed 初始化**：`InitializeBed` 接受 desired bed 后立即返回 `initializing`，后台完成 BedFS Stage-in、隔离准备和 resident 发布；`status.phase` 与 `status.readiness` 是控制面观察点。原生数据面的 `Ensure` 复用同一初始化并等待 Ready，不会取得半成品 Bed。
- 一个 pod 只用 default bed = 独占；多 bed = 共享，每 bed 仍有私有 ns / workspace / shell state。
- **idle GC**：bed 空闲超时回收（默认 30min，可配；default bed 永不回收）。

### exec 模型：状态住文件系统，进程短命或显式持有

- **`/command` = bed 隔离内的一次性进程**（execd 同款）：每次 fork 全新 `bash -c`，前台流式等待、后台注册分离，只差 wait 模式。调用方脚本的 `set -e` / `exit` / `trap` 是合法输入，与自己的进程共存亡，**不可能波及 bed 的其它执行**。曾让前台搭常驻 shell 的便车（省一次 fork、cwd/env "免费"延续），代价是任何一个脚本的 `exit` 都拆掉共享会话、连坐后续所有 exec（真实故障：AS skill batch-sync 以 `set -euo pipefail` 开头，一步失败即杀会话）——无状态端点不得偷用有状态实现。
- **跨 exec 的延续走 workspace，不走 shell 内存**：控制面的既有契约就是文件——init_script 写 env 文件、后续每条 exec 由调用方拼 `source`；cwd 每次显式传。pod 档 `k8s exec`（每次新进程、无常驻 shell）跑同一套请求是决定性证据。由此 bed 与 pod 档 exec 语义同构，弱档可无差别替换中档。
- **`/session` = 显式有状态会话**：调用方自己 create / 持有 / delete 的常驻 bash（REPL 式 `export`/`cd` 延续）；死了只影响自己。有状态是 opt-in 的例外，不是每个 exec 的默认。
- **进程环境按 owner 分层**：`HOSTEL_*` 只供 daemon 配置，外部 `BED_*` 也会被过滤；其余 Carrier 环境默认传给 bed，再由 Hostel 覆盖 bed context 与本次 request env。部署方负责非 Hostel 环境的安全性。完整边界见 `data.md`〈敏感数据边界〉。

### BedFS：agent 把 bed 当独享机器

**对外契约（调用方的预期）**：调用方（上层调度、planit 注入、agent 发 exec）把 bed 当独享机器——`/workspace` 就是"我的工作区"，`/workspace/skills` 就是"我的 skill"。这不是 sandctl 一家的约定，是 sync + planit 路径注入 + agent bash 三方共享的心智，pod 档下天然成立（一 pod 一 sandbox，`/workspace` 就是那份 workspace）。弱档要无差别替换 pod 档，须原样兑现：**同 pod 内 conv1 的 `/workspace` 与 conv2 的 `/workspace` 是两份不同数据、互不可见**。这一契约由 suite 完整兑现，低档只能部分兑现（见〈降级〉）。

**兑现方式：BedFS + Executor View**。file API 的 path、workdir/cwd 等结构化字段先由 BedFS 映射到 bed_home，再投影到当前 Executor。suite 先 tmpfs 遮蔽兄弟 Bed，随后把完整 bed_home bind 到机制私有入口，并把 `bed_home/workspace` 额外 bind 到规范 `/workspace`；所以 `cwd="/"`、`cwd="/tmp/job"` 与 workspace cwd 都可用。opaque agent bash 里的绝对字面量无法可靠改写，只有 `/workspace/...` 由规范挂载保证同名同物；`/usr /opt /bin /etc` 等 carrier 工具链仍来自共享只读根。完整路径模型见 `filesystem.md`。

**为什么独享 `/workspace` 而不独享 `/`**：bed 要的是"独享一台机器"的**数据面**——工作区私有、互不串数据；而**系统面**（`/usr /bin /lib /etc` 工具链、`/opt/sandbox/commands`、node/python 解释器、skill 自带工具）是**只读、无租户数据、天然可共享**的。让每个 bed 独享 `/` 等于每 bed 复制一整套 rootfs，与"一个 carrier pod 装一份重运行时、N 个 bed 共享它"的弱档初衷（省内存/启动、高密度）直接矛盾；而共享只读 `/` 零泄漏风险（读到的是公共二进制，不是别人的数据）。所以隔离精确地只加在**有租户数据的那一层**（`/workspace` + 兄弟目录），把无数据的系统层留作共享——这既是安全上的必要（数据面必隔离），也是成本上的最优（系统面不必复制）。代价是"独享 `/`"这类更强诉求（conv1 `ls /` 看不到 conv2）弱档不提供，需要它就上强档（microVM）。

**降级：诚实披露，不假装**。环境到不了 suite 时 `/workspace` 隔离降级，两档失败模式不同、都如实上报（capabilities/healthz 报 `workspace_mount: false` + `isolation.effective`），由上层决策（接受降级 / 换能到 suite 的环境），**不是拒绝服务**——结构化 file API、相对路径 exec 在低档仍可用，受影响的只是"字面 `/workspace` 绝对路径 in bash"这一类：

- **room（landlock）**：兄弟目录存在性可见、但跨 bed 读写被内核拒。agent 的字面 `/workspace/skills` 落在 bed 领地外 → **EACCES 诚实失败**，绝不串数据。
- **dorm（direct，无墙）**：`/workspace` 是 carrier pod 的共享真路径，conv1 与 conv2 的 `/workspace/skills` **物理是同一份 → 会互相覆盖/串数据**。这是 dorm"通铺无墙"的本性，healthz 如实报 `level=dorm`，调用方须知此档不保证 conv 间隔离。

**k8s pod 内够到 suite 的三道闸**（真实集群踩点，前两道 hostel 自解、见 `data.md`〈k8s pod 内可达性〉）：`--unshare-user`（非特权 pod 建 mount ns）+ `--ro-bind /proc`（绕 masked /proc，弃 pidns）+ **AppArmor 豁免**（containerd 默认 profile deny mount，是部署项，由上层按集群策略自适应，非 hostel 硬性要求；探不过时 `apparmor_profile` 进 healthz 点名）。三点均无需特权。

### 执行层次与进程树

领域层次固定为 `Hostel → Bed → Executor → Execution`：

```
tini (pid=1)                      ← pod 级收尸兜底
 └─ hostel (daemon)               ← 内置 amenity supervisor（Registry），非独立进程
     ├─ chromium (amenity)        ← pod 级共享，按 bed 切租，不进任何 bed 的树
     ├─ jupyter  (amenity)
     └─ bed A                     ← workspace / sandbox 持久身份，不是 OS 进程
          └─ executor E1          ← 当前 0/1；丢失后可替换为 E2
               └─ supervisor     ← Linux backend 的具体 supervisor 进程
                    ├─ execution X 的 process
                    └─ session shell
```

- **Bed 与 Executor 解耦**：Bed 的 workspace、generation、retention 不随 Executor 消亡；Executor lost 时，其在途 Execution 以 `process.kind=lost`、`termination_cause=executor_lost` 结束，下一次请求为同一 Bed 创建新 Executor id。API 不透传 Unix socket 的裸 EOF，transport detail 只进服务端日志与 Trace。
- **Executor 契约**：调用方生成 process id；`Start(processID, spec)` 幂等，重复 id + 同 spec 返回同一进程，重复 id + 不同 spec 拒绝；`Get` / `Wait` 可换连接重试；每次请求携带 executor id 做 fencing，旧连接不能误投给替代实例；terminal status 保留到 Executor 结束。
- **supervisor 是 Linux backend**：hostel re-exec 自己成为每 Executor 一个的小型 supervisor，unixpacket + `SCM_RIGHTS` 传 stdio，负责 fork、subreaper 收尸与整树 Shutdown。信号与 Shutdown RPC 走同一优雅退出状态机，不在 signal goroutine 直接 `os.Exit`。Linux 里父子关系由 fork 方决定，所以单纯让 daemon 设 subreaper 无法替代这一层。
- **local 是可移植 backend**：命令是 daemon 的直接子进程，以独立 pgid 管理。它保持相同 Executor / Process 接口与结构化终态，但不承诺清理脱离 pgid 的 double-fork 进程。
- **对照 execd**：execd 是 daemon 直接派生的平树；其 pgid 与 zombie 处理对应 local backend。supervisor backend 通过真实父子树解决整域回收问题。
- **amenity 监督内置于 daemon，不设独立 amenity-manager 进程**：pod 语义下 hostel 是主容器进程，hostel 死 = pod 重启，独立 manager 买不到任何存活性，只多一层 IPC 和"谁重启 manager"。`amenity.Registry` 升级为 supervisor（健康检查 → backoff 重启）；崩溃重启后的租约走**惰性重建**——tenant 标失效，下次 `AcquireTenant` 重建切片（bed 侧感知为一次"新开"），避免主动全量重建的重启风暴。
- **后续边界**：suite 的持久 namespace + PID 1 可作为新 Executor backend 能力演进；cgroup v2 `cgroup.kill` 只补强“杀”，不替代 supervisor 的收尸与终态所有权。
- **实现锚点**：抽象与 backend 在 `internal/executor`；re-exec 协议和 supervisor/reaper 在 `internal/supervisor`；Bed 只持有 current Executor，Execution 只消费 Process 终态。

## 三、通用 managed-service 框架

**通则**：重资产、自带多租能力的服务，由 hostel 在 bed 外统一管理**一份**，用应用**原生**的租户机制切分，产物落对应 bed 的 workspace。Chromium、Jupyter 各是一个实例（execd 的 `/code` 委托 Jupyter、我们 `as serve` 的 Chromium 是两个先例）。

内部接口（非 HTTP，是 hostel 内插件点）：

```go
type ManagedService interface {
    Name() string
    AcquireTenant(bedID, workspace string) (Tenant, error) // 取/建本 bed 的切片
    ReleaseTenant(bedID string) error                       // bed 删除/idle 时调
    Healthy() bool
}
```

| 维度 | Chromium 实例 | Jupyter 实例 |
|---|---|---|
| 共享进程 | 一个 Chromium（pod ns，bed 看不见） | 一个 Jupyter Server |
| 租户切片 | BrowserContext（~ms 创建） | per-bed kernel |
| 产物路由 | 下载 → `<bed>/downloads` | 输出 → `<bed>` |
| 所有权 | hostel 强制 bed 只碰自己的切片 | 同 |
| HTTP 面 | v1.1 `/v1/browser/*` | v1.1 `/code` 或 `/v1/jupyter/*` |

各服务 HTTP 面自定义（navigate vs run code 无法统一），通用的是**内部接口 + bed 拆除钩子**。

**v1 只做钩子**：`bed.Manager` 持有一个（v1 为空的）service registry，Delete / CollectIdle 时遍历 `ReleaseTenant(bedID)`。实例（Chromium/Jupyter）推 v1.1，此钩子让其 drop-in。

## 四、API

**v1 实现**：
- `/ping`、`/healthz`（isolator 名 + 可用性 + bed 数）
- `/files/*`：info、mv、permissions、search、replace、upload、download、DELETE
- `/directories/*`：list、create、delete
- `/command`：前台/后台共享同一个 `Execution` 生命周期，只差 initiating request 是否等待；SSE 依次表达 execution_start、stdout/stderr、execution_end，终态区分 exited / signaled / lost，并保留 timeout / cancel / interrupt / teardown cause；后台带 `/command/status/{id}` + `/command/{id}/logs`
- `/session`：bash 会话 create / run / delete（显式有状态会话，常驻 shell 只存在于此）
- `/v1/isolated/*`：`session_id` 与非 default bed ID 一一对应；default 只服务原生 API 的缺省路由，不向 session list / attach 暴露。该视图复用 bed 的生命周期、常驻 shell、Execution 与文件/目录能力，不再维护平行状态。当前支持 balanced + 读写 `/workspace` + 共享网络，超出能力边界的参数明确返回 `NOT_SUPPORTED`
- `/v1/beds`：管理与 capabilities（hostel 特有）；POST 接受初始化并返回 `202 initializing`，GET 通过 phase/readiness 观察进度与失败原因

**v1 不做（v1.1+）**：`/code`（委托 Jupyter，AS 用不上，砍）、`/pty` WS。`/v1/isolated/*` 的 diff / commit 路由为兼容性保留并明确报告不支持；持久身份仍由 bed 快照负责，不另造 isolated-session persist。

## 五、isolation

- `direct`（默认，全平台）：仅 chdir 到 workspace，无隔离——dev / 可信单租户；
- `bwrap`（linux，build tag）：user/uts/ipc + 私有 mount 视图、RO 根与 BedFS bind；非 linux 诚实降级；
- 更强档（真 setuid / seccomp / per-bed cgroup）v1.1 按 OSEP-0013 增补。

## 六、文件与数据

- **BedFS home = `<root>/<bedID>/data`**，workspace 是其真实子目录 `data/workspace`；Bed 目录还包含不暴露给进程的 `meta.json`。持久身份与跨 carrier 恢复由 Store 负责；
- overlay / upper（CoW）**v1 不做**：持久数据走 rw-bind，overlay 留临时态，v1.1 再加（内核 overlayfs 的 upper 不能放网络 FS，见 `store.md`）。

## 七、目录结构

```
hostel/
├── cmd/hostel/main.go
├── internal/
│   ├── config/      flags + env（HOSTEL_*）
│   ├── bed/         Bed 生命周期、Execution、shell 与 Store 同步调度
│   ├── bedfs/       bed_home/workspace、三类路径投影与 Bed 内文件操作
│   ├── executor/    Executor 抽象与 local / supervisor backend
│   ├── supervisor/  Linux supervisor/reaper 与可重连 IPC
│   ├── isolation/   Isolator 接口 + direct / landlock / uid / bwrap
│   ├── amenity/     共享设施接口、Registry 与 Chromium
│   ├── store/       noop / S3 快照、恢复与持久身份
│   └── web/         gin：router / sse / files / command / beds / errors（薄适配层）
├── docs/kernel.md
├── Makefile / README.md / NOTICE / .gitignore
```

**关键约束**：`bed` / `bedfs` / `executor` / `isolation` 纯 Go、**不含任何 HTTP 类型** → 换框架只动 `web/`。

## 八、技术选型决策

| # | 决策 | 结论 | 依据 |
|---|---|---|---|
| 1 | HTTP 框架 | **gin** | 与 execd 一致（execd 即 gin）；gin/hertz 皆可 go get，可用性非变量 |
| 2 | 移植方式 | **净重写，execd 作参考** | 搬设计（bwrap argv / marker-shell / fs 防护 / UpperManager）；同为 gin，可直接借 execd 的 handler 片段（保 Apache-2.0 attribution） |
| 3 | managed-service | v1 只留 `ReleaseTenant` 钩子，实例推 v1.1 | Chromium 只是实例之一，框架先立 |
| 4 | v1 范围 | 砍 `/code`，PTY / cgroup / seccomp / overlay-commit 推 v1.1 | 先跑通 bed + exec + file 主干 |

> hertz 备选记录：若 hostel 将来产品化、要接字节内部可观测 / 服务网格，可迁 hertz；因 web 层是薄适配、核心零框架依赖，迁移成本集中在 `web/` 一层。本次为与 execd 一致选 gin。

## 九、v1 交付物

单二进制 `hostel`，`--isolation direct` 本机起、curl 通 `/files` `/directories` `/command`(SSE) `/session` `/v1/beds` `/healthz`；`go build` + `go test` 绿；README 记两层模型（bed = 隔离单元 / spec 原语在 bed 内）+ 决策。

## 十、专题设计文档

bed 的三个正交维度各有专门文档，本文只留一句定位：

- **数据治理**（一个 bed 不能读写另一个 bed / 宿主的数据；tmpfs 遮蔽兄弟 bed + `/workspace` 规范挂载）：`data.md`
- **数据持久化**（本地 workspace 是工作副本，S3 快照是持久身份；生命周期边界同步）：`store.md`
- **资源治理**（per-bed cgroup v2 记账与 carrier 准入已落地；per-bed 硬限额待实现）：`resource.md`
- **amenity 共享设施**（Chromium/Jupyter 等重资产进程共享、按 bed 切租、bed 级动作不裸暴露 CDP）：`amenity.md`
