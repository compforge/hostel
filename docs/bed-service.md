# Bed Service：Bed 内的托管服务

本文描述 Bed Service 的职责、生命周期及使用契约；部署配置与 API 见第四节。

## 一、理念与概念

Bed Service 是归属于某个 Bed、由 Hostel 托管生命周期的常驻程序。服务可以提供 HTTP
接口，也可以是后台 worker；均以前台进程运行，由 Hostel 监督启动、就绪、退出与重启。
服务通过 Bed 的统一执行入口运行，与用户命令使用相同的目录视图和隔离策略。

Service 是 Bed 定义的一部分。一个 Bed 可以没有服务，也可以声明多个服务；省略声明
等同于空列表。Carrier 镜像包含某个程序，不意味着每个 Bed 都启用它。

### 服务声明与运行实例

| 对象 | Owner | 职责 |
|---|---|---|
| `ServiceSpec` | Bed 创建者 | 完整描述进程 argv、工作目录、环境引用、监督策略和可选 HTTP 能力 |
| Bed 服务声明 | Bed Spec，由 Bed Manager 写入 | 保存规范化后的非敏感 `ServiceSpec`，作为 Bed 不可变定义的一部分 |
| Service 运行状态 | Service Manager | 维护期望运行、就绪、重启及 endpoint 状态 |
| Service Execution | Executor 与既有执行记录机制 | 承载一次实际运行，记录进程退出事实及执行归属 |
| Port allocation | daemon 级 Port Manager | 统一管理各领域申请的端口、监听占用及具体 allocation 的释放 |

服务以 `(Bed ID, service name)` 定位。同一 Bed 可用相同的程序定义创建多个服务，例如
`api-main` 和 `api-tools`。每次重启产生新的 Execution ID，
并记录所属 Executor ID；同名 Bed 在 Forget 后重新创建，不继承旧运行实例。

### 与 Amenity、Executor 的边界

Amenity 是 Carrier 设施，按 Bed 分配 Tenant，其进程、网络与权限不自动继承 Bed。
Bed Service 则是 Bed 内的工作负载，必须复用 Bed 已解析的执行环境。两者的差异由
隔离与资源归属决定，不由是否使用独立进程决定，参见 [amenity.md](amenity.md)。

Service Manager 管理服务期望与监督策略；Executor 仍是唯一的进程承载域。普通后台
命令不自动变成 Service，只有需要声明式托管的常驻程序才使用服务模型。

```text
Hostel
├─ Port Manager：统一管理 Amenity、Bed、Service 及控制面申请的端口
├─ Bed Manager：协调准备、就绪、准入与回收
├─ Service Manager：按 Bed ID 管理服务运行状态
├─ Amenity Manager：管理独立设施及 Bed 到 Tenant 的绑定
└─ 各 Bed
   ├─ BedFS / Network / Privilege / Resource
   ├─ Spec.Services：该 Bed 的服务声明，可以为空
   └─ Executor：当前可替换的进程承载域
      ├─ 用户 Execution / Session
      └─ Service Execution
```

## 二、主流程

### 部署与创建

Hostel 不维护业务服务目录，也不解释具体业务概念。可信控制面在创建 Bed 时提交
完整 `ServiceSpec`；Carrier 镜像负责提供声明引用的程序、动态库和其他运行依赖。

Bed 创建声明示例（HTTP API 使用对应 JSON）：

```yaml
id: agent-bed
services:
  - name: workspace-api
    command: [/opt/bed-services/workspace-api, --listen, "${LISTEN_ADDR}"]
    directory: /workspace
    env:
      WORKSPACE_ID: workspace-xxx
    env_files:
      OBJECT_STORE_TOKEN: /run/hostel/service-secrets/workspace-api/OBJECT_STORE_TOKEN
    required: true
    restart: on-failure
    http:
      ready_path: /healthz
      token_env: SERVICE_ADMIN_TOKEN
```

