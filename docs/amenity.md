# Amenity：共享设施与 Bed 状态

## 一、理念与概念

Amenity 是独立于 Bed 的设施，由 daemon 下的 Amenity Manager 管理。Tenant 是 Hostel
为服务 Bed 而定义的设施使用单元；具体资源如何实现属于设施内部细节。Chromium 可以为 Tenant
分配 BrowserContext，MCP 可以为 Tenant 管理配置和连接池，不要求 Tenant 对应独立进程。Tenant 表达使用归属，期望按 Tenant 隔离但不强求；共享资源或独立资源均由设施决定，Tenant 的存在不构成额外隔离保证。

Bed Manager 解析调用方的 Bed Name，Amenity Manager 以本地 Bed ID 维护到各设施 Tenant ID
的绑定。Tenant ID 在设施内唯一、生命周期内稳定，不包含 Bed 名称语义；同名 Bed 重建使用新的
本地身份，不能继承旧绑定。Tenant 内部资源可以重建而不改变 Tenant 身份。

设施拥有 Tenant 及具体资源，Amenity Manager 拥有绑定与协调责任。一个 Bed 在每个设施下
按需绑定一个 Tenant，未使用的设施不分配。获取动作受 Bed operation/session 准入保护，绑定
创建与回收串行协调；设施之间的实现差异由各自类型化动作接口承接。

产物归属、访问屏障和持久化分别判断：截图与下载写入对应 Bed workspace，file API 按
BedFS 读取，Store 按 Volume persistence 选择完整持久卷。产物落在 Bed 目录，并不自动保证其他 Bed 进程
无法访问；这仍依赖文件机制。BrowserContext 的 cookie/localStorage 等运行态不进入快照。

## 二、主流程

```text
实例启动：注册设施 → Start 初始化并探测 → 不可用设施保留状态和原因
Bed 首次使用：创建 Tenant 并绑定 → 按需分配具体资源 → 执行动作或建立代理会话
实际使用：operation 或 session 流量更新 Bed 活跃度
Bed 回收：先撤销 session → 关闭各绑定 Tenant 并撤销凭据 → 成功后移除绑定
设施空闲：自行决定是否停止；共享进程故障可能丢失多个 Tenant 的资源，Tenant 身份仍可保留
```

Chromium 支持 launch 或 attach。launch 由 Hostel 惰性启动浏览器，最后一份浏览器资源释放后
按空闲期限停止；attach 连接部署方提供的浏览器，只管理 Bed 切片，不拥有外部进程生死。
浏览器不可用不阻止其他 Hostel 能力启动，使用浏览器的请求明确失败。

MCP 在 daemon 内维护连接池，没有独立子进程；调用期间持有 operation，空闲连接不阻止
Bed 回收。连接身份、请求快照和超时契约见下文 MCP 章节。

## 三、关键设计

### 通过 Bed 入口访问共享设施

原始 browser 级 CDP endpoint 可以操作整个共享浏览器，不能直接作为某个 Bed 的能力
下发。Hostel 提供 Bed 级浏览器动作，以及带 token 和 context 过滤的 CDP 代理：

```text
浏览器动作 API → 按 Bed 找到 BrowserContext → 执行动作
Bed 内客户端 → Bed 级 CDP endpoint → 校验 token、过滤协议 → 共享 Chromium
```

动作 API 覆盖导航、文本、截图和常用交互。CDP 代理让 Playwright 等客户端复用同一份
浏览器切片。API 使用方法见 [README](../README.md)，协议过滤的实现锚点是
`internal/amenity/chromium_cdp_proxy.go`。

当前代理强制部分命令使用本 Bed context，过滤部分跨 context 事件与响应，并阻止
关闭、崩溃共享浏览器的命令。这是有限过滤，未对全部 CDP 指令、target/session ID 和
事件做完整授权校验；不下发邻居 target ID 也不能替代授权检查。它面向可信或半可信
客户端，不承诺对抗性协议访问下的完整隔离。

Bed 级动作、配置与 browser/info 属于可信控制面，入口认证和 Bed 授权由接入方负责。
代理 token 保护代理连接，不能替代给 token 的接口本身的授权。原始 CDP 只监听 Carrier
loopback 也不代表对 Bed 不可达：共享网络时 Bed 仍可能访问它；netns 可用时同样需要
核对 Carrier 服务的实际可达范围。部署必须结合 [network.md](network.md) 判断边界。

