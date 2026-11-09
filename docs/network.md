# 网络管理

`internal/host/network` 提供通用网络 allocation、DNS 与规则执行；Bed Network Manager 负责 Bed 绑定、可选启用策略与状态发布。分层边界见 [核心架构](kernel.md#领域与-host-机制)。

## 理念与边界

网络隔离沿 Bed 的独立执行空间目标按环境能力尽量兑现，统一模型与组合约束见
[isolation.md](isolation.md)。

`network.Manager` 是实例级子系统，管理 resident Bed 的网络资源。网络与文件隔离档正交；
Bed 的每次命令和常驻 shell 使用相同 netns，Executor 替换不改变该网络身份。
当前覆盖 `bed_processes`：共享 Chromium/MCP 等 amenity 的出站仍走 carrier 网络。

Network 保留 shared/private 两级原生能力，分别映射 dorm/suite；不为了对应三种房型增加虚构的中间 backend。
Network 尽力选择 private，实例探测失败可选择 shared，并报告网络空间的缺口；出站策略另行选择 netns 或 UID 执行机制。Hostel 不按房型拦截 Bed。探测成功后单个 Bed 的分配、策略安装失败仍使初始化失败，不静默撤销已选边界。

## 能力失败归因

`cause` 归属于失败本身。探针和组合验证保留类型化错误，Subsystem 按从强到弱的顺序提供允许的候选实现；
选择结果携带相关失败，Config 不保存诊断字符串。成功回退为可执行的 shared 网络时，
通过状态报告缺口；需要拒绝创建或执行能力时，以原始错误链保留归因。
状态中的 cause 是失败证据的展示，不参与重新分类；未知错误不会因为发生在 Bed 内就归为 bed。


`/v1/status` 的 `subsystems.network` 与 `/v1/beds/capabilities` 的 `network` 报告实例能力。
失败时的 `cause` 表示归因，`probe.stage` 表示检测阶段，`reason`/`probe.error` 保存运维详情；
`scope` 仍表示网络执行范围，不是错误归因。

例如 carrier netns 的 `ip_forward=0` 得到 `cause=carrier, probe.stage=forwarding`。
这会令当前独立网络路径不可用。Hostel 可继续准备 shared Bed；若已采用独立 UID 且 UID+nft 探测通过，仍可执行出站策略，否则标为 unenforced，保留所请求的规则和 cause；
caller 决定是否使用该 Bed。Hostel 不修改 carrier sysctl。
配置显式禁用独立网络却请求其策略时归 request；地址耗尽归 resource；机制验证未兑现归 bed。
namespace 命令或连通性失败本身不能证明是 carrier 还是 Bed 实现的问题，证据不足则为 unknown。
原始 probe 错误留在全局状态/日志，创建 API 保留固定安全消息。

## Pillow 端口与 Network allocation

调用方只在 Pillow `ports` 声明监听需求。Network 直接使用这份声明，为每个当前 Execution
持有精确的 PortMapping 句柄；Bed Spec 不保存另一份端口配置。声明与示例见
[Pillow 端口](pillow.md#端口声明)。

```yaml
pillows:
  - name: web
    command: ["server", "--listen", "${LISTEN_ADDR}"]
    ports:
      - name: http
        protocol: TCP
        dynamic: true
        publish: true
    readinessProbe:
      tcpSocket:
        port: http
```

`containerPort` 是固定监听端口，不是偏好；`dynamic: true` 才允许运行时选取端口。
固定端口冲突直接失败，不静默改号。共享网络中的多个 Bed 应使用动态端口或不同固定端口。
Host 端口由统一端口池管理，不接受静态 hostPort，也不写入持久化声明。

| 实际网络 | Bed 内监听 | publish=true 的 Carrier 发布 |
|---|---|---|
| private netns | containerPort 固定，或 dynamic 动态分配 | 另分配 Host 端口，TCP 转发到 Bed IP |
| shared | containerPort 固定，或 dynamic 从 Host 端口池分配 | 使用进程的同一个端口，无额外转发 |

`publish=false` 不创建转发且不返回 host_port；共享网络注入 loopback 监听地址。
私有网络的 listener 仍需供 Carrier 探针访问。publish 是监听地址与发现契约，不是防火墙：
程序忽略注入地址、自行 bind，或其他进程访问已知地址，不受该选项拦截。

### 发现、归属与回收

Bed 详情直接返回 `status.pillows` 和 `status.port_mappings`；`status.subsystems.network`
继续描述隔离能力与网络状态。每项映射报告 `name`、`protocol`、`pillow`、`execution_id`、
`network`、`state`、`bed_port`、`host_port` 和降级 `reason`。`host_port` 仅在发布时存在。
端口事实只由 PortMapping 持有；Pillow 主进程状态通过 `ports` 名称列表引用它。
地址和应用 URL 是派生值，不作为独立状态或 API 字段，避免同一绑定出现多个信息源。

Pillow access 返回 `ports` 数组，每项是当前 PortMapping 的完整快照，包含 execution_id
与实际端口。所有项在同一 Pillow 主进程锁内读取，不返回业务 token，不要求声明 HTTP。
它是 Network 状态的查询投影，不是第二份可变状态，也不保证进程随后不会重启。

调用方按以下规则推导地址；HTTP 服务再加 `http://`：

| 访问位置 | 地址来源 |
|---|---|
| Bed 内 | `127.0.0.1` + 映射的实际 `bed_port` |
| Carrier 外 | 调用方已知的 Carrier IP/域名 + 映射的 `host_port` |

Carrier host 来自调用方的部署或连接信息，例如上层调度服务持有的 Carrier Pod IP；
Hostel 不配置或推测这个地址。Host 指承载 Hostel 的网络域，Pod 部署时通常就是
Carrier Pod。外部访问需要该 host 和端口可达；不能从经过代理的控制面 URL 假定数据端口可达。
未发布时没有 `host_port`，调用方不得据此构造外部地址。

例如 containerPort=18089、publish=true：私有 netns 下 Bed 内访问
`127.0.0.1:18089`，外部使用分配的 `<CarrierPodIP>:host_port`；共享网络要求
Carrier 的 18089 可用，bed_port 与 host_port 都为 18089。dynamic=true 时调用方读取实际端口。
私有 netns 的 loopback 属于 Bed，不能用自己的 `127.0.0.1:host_port` 访问 Host 入口。

端口属于当前 execution，重启后必须重新发现，不能将端口当成稳定身份。

`internal/bed/network.PortMappings` 复用 Host PortManager/TCPForwarder，拥有 reserve、publish、
withdraw、release；Pillow 主进程仅负责进程、通用 readiness 和重启，并绑定具体映射句柄。
准备顺序是网络就绪 → 预留 Bed 端口 → 启动进程 → Pillow 主进程就绪 → 发布 Host 入口。
没有声明 readinessProbe 时，发布以进程存活为准，不宣称应用已能服务请求。

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
文件视图与用户程序启动顺序由 [权限模型](isolation.md#特权操作顺序) 统一定义。
Network 自行根据真实探测决定 private 或 shared；required 和清理失败会阻止启动。
实例在 HTTP 启动前还会实测准备后的 command/session/Pillow 主进程；失败只报告并清理，不触发跨子系统重选。

网络分配提供 Bed resolver 文件，Bed Manager 在组装 mount 视图前交给 Filesystem，
只读挂到 `/etc/resolv.conf`。这样进入已准备的 mount namespace 不会丢失 `ip netns exec`
临时挂入的 resolver。该文件属于网络 allocation，不是用户 VolumeMount；显式映射与其
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
  相应 mount/seccomp/LSM 操作；完整权限矩阵见 [isolation.md](isolation.md)。单独
  `NET_ADMIN` 不等于可用；当前 backend 不实现 userns 辅助创建路径。
- 需要 carrier 已开启 IPv4 forwarding；Hostel 不修改全局转发 sysctl。官方镜像包含所需
  工具，自定义镜像缺工具会在诊断中显示具体缺项。
- 从 `198.18.0.0/15` 选择与现有非默认路由不重叠的 `/30`，每 Bed 一对 veth。nft 规则
  只作用于分配的网络，拒绝跨 Bed 转发和 IPv6 转发；现有 CNI/防火墙限制仍然生效。
- IPv4 NAT 使用 `ip` family，过滤使用 `inet` family，并在同一 nft transaction 中创建和回收。
  这样在 Linux 4.19 上无需依赖 Linux 5.2 才提供的 inet NAT，同时保留 IPv6 阻断；
  内核模块、权限与工具是否可用仍由真实启动探测决定。
- DNS 由有界的 UDP/TCP 转发器使用 carrier resolver，兼容 carrier 内的 loopback DNS。
- Bed 内的 CDP 地址使用 gateway 访问共享 Hostel API；使用该能力时 API 需监听可从
  veth 访问的地址（默认 `:8872`），不是仅监听 carrier loopback。
- 这些边界不等于安全容器：Hostel 的可信/半可信代码模型不变。透明 MITM 和 Credential Vault 不在当前能力范围。

`GET /v1/status.subsystems.network` 和 `/healthz.network` 返回 expected/effective/supported 原生等级、room、backend、scope、reason/cause 和启动 probe。
没有网络 enabled 开关。netns 是否采用从 effective=private 或工具 selected 判断；读取状态不触发探测。
Bed 的 `status.subsystems.network` 报告准备后实际 room/level、gateway、requested_policy、已执行 policy、policy_status 与缺口。
`/v1/isolated` 的 share_net 依据实际网络等级；显式参数与实际能力冲突仍报错。

共享 Chromium 的 BrowserContext 不是 OS 网络边界。待办是在创建 Context 时设置各 Bed
的 `proxyServer`，代理按 Bed 的网络策略拨号，并覆盖 bypass、QUIC/WebRTC 等路径。
该能力完成前浏览器不宣称受 Bed 网络隔离约束；待办统一记录于 `backlog.md`。

组合与回收验证见 [单机 E2E](../tests/e2e/README.md) 和 [isolation.md](isolation.md)。

## Bed 网络策略

NetworkManager 同时拥有 Bed netns 和有效策略。控制面可以在 `POST /v1/beds` 的
`networkPolicy` 字段指定初始策略，规则安装成功后才发布 Bed 就绪；重复创建相同 Bed
不会重置运行期策略，初始参数冲突则拒绝。shared 网络下优先用实际分配的独立 UID 执行策略；该机制不可用时仍可接纳声明，并明确报告请求规则未执行；caller 根据实际状态决定是否使用。
`/v1/beds/capabilities` 的 `network_policy` 表示 API 支持，不代表当前环境可执行全部规则。

每个现存 Bed 提供 `/v1/beds/{id}/network/policy`：GET 查询、POST/PUT 替换、PATCH 合并
规则数组、DELETE 删除 target 字符串数组。`/network/healthz` 查询同一 Bed 的有效状态。
这些控制面接口按路径中的 Bed 定位，不接受 header 覆盖；操作引用防止执行期间被回收。
缺少 Bed 返回 404，非法规则返回 400，nft 提交失败返回 500。查询不会创建 Bed。
没有可用策略执行机制的 shared Bed 保存和更新 desired policy，返回 status=degraded、mode=unenforced、enforcementMode=none；
requested_policy 是请求，policy=allow 仅表示 Hostel 没有安装限制，不保证外部 CNI/防火墙放行。Bed status 的已执行 policy 保持 null。
非法更新保留之前声明；运行中已经采用的执行机制失败不能转为这个降级路径。

策略包含 `defaultAction: allow|deny` 和 `egress: [{action,target}]`；target 接受 IP、CIDR、
域名和 `*.example.com` 通配域名。省略 defaultAction 时，空规则允许全部，有规则则默认拒绝。
IP/CIDR 拒绝优先于允许；域名拒绝由 Bed DNS 执行，默认拒绝策略下，仅在 nft 成功安装解析结果
后才返回 DNS 响应。放行地址使用 DNS TTL（最多 300 秒）到期，受每 Bed 4096 个地址上限约束。
Bed 进程只能向本 Bed DNS gateway 的 53 端口查询；IP 规则控制实际连接，域名规则不解释
HTTPS、DoH 或已经由调用方掌握的 IP，不能作为 HTTP Host/SNI 校验或 MITM 的替代。

策略更新使用原子 nft transaction，失败保留旧策略；更新清除旧的 DNS 放行集，旧版本下
尚未返回的 DNS 响应不能重新开放地址。已缓存 DNS 的客户端在收紧规则后可能需要重新解析。
private 网络的规则在 Bed 自己的网络 namespace 内执行；shared 网络按实际 socket UID 匹配。
两者都不修改 Carrier 的默认出站策略；private 网络现有的跨 Bed 隔离规则仍然成立。当前网络 backend 仅提供 IPv4 外部连通，IPv6 转发仍被阻断。

响应的 `scope=bed_processes` 明确限定命令和 shell；共享 Chromium、Hostel 代办的文件下载
等 carrier 进程不在其作用域。策略随 resident Bed 网络存在，evict 后销毁，不随 workspace
Store 保存；控制面重建 Bed 时需重传初始策略。透明凭据注入与 browser proxy 仍是独立能力。


### 共享网络下的 UID 策略

BedSpec 继续使用 `networkPolicy.defaultAction` 与 `networkPolicy.egress`，不指定底层机制。
Network 将网络空间与策略执行分别报告：网络 `level=shared`、房型 dorm 时，仍可有
`policy_backend=uid` 和 `policy_status=enforced`。策略接口返回 `status=ok`、
`enforcementMode=dns+nft`；`mode` 随规则为 allow_all、deny_all 或 enforcing。
实例的 `policy_backend`、`policy_probe` 和 `tools.uid_nft` 报告实际选择与探测结果。
网络原有 reason/cause/probe 继续解释独立 netns 的缺口，不代表 UID 策略未执行。

前提是 Privilege **实际采用** `per_bed` 独立 UID，用户进程以该身份创建 socket，
并已清除 capabilities、设置 no_new_privs；所有 Pillow、命令和 session 共用该 Bed 身份。
仅 supported 包含 dedicated、实际仍配置固定 UID/GID，不满足前提。嵌入方的
`Bed.Network.UIDNFT` 使用 auto/off/required，默认 auto；`NetNS=off` 只关闭 namespace
机制，关闭全部网络机制需同时指定 `UIDNFT=off`。required 未被选用时启动失败。
当前 CLI 不额外暴露这些工具配置；`HOSTEL_BED_EXTRA_GIDS` 不改变独立身份选择，也不参与 nft 的 UID 匹配。
镜像或部署若默认注入这两个变量，启用 per-Bed UID 前须真正移除它们；设为空字符串
仍被视为显式配置。升级 Hostel 二进制本身不会覆盖现有固定身份选择。

此 backend 在 Carrier OUTPUT 路径按 `meta skuid` 分流，每个 allocation 独占 nft 表、
DNS listener 与 DNS 地址集合。UDP/TCP 53 按 UID DNAT 到本 Bed 的 DNS listener，
其他 Bed 不能直接访问该 listener。域名解析沿用现有默认动作、deny 优先、TTL 和容量限制。
不依赖独立 netns、`ip_forward` 或 cgroup；需要当前 netns 的 `CAP_NET_ADMIN`、nft 的
UID 过滤和 IPv4 OUTPUT NAT 能力。启动验证完整 nft 规则并执行空表创建/删除，实际
DNS listener 和 Bed 规则在 Prepare 中创建；这些步骤失败会使该次 Bed 初始化失败。

共享 loopback 不再隐式全放行，访问本地服务同样受 egress 规则约束；允许连接的回复流量
仍可返回。规则逐包执行，更新会撤销既有连接的出站权限，并原子清空旧 DNS 放行集合。
IPv6 继续阻断，不通过共享 IPv6 路径绕过 IPv4 策略。该机制提供出站过滤，不提供独立
端口空间、Ingress 隔离或共享浏览器/代理的代办流量归属；允许的本地代理也能代表调用方发起请求。

Network 在进程启动前取得身份并安装规则，Bed Manager 在停止进程后释放规则和 DNS；
失败的准备或清理保留 allocation，UID 只能在资源与本地身份清理完成后复用。
规则所有权使用 allocation 唯一表名，旧句柄的重复释放不影响同 UID 的新 allocation。
验证入口见 [UID 策略 E2E](../tests/e2e/README.md#共享-netns-的-uid-出站策略)。

### Allocation 归属与崩溃恢复

Bed Manager 将 Network 的运行记录目录绑定到 `<beds-root>/.network`。Network
负责持久记录和回收自己创建的 UID nft 表、独立 netns、veth 与 resolver 配置；
Privilege 仍负责 UID 租约。该目录不属于 BedFS，不进入 Store 快照。

创建内核资源前，先原子写入 allocation 记录，包含内部 Bed ID、唯一资源名、UID
或 veth 名，以及 Carrier netns 与系统 boot ID。同目录的进程生命周期文件锁阻止
两个 daemon 同时接管；不同目录只按各自记录清理，不按 `hu_*` / `hn*` 前缀全局删除。
文件原子替换覆盖 daemon 进程崩溃，不提供断电落盘保证。

Network 在全局 Start 中、机制探测和 Bed 准入之前 reconcile 旧 allocation，
即使对应 Bed 目录已不存在或 caller 不再访问它也会处理。UID backend 先检查该 UID
是否还有活进程；netns backend 检查 namespace 内的进程。仍有活进程、记录损坏或
清理失败时，保留记录并阻止启动，不在运行中的旧 workload 下移除策略。正常 Executor
进程承载的 parent-death 清理先终止旧执行；若仍有存活后代，须先完成进程回收再启动。

同一 Carrier 的旧资源幂等清理成功后才删除记录，再按新的声明准备 Bed。
Carrier netns 或 boot ID 已变化时，不触碰当前 namespace 的同名资源，仅丢弃旧记录。
正常 Release 同样在资源回收成功后删除记录；失败保留归属供重试。Bed Manager 只有
完成全部运行资源和本地数据清理后才 Forget 并释放 UID，避免 UID 复用继承旧规则。

无记录的旧版本遗留资源无法可靠判断实例归属，升级不会按名称前缀猜测删除；这类
遗留需通过原实例的正常清理或替换 Carrier 处理。恢复 E2E 见
[TestBedIdentityCrashRecovery / TestPrivateNetworkCrashRecovery](../tests/e2e/README.md#verify-uid-egress-across-process-lifecycles)。
