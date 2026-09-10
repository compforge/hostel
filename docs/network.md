# 网络管理

## 理念与边界

网络隔离沿 Bed 的独立执行空间目标按环境能力尽量兑现，统一模型与组合约束见
[isolation.md](isolation.md)。

`network.Manager` 是实例级组件，管理 resident Bed 的网络资源。网络与文件隔离档正交；
Bed 的每次命令和常驻 shell 使用相同 netns，Executor 替换不改变该网络身份。
当前覆盖 `bed_processes`：共享 Chromium/MCP 等 amenity 的出站仍走 carrier 网络。

网络能力自动启用，没有默认要求部署提权的开关。网络候选探测失败不影响服务启动；普通 Bed
继续共享 carrier 网络。探测成功后，单个 Bed 创建网络失败会使该次初始化失败，不能在
同一实例中静默改成共享网络。运行中权限撤销也不会自动放开已隔离 Bed。

## 主流程

启动时检查 Linux、`ip`/`nft`/`setpriv`、现有 IPv4 forwarding 和 DNS，然后创建临时
netns、veth、路由、nft 表与 DNS 转发入口，执行真实的 namespace 进入和能力丢弃，清理
后缓存 verdict。诊断请求不会重新运行探测。

Bed 初始化完成数据准备后获取具体 `Attachment`，保存到 resident Bed 的
`isolation.Environment`，准备完成后才发布 Ready。命令和 shell 共用这个组合入口：
先进入 netns，再准备文件视图，最后切换 UID（如需要）、清空 capability bounding /
inheritable / ambient sets 并设置 `no_new_privs`，然后启动用户程序。Network Manager
只负责网络进入，不自行决定文件边界前后的降权时机。实例在 HTTP 启动前还会实测选中的
完整命令与 shell 组合；组合失败明确阻止启动。

回收先停止 Bed 的命令和 shell、释放 amenity 与资源组，再删除网络。关闭失败的
Attachment 保留清理 owner，但立即失去执行资格；同 ID Acquire 必须先清理残留才能
分配新网络，旧句柄不能删除新分配。Bed Manager 同时保留待清理身份，阻止重建，并允许
Evict/Purge 或实例关闭重试。网络地址和 namespace 不写入 workspace，不随 Store 恢复。
初始化已分配网络但未发布 resident 时，先回收运行资源再通知初始化结束；回收失败继续
保留身份与容量名额。

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
- 这些边界不等于安全容器：Hostel 的可信/半可信代码模型不变。透明 MITM 和 Credential Vault 不在当前能力范围。

`GET /v1/diagnostics` 和 `/healthz` 的 `network` 返回启用状态、backend、作用域、失败
原因和启动 probe（attempted、stage、duration_ms、error）。`enabled=false` 是网络能力
缺席，不是 Hostel 健康检查失败。`/v1/isolated` 的 share_net 如实反映该状态，显式请求
与实例能力冲突会报不支持。

共享 Chromium 的 BrowserContext 不是 OS 网络边界。待办是在创建 Context 时设置各 Bed
的 `proxyServer`，代理按 Bed 的网络策略拨号，并覆盖 bypass、QUIC/WebRTC 等路径。
该能力完成前浏览器不宣称受 Bed 网络隔离约束；待办统一记录于 `backlog.md`。

组合与回收验证见 [单机 E2E](../tests/e2e/README.md) 和 [isolation.md](isolation.md)。

## Bed 网络策略

NetworkManager 同时拥有 Bed netns 和有效策略。控制面可以在 `POST /v1/beds` 的
`networkPolicy` 字段指定初始策略，规则安装成功后才发布 Bed 就绪；重复创建相同 Bed
不会重置运行期策略，初始参数冲突则拒绝。未启用网络能力时，带策略的创建请求返回 503，
不带策略的创建继续使用原有降级行为。`/v1/beds/capabilities` 的 `network_policy` 表示
API 是否支持，实际可用性仍看 `network.enabled`。

每个现存 Bed 提供 `/v1/beds/{id}/network/policy`：GET 查询、POST/PUT 替换、PATCH 合并
规则数组、DELETE 删除 target 字符串数组。`/network/healthz` 查询同一 Bed 的有效状态。
这些控制面接口按路径中的 Bed 定位，不接受 header 覆盖；操作引用防止执行期间被回收。
缺少 Bed 返回 404，网络管理未启用返回 503，非法规则返回 400，nft 提交失败返回 500。
查询不会创建 Bed，也不会把共享网络报成 enforcing。

策略包含 `defaultAction: allow|deny` 和 `egress: [{action,target}]`；target 接受 IP、CIDR、
域名和 `*.example.com` 通配域名。省略 defaultAction 时，空规则允许全部，有规则则默认拒绝。
IP/CIDR 拒绝优先于允许；域名拒绝由 Bed DNS 执行，默认拒绝策略下，仅在 nft 成功安装解析结果
后才返回 DNS 响应。放行地址使用 DNS TTL（最多 300 秒）到期，受每 Bed 4096 个地址上限约束。
Bed 进程只能向本 Bed DNS gateway 的 53 端口查询；IP 规则控制实际连接，域名规则不解释
HTTPS、DoH 或已经由调用方掌握的 IP，不能作为 HTTP Host/SNI 校验或 MITM 的替代。

策略更新使用原子 nft transaction，失败保留旧策略；更新清除旧的 DNS 放行集，旧版本下
尚未返回的 DNS 响应不能重新开放地址。已缓存 DNS 的客户端在收紧规则后可能需要重新解析。
规则在 Bed 自己的网络 namespace 内执行，不修改 carrier 默认出站策略；现有跨 Bed 隔离
规则仍然成立。当前网络 backend 仅提供 IPv4 外部连通，IPv6 转发仍被阻断。

响应的 `scope=bed_processes` 明确限定命令和 shell；共享 Chromium、Hostel 代办的文件下载
等 carrier 进程不在其作用域。策略随 resident Bed 网络存在，evict 后销毁，不随 workspace
Store 保存；控制面重建 Bed 时需重传初始策略。透明凭据注入与 browser proxy 仍是独立能力。