创建准入校验实例名唯一、argv 与监督参数合法、凭据只使用文件引用，并确认 HTTP 发布
所需的 Port Manager 可用。Bed Manager 将规范化后的完整声明纳入 Bed Spec。服务集合在创建时确定；重复创建同名 Bed 必须检查声明
一致性，不能静默添加或删除服务。请求为空列表与省略字段使用同一语义。
原生数据面内部的 `Ensure` 只加入已有声明，不把未传服务解释成修改服务集合。

```text
恢复 Bed 数据
  → 准备文件、身份、网络与资源
  → 绑定 Bed Environment
  → 为有服务的 Bed 创建 Executor、启动服务
  → 必需服务就绪
  → 发布 Bed Ready
```

无服务 Bed 沿用原有按需创建 Executor 的路径。有服务 Bed 的启动不能绕经要求 Bed
已经 Ready 的公开执行 API；内部准备入口使用已绑定的 Environment，避免初始化互相等待。
可选服务失败只影响该服务的可用性，必需服务失败使 Bed readiness 不成立。

### 监督与恢复

Service 的 `phase` 表达 starting、running、backoff、stopping、stopped、failed 等运行阶段，
独立的 `ready` 表达当前可用性，同时保留进程退出事实。进程启动即进入 running；只有声明的
就绪检查成功后才 ready。非 HTTP worker 仅以进程存活就绪，不保证业务已初始化。

首次就绪前受启动超时约束，超时按启动失败处理。运行中 HTTP 超时、非 2xx、尚未监听或
临时检查故障只令 ready=false，继续探测；成功后恢复 ready，不重新计算启动期限，也不消耗
重启预算。必需服务未就绪使 Bed readiness 不成立，阻止新工作准入，但不取消已有任务、
Service hold 或运行实例；诊断、重启与回收入口仍可访问。可选服务未就绪只影响本服务。

重启策略区分 `never`、`on-failure` 和 `always`。自动重启受启动超时、退避及重试预算
限制，耗尽后报告失败，不能无限高速重启。显式重启只作用于选中的服务，不替换整个 Bed。
Readiness 不是终止条件；实际退出、Executor 丢失、明确 listener 归属冲突及显式生命周期操作
仍按各自规则处理。Hostel 不从业务暂时不可用推断进程卡死，当前不提供独立 liveness/watchdog。

Executor 丢失先终结并确认清理旧执行域，再按需重建 Executor，恢复声明的服务。
Environment 仍绑定当前 resident Bed 的文件与网络 allocation；恢复不会重做 Store Stage-in。
监督任务绑定确切运行实例，旧实例的退出通知不能覆盖新实例状态。

### 回收

停止新的用户操作和服务代理准入，撤销已有代理 session，并关闭服务自动重启。
协调停止服务、用户执行和 Transfer，确认各自不再写入后，才进入需要静止写入方的
持久化阶段；最后释放 Executor、网络、文件视图等运行资源。

服务停止发送 TERM，超过停止期限后 KILL，并等待进程组不再有活进程。服务必须前台
运行，子进程不能通过 daemonize/setsid 脱离进程组。Linux 使用未回收的 leader 固定 PID，
macOS 使用 kqueue 退出通知；清理失败不提前发布完成。整个 Executor 的兜底清理仍遵循
其原有能力边界。服务必须负责
自己的 graceful shutdown；Hostel 不承诺强制终止时仍能完成业务收尾。停止或释放失败
保留具体 allocation 的清理责任，重试不能错误清理同名的新实例。

## 三、关键设计

### 统一继承隔离，而非复制隔离机制

Service 与 command、session 共同复用 `manager.Environment` 和 Executor 启动链路。
文件视图、BedUser、网络 allocation、权限准备与最终降权顺序由 Bed Manager 统一组合，
服务执行计入对应 Bed 的资源归属。`ServiceSpec` 不能通过另一条 Carrier 进程启动路径绕过这些规则。

