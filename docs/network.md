# 网络管理

## 理念与边界

`network.Manager` 是实例级组件，管理 resident Bed 的网络资源。网络与文件隔离档正交；
Bed 的每次命令和常驻 shell 使用相同 netns，Executor 替换不改变该网络身份。
当前覆盖 `bed_processes`：共享 Chromium/MCP 等 amenity 的出站仍走 carrier 网络。

网络能力自动启用，没有默认要求部署提权的开关。启动探测失败不影响服务启动；普通 Bed
继续共享 carrier 网络。探测成功后，单个 Bed 创建网络失败会使该次初始化失败，不能在
同一实例中静默改成共享网络。运行中权限撤销也不会自动放开已隔离 Bed。

## 主流程

启动时检查 Linux、`ip`/`nft`/`setpriv`、现有 IPv4 forwarding 和 DNS，然后创建临时
netns、veth、路由、nft 表与 DNS 转发入口，执行真实的 namespace 进入和能力丢弃，清理
后缓存 verdict。诊断请求不会重新运行探测。

Bed 初始化完成数据准备后分配网络，网络完成后才发布 resident。所有 Executor 的 Start
入口先进入 Bed netns，再执行文件隔离和用户命令。进入后丢弃 capability bounding、
inheritable 与 ambient sets，避免把网络管理权限交给 Bed 程序。

回收先停止 Bed 的命令和 shell、释放 amenity，再删除网络；失败的网络清理仍由 Manager
持有并在关闭时重试。网络地址和 namespace 不写入 workspace，不随 Store 恢复。

## 实现约束与诊断

- 当前 Linux backend 使用 iproute2 的 named netns，要求能完成 netns 创建/进入所需的
  `SYS_ADMIN`、veth/路由/nft 所需的 `NET_ADMIN`，以及相应 mount/seccomp/LSM 操作。
  单独 `NET_ADMIN` 不等于可用；当前 backend 不实现 userns 辅助创建路径。
- 需要 carrier 已开启 IPv4 forwarding；Hostel 不修改全局转发 sysctl。官方镜像包含所需
  工具，自定义镜像缺工具会在诊断中显示具体缺项。
- 从 `198.18.0.0/15` 选择与现有非默认路由不重叠的 `/30`，每 Bed 一对 veth。nft 规则
  只作用于分配的网络，拒绝跨 Bed 转发和 IPv6 转发；现有 CNI/防火墙限制仍然生效。
- DNS 由有界的 UDP/TCP 转发器使用 carrier resolver，兼容 carrier 内的 loopback DNS。
- Bed 内的 CDP 地址使用 gateway 访问共享 Hostel API；使用该能力时 API 需监听可从
  veth 访问的地址（默认 `:8872`），不是仅监听 carrier loopback。
- 这些边界不等于安全容器：Hostel 的可信/半可信代码模型不变。当前没有域名/CIDR policy
  注入 API、透明 MITM 或 Credential Vault，不声明与 hisandbox egress 等价。

`GET /v1/diagnostics` 和 `/healthz` 的 `network` 返回启用状态、backend、作用域、失败
原因和启动 probe（attempted、stage、duration_ms、error）。`enabled=false` 是网络能力
缺席，不是 Hostel 健康检查失败。`/v1/isolated` 的 share_net 如实反映该状态，显式请求
与实例能力冲突会报不支持。

共享 Chromium 的 BrowserContext 不是 OS 网络边界。待办是在创建 Context 时设置各 Bed
的 `proxyServer`，代理按 Bed 的网络策略拨号，并覆盖 bypass、QUIC/WebRTC 等路径。
该能力完成前浏览器不宣称受 Bed 网络隔离约束；待办统一记录于 `backlog.md`。