### Bed 凭据与设施切片有不同寿命

CDP token 属于 Bed，由进程环境或 browser/info 下发；铸造 token 不启动 Chromium，
首次代理拨号才准备浏览器资源。browser/close 只回收资源，Tenant 身份和 token 保留，以便常驻 shell
继续使用已注入的 endpoint。共享 Chromium 重启同样不撤销 Bed token。

Bed teardown 关闭绑定的 Tenant，先撤销 token，再清理资源。已有 CDP 连接由 Bed session
生命周期撤销，不能只删除 token 而放任旧连接继续使用设施。关闭失败的 Tenant 禁止重新获取
凭据，仍保留资源清理责任。完整回收次序见 [bed.md](bed.md)。

### 两级状态由领域拥有

设施提供全局 Amenity Status，Tenant 提供该设施使用单元的状态。两者字段由设施自身定义：
Chromium 报告浏览器资源是否就绪、是否等待清理；MCP 报告配置的服务数量、连接条目、活跃调用
和关闭状态。Manager 只聚合，不把不同设施统一解释成一份通用资源状态。

`/v1/status` 的 `amenities` 汇总设施状态；`/v1/beds/:id` 的 `status.amenities` 通过绑定查询
关联 Tenant，包含 Tenant ID 和领域报告。Tenant 状态不复制进 Bed 模型；没有绑定就不列入。
观察不创建资源、不续租、不访问远端，也不返回凭据或底层资源句柄。详见 [observability.md](observability.md)。

### 应用切分不能覆盖进程、网络和资源边界

| 维度 | 当前兑现方式与限制 |
|---|---|
| 应用状态 | BrowserContext 切分 cookie、localStorage 等状态；MCP 按 Bed 配置和连接身份切分 |
| 文件 | 设施在 Bed 执行边界之外写入对应 workspace；产物访问与快照范围分别由 BedFS、文件机制和 Store 决定 |
| 网络 | Chromium/MCP 使用 Carrier 网络，不继承命令的 netns；浏览器 per-bed 代理出站尚未实现 |
| 资源 | 共享设施计入 Carrier 总量，当前未实现 Chromium 专用 cgroup 预算，也不声称精确归因到每个 Bed |
| 故障 | 共享进程或 daemon 的故障可能影响多个 Bed；重新连接不恢复已丢失的浏览器运行态 |

这使 Amenity 成为 Bed 隔离机制需要覆盖的执行路径，而非理想独立性之外的例外。
网络治理见 [network.md](network.md)，资源预算与归因边界见 [resource.md](resource.md)。

### 释放失败保留清理责任

Amenity Manager 尝试关闭所有绑定 Tenant，汇总错误。成功项立即移除绑定，失败项保留
身份和清理责任；Bed Manager 因此保留待清理 Bed，重试从未完成的资源继续。
Chromium 只有确认 Context 已释放或已随自有浏览器消失后才放弃资源记录。

设施的 Start 完成初始化后返回，重型资源保持惰性分配。后台维护属于设施，Close 负责取消和
等待退出。attach 模式仅清理 Hostel 创建的 Context 和连接，不能关闭外部浏览器。

## 四、验证边界

项目既有单元测试与 [单机 E2E](../tests/e2e/README.md) 覆盖 context/配置切分、产物路径、
token 生命周期、按需启停和回收。浏览器相关验证需要实际 Chromium；纯协议过滤测试
不能证明完整 CDP 隔离，也不能替代 Playwright 实际流量与网络绕过路径验证。

新增设施时分别验证自己的状态归属、凭据、产物、出站与释放失败；设施可用状态只表示
能提供服务，不代表这些维度都已达到理想隔离。

### 并发与状态读取

设施初始化与昂贵资源启动分开：Start 完成必要初始化，首次使用可以惰性启动资源，空闲时可以回收资源并保留 Tenant 身份和有效凭据。共享启动由设施协调；Tenant 资源操作按 Tenant 协调，不因一个 Tenant 的远端慢操作串行化所有 Tenant。

状态锁只保护内存快照，不跨浏览器启动、CDP 调用或资源释放。全局与 Tenant Status、凭据读取均不等待这些操作；超时或取消终结等待，部分资源仍保留清理归属。关闭 Tenant 先撤销凭据，再清理资源；设施关闭阻止新工作，并等待自己拥有的后台任务退出。

## MCP

