# MCP 共享设施

Hostel 在 carrier 内维护远程 MCP 连接，每个 bed 拥有独立的配置和连接池。
MCP server 可以位于外部；设施支持 SSE 和 Streamable HTTP 的工具发现与执行，
不启动 stdio server，不承担上层平台的工具授权、调度或持久化配置。

配置与连接切分属于 [Bed 隔离](isolation.md) 的设施维度，整体所有权见
[amenity.md](amenity.md)。它不为远端 MCP server 创建租户，也不保证远端存储按 Bed 隔离；
远端数据授权取决于所提供的凭据及服务自身策略。

## 调用与隔离

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

## 生命周期

一次 MCP 请求是 bed operation，执行期间禁止普通 eviction。空闲连接只是可丢弃的
amenity 状态，不计作活跃 operation，不让 bed 永久 pinned。Bed evict/purge 或 daemon
退出调用 `ReleaseTenant`，取消在途请求、关闭连接并清除内存配置。
恢复后的 bed 配置为空；上层重新 PUT 或在 call 携带 bundle 即可恢复执行。

每个 pool 有最大连接数、空闲回收与操作 deadline；达到连接上限时优先淘汰空闲连接，
全部忙时返回容量错误。相同配置复用连接，握手受请求超时控制，建立后的 SSE 流随
pool 存活，不随第一次调用结束而关闭。协议或网络失败丢弃连接，由下一次调用重建；
当前工具调用不自动重放，因为响应丢失时无法判断有副作用的工具是否已执行。

默认每 bed 最多 32 条连接，操作上限 30 秒，闲置 5 分钟后由周期回收检查关闭。
`timeoutMs` 可以缩短单个 server 的预算，不能越过进程上限。HTTP 请求 body 上限 1 MiB。
`pkg/mcpproxy.Options` 供嵌入方设置客户端、连接上限及 timeout；Hostel 使用内置默认值，
不增加部署 YAML。默认 HTTP client 拒绝重定向，部署方可注入统一 TLS/egress/tracing client。

## 代码与验证

- `pkg/mcpproxy` 是有意公开的单 sandbox Go API：`New`、`Configure`、`ListTools`、
  `CallTool`、`Close`。它不依赖 bed、Gin 或平台业务，可以由其它 sandbox runtime 复用。
- `internal/amenity/mcp.go` 将 pool 生命周期绑定到 bed。
- `internal/web/mcp.go` 负责请求大小、operation、HTTP 错误及低敏日志。

SDK 负责 MCP 握手、分页和传输；普通日志只记录 bed、路由、状态和耗时，不记录 bundle、
secret、header 或工具参数，也不回显可能包含凭据的上游 transport 错误正文。

`pkg/mcpproxy` 测试覆盖两种真实 MCP transport、配置版本并发、结果与元数据、取消、
容量和连接回收；`tests/e2e` 使用真实 Hostel 二进制验证两个 bed 的配置/调用隔离和清理。
运行方式见 [单机 E2E](../tests/e2e/README.md)。
