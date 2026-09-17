# 网络管理

`internal/host/network` 提供通用网络 allocation、DNS 与规则执行；Bed Network Manager 负责 Bed 绑定、可选启用策略与状态发布。分层边界见 [核心架构](kernel.md#领域与-host-机制)。

## 理念与边界

网络隔离沿 Bed 的独立执行空间目标按环境能力尽量兑现，统一模型与组合约束见
[isolation.md](isolation.md)。

`network.Manager` 是实例级组件，管理 resident Bed 的网络资源。网络与文件隔离档正交；
Bed 的每次命令和常驻 shell 使用相同 netns，Executor 替换不改变该网络身份。
当前覆盖 `bed_processes`：共享 Chromium/MCP 等 amenity 的出站仍走 carrier 网络。

Network 定义 shared/private 两级：dorm/room 预期 shared，suite/auto 预期 private。它按实际探测与配置选择，
不从文件 backend 推导；shared 满足到 room 的网络要求，private 满足 suite。没有默认要求部署提权的开关。网络候选探测失败不影响服务启动；普通 Bed
继续共享 carrier 网络。探测成功后，单个 Bed 创建网络失败会使该次初始化失败，不能在
同一实例中静默改成共享网络。运行中权限撤销也不会自动放开已隔离 Bed。

## Bed PortMapping

`Bed.Spec.PortMappings` 是命名的 TCP 端口需求；与 PathMapping 一样，调用方声明逻辑关系，
Hostel 根据实际能力兑现。Service 通过 `port_mapping` 引用其中一项，不再自行持有端口分配策略。
当前由托管 Service 消费映射，每项最多绑定一个 Service；没有消费者的声明不预占运行端口。

```yaml
port_mappings:
  - name: workspace-http
    protocol: tcp
    bed_port: 8080
    require_bed_port: false
    publish: true
services:
  - name: workspace-api
    port_mapping: workspace-http
    # command / env 通过 ${PORT} 或 ${LISTEN_ADDR} 接收实际监听地址
```

`protocol` 默认 `tcp`；`bed_port=0` 表示自动分配。非零端口默认是偏好，
`require_bed_port=true` 才是硬要求，必须同时指定非零 `bed_port`。
`publish` 显式控制 Carrier 入口与外部地址发现，默认 false。Host 端口由统一端口池动态分配，
不接受静态 `host_port`，也不写入持久化声明。

| 实际网络 | Bed 内监听 | Carrier 发布 | Bed 内访问 |
|---|---|---|---|
| private netns | 优先使用 `bed_port`；未指定或偏好冲突时动态分配 | 独立 Host 端口，TCP 转发至 Bed IP | `127.0.0.1:bed_port` |
| shared | 普通请求使用 Host 动态端口池；硬要求才尝试指定端口 | 与 Bed 监听为同一个端口，无额外转发 | `127.0.0.1:host_port` |

硬要求冲突直接失败，不换端口。偏好降级原因通过 `reason` 披露；共享网络下的典型原因是
`SharedNetworkUsesHostPort`。这不会提升网络隔离等级，也不能让共享网络中的每个 Bed 都使用
`127.0.0.1:8080`。需要该假设的程序必须消费发现结果或修改监听配置。

`publish=false` 时不返回外部地址、不创建转发；共享网络注入 loopback 监听地址。
私有网络中的 listener 仍需供 Carrier readiness 探测。发布选项是监听/发现契约，不是防火墙：
程序自行忽略 `${LISTEN_ADDR}` 或其他进程主动 bind 不受它约束。

### 发现、归属与回收

Bed 详情直接返回 `status.services` 和 `status.port_mappings`；`status.components.network`
继续描述隔离能力与网络状态。每项映射报告 `name`、`protocol`、`service`、`execution_id`、
`network`、`state`、`bed_port`、`host_port`、`internal_address`、`external_address` 和降级 `reason`。
`host_port` / `external_address` 仅在发布时存在；内部地址相对于该 Bed 的网络视图。

HTTP Service 的 access 同时返回 `internal_endpoint`、外部 `endpoint`、`port_mapping` 和
`execution_id`，有认证时另含 token。Bed 内进程消费内部地址，外部调用方消费外部地址；
两种网络模式使用同一接口。未发布服务的外部 endpoint 为空。地址属于当前 execution，
重启后必须重新发现，不能将端口当成稳定身份。

`internal/bed/network.PortMappings` 复用 Host PortManager/TCPForwarder，拥有 reserve、publish、
withdraw、release；Service 仅负责进程、HTTP readiness 和重启，并绑定具体映射句柄。
准备顺序是网络就绪 → 预留 Bed 端口 → 启动进程 → Service 就绪 → 发布 Host 入口。
纯 TCP 服务没有 HTTP readiness 时，发布以进程存活为准，不宣称应用已能服务请求。

Readiness 暂时丢失保留本次运行及连接，但拒绝新 access。真正停止时先撤销发现与转发、
再停止进程、最后释放内部端口；清理失败保留责任并重试。网络 namespace 在映射回收后才可释放。
同名重建和重启不能让旧句柄释放新资源。持久化只记录声明，恢复时重新分配；
不恢复旧 Host 端口、endpoint 或 execution。重建 Bed 时调用方重新提交完整声明。

## 主流程

启动时检查 Linux、`ip`/`nft`、现有 IPv4 forwarding 和 DNS，然后创建临时
netns、veth、路由、nft 表与 DNS 转发入口，执行真实的 namespace 进入和能力丢弃，清理
后缓存 verdict。诊断请求不会重新运行探测。

Bed 初始化完成数据准备后获取具体 `Attachment`，保存到 resident Bed 的
`manager.Environment`，准备完成后才发布 Ready。命令和 shell 共用这个组合入口：
网络进入发生在最终降权之前；Network Manager 只负责进入所分配的 netns，完整的身份切换、
文件视图与用户程序启动顺序由 [权限模型](privilege.md#特权操作顺序) 统一定义。
实例在 HTTP 启动前还会实测选中的完整 command/session/Service 组合；自动候选可在完整清理后有限回退，required 或清理失败则阻止启动。

网络分配提供 Bed resolver 文件，Bed Manager 在组装 mount 视图前交给 Filesystem，
只读挂到 `/etc/resolv.conf`。这样进入已准备的 mount namespace 不会丢失 `ip netns exec`
临时挂入的 resolver。该文件属于网络 allocation，不是用户 PathMapping；显式映射与其
目标冲突时拒绝准备，不静默覆盖。PRoot/Carrier 视图沿用外层 netns 的 resolver。

回收先停止 Bed 的命令和 shell、释放 amenity、资源组与文件视图，再删除网络。关闭失败的
Attachment 保留清理 owner，但立即失去执行资格；同 ID Acquire 必须先清理残留才能
分配新网络，旧句柄不能删除新分配。Bed Manager 同时保留待清理身份，阻止重建，并允许
Evict/Purge 或实例关闭重试。网络地址和 namespace 不写入 workspace，不随 Store 恢复。
初始化已分配网络但未发布 resident 时，先回收运行资源再通知初始化结束；回收失败继续
保留身份与容量名额。实例关闭会等待仍在创建的网络完成发布或回收；创建记录只在 endpoint
清理结束后移除，endpoint 自身串行化所有清理入口，因此关闭超时后的重试不会与上一轮清理并发。

## 实现约束与诊断

- 当前 Linux backend 使用 iproute2 的 named netns，要求 namespace 管理和网络管理能力，以及
  相应 mount/seccomp/LSM 操作；完整权限矩阵见 [privilege.md](privilege.md)。单独
  `NET_ADMIN` 不等于可用；当前 backend 不实现 userns 辅助创建路径。
- 需要 carrier 已开启 IPv4 forwarding；Hostel 不修改全局转发 sysctl。官方镜像包含所需
  工具，自定义镜像缺工具会在诊断中显示具体缺项。
- 从 `198.18.0.0/15` 选择与现有非默认路由不重叠的 `/30`，每 Bed 一对 veth。nft 规则
  只作用于分配的网络，拒绝跨 Bed 转发和 IPv6 转发；现有 CNI/防火墙限制仍然生效。
- DNS 由有界的 UDP/TCP 转发器使用 carrier resolver，兼容 carrier 内的 loopback DNS。
- Bed 内的 CDP 地址使用 gateway 访问共享 Hostel API；使用该能力时 API 需监听可从
  veth 访问的地址（默认 `:8872`），不是仅监听 carrier loopback。
- 这些边界不等于安全容器：Hostel 的可信/半可信代码模型不变。透明 MITM 和 Credential Vault 不在当前能力范围。

`GET /v1/status.components.network` 和 `/healthz.network` 返回支持等级、预期/生效等级、启用状态、backend、作用域、失败
原因和启动 probe（attempted、stage、duration_ms、error）。`enabled=false` 表示未采用 private 网络，可能是预期 shared、
策略关闭或能力缺席，不是 Hostel 健康检查失败。探测成功但未选用时仍保留 Supported 的 private。`/v1/isolated` 的 share_net 如实反映该状态，显式请求
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