服务访问的 `/workspace`、工作目录和其他路径使用相同 BedFS 语义。Carrier 镜像中的程序
与动态库、解释器等依赖需通过受控只读路径纳入所选执行视图；不能假定 Carrier 上存在
某个路径就能在所有房型中执行。服务 runtime 目录是否持久化仍由 Store 规则决定，
默认不把 socket、进程状态和临时文件纳入 workspace 快照。

这里继承的是 Bed 实际具备的隔离能力，包括其降级状态。不会因引入 Service 就获得
私有 PID namespace、per-Bed 硬限额或更强安全保证，参见 [isolation.md](isolation.md)。

### 默认 TCP 监听，端口冲突由 Hostel 管理

非 Web 服务不需要 endpoint。Web 服务默认 TCP 监听，在 Pod 部署下对外提供
`PodIP:port`。服务声明通过 `${PORT}` / `${LISTEN_ADDR}` 表达如何接收监听地址，Service Manager 向 Port Manager 申请地址并注入
启动参数或环境；不要求服务使用 Unix socket，也不要求上层手工为各 Bed 配置端口。

daemon 创建唯一 Port Manager，统一供 Amenity、Bed、Service 以及 Hostel 自身监听
入口使用，避免各领域分别维护端口池。Service Manager 将返回的 allocation 绑定到
确切的 Bed ID、service name 和运行实例；Amenity 和 Bed 相关组件维护自己的绑定。

Port Manager 位于 `internal/host/network`，只处理网络作用域、地址、端口和不透明
owner 标识，不导入 Bed、Service 或 Amenity 模型。各领域决定何时申请、公布、重启
或释放，Bed Manager 负责跨领域回收顺序。它不取代 Network Manager 的 netns、路由
与出站策略管理，也不负责启动应用进程。

Port Manager 的统一职责是：

- 管理动态端口池和明确的固定端口申请，Hostel 控制面固定监听也纳入占用登记。
- 为每次申请返回唯一 allocation 句柄及状态，区分候选预留与已确认实际监听。
- 提供 daemon 持有的 listener；外部进程监听则由领域组件按启动契约确认并关联实例。
- 按 allocation 句柄释放，禁止只凭端口号或可复用 Bed name 释放其他实例的资源。
- 汇总占用、申请方类别、冲突和池耗尽等诊断，状态查询不暴露凭据、不执行远端探测。

Amenity 的共享端口归设施实例，Bed 专属端口归具体 Bed allocation，服务监听和发布
端口归对应 Service。Bed 释放只回收其独占资源；不能关闭仍被其他 Bed 使用的共享
Amenity listener。daemon 关闭时先停止各领域的使用者，再关闭 Port Manager 剩余监听。

当前 Port Manager 管理 TCP；冲突域按网络 namespace、监听地址及地址族判断。共享 Carrier 网络的各 Bed
使用同一个分配域；独立 netns 内的监听与 Carrier 对外入口分别管理。同一 Bed 内多个
服务不能重复分配冲突地址，IPv4/IPv6 与通配监听的重叠也不能绕过占用检查。

端口申请、启动和发布必须形成完整流程：

1. 在统一端口池中原子分配候选端口，排除同冲突域的 Amenity、Bed、Service 和控制面 allocation。
2. 共享网络先检查候选地址能否绑定，排除已有外部 listener；再将地址注入服务，启动并检查就绪。
3. 若有明确绑定冲突，确认本次启动的进程树退出，释放对应 allocation，换端口有限重试。
   其他启动错误按实际原因报告，不能全部归类为端口冲突。
4. 进程存活且 HTTP 就绪成功、没有已确认的归属冲突时发布 endpoint；端口池或重试预算耗尽时明确失败。

