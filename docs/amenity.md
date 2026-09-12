# Amenity：共享设施与 Bed 状态

## 一、理念与概念

Amenity 在 Carrier 内复用重型进程或连接管理，为每个 Bed 分配自己的应用状态。
Chromium 使用 BrowserContext，MCP 使用按 Bed 管理的配置和连接池；Jupyter 尚未实现。
复用可以降低启动与常驻成本，但应用状态切分只兑现部分独立性。统一目标与信任边界见
[isolation.md](isolation.md)。

设施实例拥有共享运行态，Bed 拥有自己的 tenant 切片。Registry 把 Bed 回收接到各设施的
释放入口；具体协议由设施实现负责，HTTP 适配留在 web 层。设施可以有自己的生命周期，
不要求一份切片对应一个 OS 进程。

产物归属、访问屏障和持久化分别判断：截图与下载写入对应 Bed workspace，file API 按
BedFS 读取，Store 按配置选择快照子树。产物落在 Bed 目录，并不自动保证其他 Bed 进程
无法访问；这仍依赖文件机制。BrowserContext 的 cookie/localStorage 等运行态不进入快照。

## 二、主流程

```text
实例启动：注册设施，探测可用性，报告 unavailable / idle / running
Bed 首次使用：启动或连接设施 → 分配本 Bed 切片 → 执行动作或建立代理会话
实际使用：operation 或 session 流量更新 Bed 活跃度
Bed 回收：先撤销 session → 释放设施切片并撤销 Bed 级凭据
设施空闲：自行决定是否停止；共享进程故障可能同时丢失多个 Bed 的切片
```

Chromium 支持 launch 或 attach。launch 由 Hostel 惰性启动浏览器，最后一个 tenant 释放后
按空闲期限停止；attach 连接部署方提供的浏览器，只管理 Bed 切片，不拥有外部进程生死。
浏览器不可用不阻止其他 Hostel 能力启动，使用浏览器的请求明确失败。

MCP 在 daemon 内维护连接池，没有独立子进程；调用期间持有 operation，空闲连接不阻止
Bed 回收。连接身份、请求快照和超时契约见 [mcp.md](mcp.md)。

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
首次代理拨号才准备 tenant。browser/close 只回收浏览器切片，token 保留，以便常驻 shell
继续使用已注入的 endpoint。共享 Chromium 重启同样不撤销 Bed token。

Bed teardown 才通过 `RevokeBedSecrets` 撤销 token，防止旧凭据授权同 ID 的下一次 Bed。
已有 CDP 连接由 session 生命周期撤销，不能只删除 token 而放任旧连接继续使用设施。
完整回收次序见 [kernel.md](kernel.md)。

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

Registry 尝试释放所有设施并撤销 Bed 级凭据，汇总释放错误。任一设施失败都会让 Bed
保留为待清理身份，阻止同 ID 重建；重试 Evict/Purge 或实例关闭时继续释放。
Chromium 仅在 context 释放成功或承载实例已停止后移除本地 tenant 记录，释放失败保留
记录供重试。因此 API 返回清理失败时，不应假定外部切片已经消失。

## 四、验证边界

项目既有单元测试与 [单机 E2E](../tests/e2e/README.md) 覆盖 context/配置切分、产物路径、
token 生命周期、按需启停和回收。浏览器相关验证需要实际 Chromium；纯协议过滤测试
不能证明完整 CDP 隔离，也不能替代 Playwright 实际流量与网络绕过路径验证。

新增设施时分别验证自己的状态归属、凭据、产物、出站与释放失败；设施可用状态只表示
能提供服务，不代表这些维度都已达到理想隔离。