Hostel 在 carrier 内维护远程 MCP 连接，每个 bed 拥有独立的配置和连接池。
MCP server 可以位于外部；设施支持 SSE 和 Streamable HTTP 的工具发现与执行，
不启动 stdio server，不承担上层平台的工具授权、调度或持久化配置。

配置与连接切分属于 [Bed 隔离](isolation.md) 的设施维度，整体所有权见
[amenity.md](amenity.md)。它不为远端 MCP server 创建租户，也不保证远端存储按 Bed 隔离；
远端数据授权取决于所提供的凭据及服务自身策略。

### 调用与隔离

HTTP 请求沿用 `X-Hostel-Bed`（或 `?bed=`）选择 bed，省略时使用 default。
这些接口与 `/command` 一样属于可信控制面：bed 标识仅负责路由，不是认证凭据，
接入方负责入口认证和 bed 访问授权。设施使用 carrier 网络和 HTTP 客户端的出网策略，
不声称继承某个 bed executor 的网络隔离。

- `PUT /v1/mcp/config` 原子替换该 bed 的默认 bundle。
- `POST /v1/mcp/servers/{name}/tools/list` 使用默认 bundle 列举工具。
- `POST /v1/mcp/servers/{name}/tools/call` 优先使用请求自带 bundle，否则使用默认配置。

Bundle 包含 `revision`、`servers`、可选 `secrets`。Server 包含 `url`、
`transport`（默认 `streamable_http`）、可选 `headers` 与 `timeoutMs`。
Header 的 `${NAME}` 只从本 bundle 的 secrets 展开，缺失时拒绝配置，绝不读取 carrier env。
调用包含 `name`、可选 `arguments` 对象、`_meta` 和 `bundle`。
返回 MCP SDK 的完整工具结果，包括 `content`、`structuredContent`、`isError`；
`isError=true` 是工具执行结果，HTTP/连接失败则以非 2xx 报告。

连接身份由 bed、revision、配置内容摘要与 server name 共同决定。不能只信任调用方的
revision：相同 revision 配不同 header 也必须建立不同连接。请求快照与默认配置独立，
并发更新默认 bundle 不改变已开始调用的凭据。配置输入按值保存，不持有调用方可变 map。
同一连接的操作串行，不同连接可以并行。

### 连接生命周期

一次 MCP 请求是 bed operation，执行期间禁止普通 eviction。空闲连接只是可丢弃的
amenity 状态，不计作活跃 operation，不让 bed 永久 pinned。Bed evict/purge 或 daemon
退出调用 `ReleaseTenant`，取消在途请求、关闭连接并清除内存配置。
恢复后的 bed 配置为空；上层重新 PUT 或在 call 携带 bundle 即可恢复执行。

每个 pool 有最大连接数、空闲回收与操作 deadline；达到连接上限时优先淘汰空闲连接，
全部忙时返回容量错误。相同配置复用连接，握手受请求超时控制，建立后的 SSE 流随
pool 存活，不随第一次调用结束而关闭。协议或网络失败丢弃连接，由下一次调用重建；
当前工具调用不自动重放，因为响应丢失时无法判断有副作用的工具是否已执行。

连接池、操作期限和输入大小均有上限。`pkg/mcpproxy.Options` 供嵌入方配置客户端、容量与期限。默认 HTTP client 拒绝重定向，部署方可注入统一 TLS/egress/tracing client。

### 嵌入与验证

- `pkg/mcpproxy` 是有意公开的单 sandbox Go API：`New`、`Configure`、`ListTools`、
  `CallTool`、`Close`。它不依赖 bed、Gin 或平台业务，可以由其它 sandbox runtime 复用。
- `internal/amenity/mcp.go` 将 pool 生命周期绑定到 bed。
- `internal/api/apiv1/handler/mcp.go` 负责请求大小、operation、HTTP 错误及低敏日志。

SDK 负责 MCP 握手、分页和传输；普通日志只记录 bed、路由、状态和耗时，不记录 bundle、
secret、header 或工具参数，也不回显可能包含凭据的上游 transport 错误正文。

`pkg/mcpproxy` 测试覆盖两种真实 MCP transport、配置版本并发、结果与元数据、取消、
容量和连接回收；`tests/e2e` 使用真实 Hostel 二进制验证两个 bed 的配置/调用隔离和清理。
运行方式见 [单机 E2E](../tests/e2e/README.md)。