Port Manager 管理经 Hostel 申请的端口，不拦截 Bed 用户程序自行调用 bind，也不能
阻止其他进程抢占端口。内核实际 bind
是占用事实；探测端口后关闭再启动不是无竞争预留。对于接受端口参数的普通程序，必须
处理这个窗口中的 bind 失败。程序支持继承监听 FD 时可以持续持有 listener 并交接；
支持 `port=0` 时可从受控本地启动通道获取实际地址，无需向上层控制面注册回调。
这两类交接方式尚未提供；当前 `ServiceSpec` 使用 `${PORT}` / `${LISTEN_ADDR}` 接收候选端口，
Service Manager 在环境支持时检查 socket 归属；不支持时采用明确披露的就绪降级路径。

### 网络能力与就绪降级

Service 继承 Bed 实际选择的网络；netns 不可用时共享 Carrier，并使用统一端口分配域。
网络能力缺席不阻止普通 Service 启动，也不要求额外授予 `CAP_SYS_PTRACE` 来检查
跨用户的文件描述符。显式要求网络隔离或网络策略时仍须满足对应能力；已选用的网络
机制在运行时失败不能静默切换成共享网络。

网络隔离、端口归属和服务就绪是不同事实。独立 netns 限定的是 Bed，不能证明同一 Bed
中某个 listener 属于哪一个 Service。socket 检查区分当前进程组持有、其他进程占用、
尚未监听和无法检查；缺权限或检查工具缺席不能被解释为端口冲突。发现明确冲突才换
端口重试；尚未监听或临时检查故障继续探测并报告未就绪，不能据此终止仍存活的进程。
非法检查结果属于契约错误，真实进程退出仍进入生命周期处理。

归属无法检查时，就绪降级为本次进程存活且 HTTP readiness 成功。Service 状态单独
披露 listener 检查结果、方式和不可用原因，首次降级记录诊断日志。周期检查也遵守
同一规则，不因失去检查权限就杀死仍健康的服务。Ready 表达可用性，不宣称已经证明
endpoint 身份；HTTP 200 或携带 Bearer token 的请求成功均不证明服务验证了 token。

降级路径不能排除受管端口之外的抢占者，也不提供对抗性多租户的网络边界。Port Manager
避免受管服务互相分配冲突端口，但不会拦截任意用户进程的 bind。这一限制在共享网络
下尤其重要，调用方应结合实际网络隔离和 listener 检查状态决定是否接受。

重启先撤销旧 endpoint，确认旧进程、连接及代理 listener 已按各自 owner 清理，再
复用或更换端口。旧实例的延迟回收不能释放新实例的 allocation。Hostel 重启后重新
核对并建立实际监听，不把历史端口记录当作当前预留。

端口 allocation、转发实例、token 和 Execution ID 属于本次运行，而非 Ready 状态。
已发布服务暂时未就绪时保留这些资源和状态中的 endpoint，不主动关闭已有转发连接；
只有运行结束才清理。状态地址表示本次运行的绑定，不保证服务当前可用。

### 外部默认通过 PodIP:port 访问

调用方按 Bed name 与 service name 查询 endpoint；Hostel 返回当前运行实例对应的
地址和就绪状态。共享 Pod 网络时，服务直接监听分配的 Pod 端口；独立 Bed netns 时，
Hostel 持有单独的 Carrier 监听端口，转发到该 Bed 可达的 TCP endpoint：

```text
共享网络：调用方 → PodIP:分配端口 → Bed Service
独立网络：调用方 → PodIP:发布端口 → Hostel TCP 转发 → BedIP:监听端口 → Bed Service
```

两种模式都必须经过统一的端口分配、实例归属和就绪发布规则。代理入口由 Hostel
实际 bind 并持续持有，目标来自 Service Manager 的登记，不允许调用方指定任意目标。
连接与探活必须使用目标 Bed 网络，不能错误连接 Carrier 的同号端口。

PodIP 和动态端口不作为永久业务标识。调用方通过发现结果定位服务，重启或重建后
重新获取地址。端口复用后的旧地址不能成为授权依据，直接访问的服务必须承担业务
认证与作用域校验；同一个静态 token 不自动防止旧地址误路由到其他 Bed。

共享网络直连与独立网络的 TCP 转发均不改写 HTTP 内容，也不重放请求。
转发固定到本次运行实例，
最多 128 个并发连接，每连接最多两小时，停止时关闭已有连接。上传下载、SSE、
WebSocket 的业务正确性仍需部署方结合实际服务验证。

直接访问 Bed Service 不经过 Hostel HTTP 准入，因此调用方须在发起操作前持有 Bed 保留
机制；不能假设 Service Manager 能统计该端口的业务活动。

对外地址取决于 Pod 网络可达性及网络策略，Hostel 不自动创建 Kubernetes Service、
NodePort 或 Ingress。调用方向保持外部到 Service，服务访问 UP/S3 等后端服从 Bed
出站策略。Unix socket 可以作为可选内部 endpoint，但不属于默认 TCP 方案的前提。

### 活跃度不能由常驻进程代替

Service 生命周期完全跟随所属 Bed，没有独立的保活计时。常驻进程本身不永久持有
Bed operation，内部 readiness 轮询和状态查询也不刷新 Bed 保活。当前 access 接口
提供显式的限时 Bed operation 保留，不能把 TCP
连接数当作业务任务完成或 Bed 活跃度的证明。

HTTP 返回不代表服务内部的异步任务已经完成。通用 Service Manager 不能推断这些
业务任务是否仍在写文件；调用方必须通过
既有 Bed 保留机制覆盖任务寿命，服务在停止时取消或等待内部任务。自动重启、后台任务
与 Store 的快照/恢复仍需显式协调，不能把 HTTP 请求结束当作静止写入证明。

### 凭据、配置与独立发布

Hostel 核心不包含具体业务服务代码。Carrier 镜像只负责提供二进制及依赖，上层控制面
把业务需求翻译为通用 `ServiceSpec`。更新服务程序可独立发布 Carrier 镜像；不要求
同时升级 Hostel，也不热替换正在运行的 Bed 二进制。

服务专用凭据从部署方控制的来源按需注入，只保存引用，不写入 Bed metadata、工作区
快照、日志或状态。不能把这些凭据直接放入可被所有 Bed 继承的 Carrier 通用环境。
同 Bed 内的 Service 与用户程序仍处于同一信任边界，独立 env 不提供针对同 UID 进程
读取或 `/proc` 访问的保密保证；需要更强凭据隔离时应单独设计权限或凭据代理能力。

Service 只继承 Carrier 通用环境和 Hostel 注入的 Bed 上下文，再叠加自身解析后的环境；
普通执行的 `Bed.Spec.Env` 不传给 Service。共用 Bed Environment 表达目录、身份和隔离
视图一致，不代表共用所有进程环境变量。普通命令与会话的环境契约见 [kernel.md](kernel.md#执行与进程归属)。

规范化后的非敏感 Bed 服务声明及其摘要随本地 Bed 身份保留。恢复直接使用完整声明，
不依赖部署时仍存在某份模板，也不会因 daemon 配置变化静默改变原 Bed 的启动语义。跨实例创建由上层重新声明服务，
workspace 快照不携带本地进程身份、endpoint 或部署凭据。

## 四、部署配置与 API

### 部署配置

| CLI / 环境变量 | 默认值 | 含义 |
|---|---|---|
| `--service-advertise-host` / `HOSTEL_SERVICE_ADVERTISE_HOST` | `127.0.0.1` | 调用方可达地址；Pod 部署中应注入 Pod IP |
| `--port-range-start` / `HOSTEL_PORT_RANGE_START` | `20000` | 动态 TCP 端口池起点 |
| `--port-range-end` / `HOSTEL_PORT_RANGE_END` | `29999` | 动态 TCP 端口池终点（含） |

`POST /v1/beds` 直接携带完整声明，例如：

```json
{"id":"agent","services":[{
  "name":"example-http",
  "command":["/opt/bed-services/example-http","--listen","${LISTEN_ADDR}"],
  "directory":"/workspace",
  "env":{"WORKSPACE_ID":"workspace-1"},
  "env_files":{"UP_SECRET":"/run/hostel/service-secrets/example/UP_SECRET"},
  "required":true,
  "restart":"on-failure",
  "max_restarts":5,
  "startup_seconds":30,
  "stop_seconds":5,
  "http":{"ready_path":"/ready","token_env":"SERVICE_TOKEN"}
}]}
```

这是进程启动契约示例，Hostel 不自带该程序。Carrier 镜像负责二进制、依赖和可执行的
目录投影。`command` 是 argv，不隐式经过 shell；工作目录使用 BedFS 路径。仅替换监听
占位符，不把 Bed 配置当作 shell 表达式求值。

`env` 只用于可持久化的非敏感值；`env_files` 保存 daemon 可读的绝对路径，并在每次
启动时读取凭据值。Secret 值不进入 Bed identity、状态、日志或 workspace 快照。
创建接口属于可信内部控制面：部署必须限制其访问，并只允许调用方引用专用凭据目录，
不能把任意宿主文件路径开放给非可信租户。规范化声明和摘要随 `.identities/<bed>.local`
保存；客户端不应填写 `spec_digest`，该字段由 Hostel 生成并用于本地恢复校验。

`required` 默认 false；`restart` 默认 `never`，还支持 `on-failure`、`always`。
`max_restarts=0` 使用默认预算 5，最高 100；退避从一秒增长到最多三十秒。
Executor 丢失也在该预算内恢复服务，与应用自然退出的重启策略分开判断。
启动超时默认 30 秒（1–120），停止宽限默认 5 秒（1–30）。
明确端口冲突换端口最多尝试三次，不把任意应用失败归类为端口冲突。

HTTP 服务必须声明 `token_env`。每次运行注入新 token；程序必须校验
`Authorization: Bearer <token>`，就绪检查也使用此凭据。不跟随重定向，
单请求两秒超时，最多 16 个并发 HTTP 探测。
非 HTTP worker 不声明 `http`，仅以进程存活就绪。

统一 TCP 端口管理覆盖 Hostel HTTP、托管 Chromium、Bed 网络的 TCP DNS/连通性
探测、服务监听及发布端口。Chromium 空闲停止仍保留逻辑端口所有权，直到设施关闭；
Bed 回收不会释放它。UDP DNS 仍由网络组件实际绑定和释放，不属于当前 TCP 分配池。

### 管理接口与直连保留

| 接口 | 语义 |
|---|---|
| `POST /v1/beds` 的 `services` 字段 | 创建时启用；省略等于空列表，重复创建不允许修改 |
| `GET /v1/beds/{bed}/services` | 列表，不创建 Bed、不续租 |
| `GET /v1/beds/{bed}/services/{service}` | 状态、Execution/Executor ID 和非敏感 endpoint |
| `GET /v1/beds/{bed}/services/{service}/logs?cursor=0` | 有界 Execution 日志及下一游标 |
| `POST /v1/beds/{bed}/services/{service}/restart` | 异步重启单个服务，返回 202 |
| `POST /v1/beds/{bed}/services/{service}/access` | 传 `{"hold_seconds":300}`，取得 endpoint、token、Execution ID 和限时 hold |
| `DELETE /v1/beds/{bed}/service-holds/{hold}` | 提前释放 hold，幂等 |

Bed 详情的 `status.components.services` 展示服务状态；`GET /v1/status` 的 `ports`
展示 owner、scope、address 和 `reserved/listening` 状态。诊断和持久化声明不含 token。

调用方先获取 access，再直连 endpoint，任务结束后释放 hold。hold 为 1–7200 秒，
到期自动结束 operation，期间普通/显式 eviction 都不能销毁 Bed；daemon shutdown
仍可强制结束。HTTP 返回后仍在执行的异步任务也必须覆盖在 hold 内。
提前释放允许显式 eviction 继续，自动回收仍遵守既有 `retained_until` 保守期限。

未就绪时 access 返回不可用，不发放新的访问结果；恢复后返回同一次运行的地址和凭据。
旧客户端直连 PodIP:port 不经过 access，仍可能建立连接或继续任务。Readiness 只影响
状态与新发现/准入，不承诺阻断所有直连流量；已有 hold 仍按原期限生效。

服务重启或 Bed 重建后重新发现；旧 PodIP/端口与静态 token 不是业务身份。
access 是带凭据的内部管理接口，部署方必须限制 Hostel 控制面访问；本特性不新增
Hostel 入口鉴权，不能将开放的控制面误当作多租户授权边界。

## 五、实现范围与验证边界

`internal/bed/service.Manager` 作为 Bed 领域组件接入既有生命周期。它通过
消费侧接口使用执行能力，不反向依赖 `bed/manager`；后者负责 Environment、Executor
与 Service 的组合。Executor 补齐受控优雅停止能力，保留统一 Execution 退出事实。
Web 层提供服务列表、状态、受限日志、重启与访问保留，Bed 与实例状态提供服务诊断。

daemon 组装 `internal/host/network` 下的 Port Manager，并向需要分配端口的各领域
注入同一实例。Amenity、Bed、Service 与控制面使用统一申请入口；原有监听逐项明确
归属并接入，不能只为 Bed Service 新建一个与其他设施互不知情的私有端口池。

当前范围为创建时完整声明、统一隔离启动、就绪与重启、停止回收和 TCP 发布。
服务集合创建后固定，暂不提供动态安装、依赖图、任意 TCP 代理或独立 start/stop 期望状态。
Web 默认 TCP 监听与 PodIP 端口发布，必须一起实现端口分配、绑定冲突处理和发现；
独立 netns 的对外访问由 Hostel TCP 转发承接。

验收以通用测试服务证明 Hostel 能力，具体业务服务集成由其所属项目验证：

- 同实例内无服务、有服务、相同进程定义多实例的 Bed 共存；重复声明与冲突明确处理。
- 服务与用户命令看到同一文件，身份、网络与资源归属符合 Bed 实际能力。
- 跨 Bed、同 Bed 多实例并发启动不重复分配冲突端口，覆盖共享网络及独立 netns 的发布入口。
- Amenity、Bed、Service 与控制面并发申请时共享冲突检查，Bed 回收不释放共享设施的 listener。
- 已知外部端口占用、端口池耗尽、明确归属冲突和检查权限不足分别处理；能检查时拒绝其他进程的 listener，
  不能检查时如实披露保证较弱，不把降级就绪当作归属证明。
- 无 netns、无 socket 检查权限的服务可启动并通过周期就绪检查；独立 netns 不冒充进程归属证明。
- 重启与回收释放正确的端口 allocation，旧发现地址及端口复用不绕过服务作用域校验。
- 必需服务 readiness、启动超时、退避、Executor 丢失与恢复不破坏初始化/回收顺序。
- 200 → 503/超时 → 200 不改变运行身份、token、端口、转发连接或在途任务；临时 listener
  检查失败不终止进程，明确归属冲突仍按冲突处理，实际退出仍遵守 restart policy。
- 停止与代理/重启并发时无残留进程，旧实例事件和旧连接不影响新实例。
- 空闲常驻服务可随 Bed 回收，真实操作有租约保护，异步任务不能获得虚假的完成证明。
- 流式上传下载、断连取消、服务鉴权头保留以及未登记 endpoint 的拒绝行为正确。
- 凭据值不进入状态和持久化声明，本地恢复使用固定的完整声明，原有无服务 Bed 契约保持成立。

单元测试验证声明、状态和协调；真实隔离与进程清理由现有 Linux 单机 E2E 验证，
按项目显式执行规则运行。单测与交叉编译不等于 Linux E2E 证明，尤其 netns、实际权限
组合和具体业务服务集成必须在对应环境显式验证；上面的验收目标不表示已全部执行通过。
