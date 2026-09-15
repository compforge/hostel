# Hostel 权限模型

`internal/host/privilege` 提供凭据切换与 ownership 操作；Bed Privilege Manager 决定用户、UID 租约与执行顺序。系统事实由 `host/facts` 提供。分层边界见 [核心架构](kernel.md#领域与-host-机制)。

## 理念与信任边界

Hostel 在同一 Carrier 内同时承担两类职责：daemon 管理所有 Bed 的文件、进程、网络和持久化资源，
Bed 内的 command、session 与 Service 执行调用方代码。daemon 保留完成管理
动作所需的权限；Bed 进程可使用共享或独立 `BedUser`，并在用户代码获得控制权前清空不再需要的
capability、设置 `no_new_privs`。

权限是兑现隔离机制的前提，不是隔离结果。capability、seccomp、AppArmor、内核功能和工具必须共同
允许真实操作；Hostel 会探测完整执行路径，并通过 diagnostics 披露事实。部署不应以
`privileged: true`、`hostPID` 或无边界的宿主权限替代具体要求。

`internal/bed/privilege` 是 Bed 操作系统身份的 owner：它负责 `BedUser`、目录 ownership、进程
credentials 和 UID 租约；`privilege.Manager` 统一拥有分配器与权限报告。文件隔离策略由 `internal/bed/filesystem/isolation` 选择，网络与资源权限分别由对应
组件使用；权限层不决定房型、网络策略或资源配额。

## Daemon 与 BedUser

官方镜像默认以 root 启动 daemon，使它能跨 Bed 准备目录、切换进程身份并回收不同 UID 的进程。
配置的建议 `BedUser` 拒绝 UID/GID 0。身份切换不是启动前提：建议身份缺少跨 UID 管理能力或探测失败时，可沿用 daemon
身份；继承 UID 0 时必须实际证明子进程的 active 与 bounding capability 集合均为空、`NoNewPrivs=1`。
因此 root + drop ALL 有可工作的弱基线路径，但不能保证任意部分 capability 组合都可用。
Hostel 不尝试获取缺失权限，也不把 UID 0 等同于具备某个 capability。

每个 resident Bed 在初始化时解析一个 `BedUser`，目录属主和 command/session 的最终身份共用同一个值：

- **shared / fixed**：所有 Bed 共用身份。`HOSTEL_BED_UID/GID`、`--bed-uid/gid` 与 Go 配置提供建议值；可用时优先采用，
  不可用时尝试经过完整权限与文件访问探测的 daemon 身份。未配置建议值时，root daemon 具备完整跨 UID 管理能力便优先使用 `1000:1000`。
  降级原因与实际 UID/GID 通过 privilege 状态和启动日志披露；非法建议值、继承身份验证失败或探测清理失败仍终止启动。
- **dedicated / per_bed**：room/suite 期望每个 Bed 独立的高位 UID/GID，不依赖采用的是 bwrap、Landlock 还是 UID/DAC。
  支持性由 Privilege 独立探测；Filesystem 的 UID/DAC 只有在已选 dedicated 身份时才可采用。相同 Bed ID 从稳定槽位开始，冲突时
  在预留范围内寻找空闲值，不能因散列碰撞让两个 Bed 共享身份。

per-Bed UID 是本地数据身份的一部分。Bed Manager 识别本地目录及 `.gc-*` 清理占位，
通过绑定到本地身份的 Recover / Prepare / Forget 驱动 Privilege Manager；权限组件不推测磁盘目录布局。
启动先根据目录 owner 恢复租约，资源与本地目录全部清理后才调用 Forget。luggage 和失败清理继续
保留 UID，具体重试与准入契约见 [kernel.md](kernel.md#组件参与生命周期)。
UID 范围须由部署侧预留，避免与宿主账号或 user namespace 映射重叠。

Bed 数据交接给新属主时跳过多硬链接的普通文件，防止改变 Bed 外共享 inode 的 owner；Linux 部署应
保持 `fs.protected_hardlinks=1`。BedFS 在 daemon 通过 file API 新建文件后继续把 owner 交还给同一
`BedUser`。

## 特权操作顺序

Bed 初始化先恢复或创建 BedFS，再解析并准备 `BedUser`，随后获取网络、文件边界和 Executor 所需的
资源；全部完成后才发布 Ready。command、session 和 Service 统一通过 `manager.Environment` 组装执行环境：

```text
daemon 准备 BedFS 与资源
  → 进入 Bed netns（启用时）
  → 切换到 BedUser，并清空 capability 集合、设置 no_new_privs
  → 建立文件边界与进程视图
  → 启动用户程序
```

这个顺序保证 netns 等需要管理权限的动作在最终降权前完成，同时让 bwrap、Landlock、PRoot 或
pathshim 及用户程序运行在 Bed 身份下。任何已选机制执行失败都应终止本次初始化或执行，不能在运行
途中静默放开边界。

回收时 daemon 先撤销数据面并停止 Bed 的进程，再清理设施、资源、网络和本地目录。跨 UID 终止进程
和修改目录需要的权限必须保留到清理完成；清理失败时资源 owner 与 UID 租约继续保留，供后续重试。

## 能力与部署条件

Linux 下所有 Bed command/session/Service 都经过 `setpriv`，包括 BedUser 与 daemon 身份相同的情况，
用于清空 inheritable / ambient capability 并设置 `no_new_privs`；实际具有 CAP_SETPCAP 时才要求清空 bounding set。
启动探测还核对 permitted/effective 集合，不能仅凭 wrapper 参数声称清理成功。
仅在身份不同时附加 UID/GID 切换和 supplementary groups 清理参数，因此非 root 自定义镜像也必须
提供 `setpriv`。

当 Bed 身份与 daemon 不同时，Hostel 还需要以下 Linux capability 完成目录交接、身份切换、
进程监督和最终 capability 清理：

| Capability | 用途 |
| --- | --- |
| `CAP_CHOWN` | 把 BedFS 交给最终 BedUser |
| `CAP_DAC_OVERRIDE` | daemon 跨 Bed 检查、更新和清理数据 |
| `CAP_FOWNER` | 管理非 daemon 属主的文件元数据 |
| `CAP_KILL` | 停止不同 UID 的 Bed 进程 |
| `CAP_SETGID` | 设置 Bed GID 与 supplementary groups |
| `CAP_SETUID` | 设置 Bed UID |
| `CAP_SETPCAP` | 管理最终进程的 capability 集合 |

`drop: ["ALL"]` 会移除包括 `CAP_KILL`、`CAP_SETPCAP` 在内的默认 capability。只有希望使用跨 UID 管理时，
才需按 `/v1/status.components.privilege.requirements.capabilities` 的实际结果配置；不是 Hostel 启动的统一前提。
daemon 与 BedUser 相同时，这组身份切换要求为空，子进程凭据验证仍不可省略。

隔离和进程视图在 Bed identity 基础上还有各自的运行条件。下表区分实际操作要求与容器中常见的
放行方式；最终以机制 smoke 和 Bed 完整组合探测为准，不能仅凭某个 capability 或 profile 判断可用。

| 能力 | 实际操作要求 | 容器中常见的放行方式 | 缺失后的行为 |
| --- | --- | --- | --- |
| shared 文件 / direct | Bed identity 前提满足，无额外文件机制要求 | 继承身份基线不要求身份切换权限 | 作为文件隔离最低档继续运行 |
| confined 文件 / Landlock | 内核提供 Landlock LSM，允许所需 syscall | seccomp/LSM 允许操作，无额外 capability | 继续尝试 UID/DAC，最终可降至 shared 文件 |
| confined 文件 / UID | 完整 Bed identity capability；允许 setgroups/setgid/setuid/chown，并预留 UID 段 | 配置身份切换 capability 和允许相关操作的 seccomp/LSM 策略 | 不选择 UID/DAC |
| private 文件 / bwrap | bwrap 可执行，允许创建 user namespace 并在其中执行 mount 操作 | AppArmor 阻断时可使用合适的 `Localhost` profile 或 `Unconfined`；seccomp 也须允许操作 | 继续选择可用的较低文件等级 |
| PRoot workspace view | PRoot 可执行，允许其跟踪 Bed 子进程，ptrace 与 PRoot smoke 成功 | 按实际阻断调整 seccomp/LSM；部分容器策略可通过声明 `CAP_SYS_PTRACE` 放行，但它不是通用必需条件 | 继续尝试 pathshim 或 Carrier view |
| per-Bed netns | named netns 创建/进入、veth、route、nft 可用；`ip`、`nft`、`setpriv` 可执行且 IPv4 forwarding 已开启 | 当前 backend 需要 `CAP_SYS_ADMIN`、`CAP_NET_ADMIN`，seccomp/LSM 也须允许相关操作 | 启动探测失败时使用共享 Carrier 网络 |

`CAP_NET_ADMIN` 单独不能完成当前 named netns backend；bwrap 通过 user namespace 完成
mount 准备，不因文件视图本身要求宿主级 `CAP_SYS_ADMIN`；suite 的 private 网络另有 named netns 的权限要求。各机制的隔离语义分别见
[isolation.md](isolation.md)、[filesystem.md](filesystem.md) 和 [network.md](network.md)。

Kubernetes Pod Security Standards 只约束准入，不代表节点实际能力。当前 root daemon 加 Bed identity
capability 的形态不满足 Restricted；这些 identity capability 可落在 Baseline 允许集内，但
`Unconfined` AppArmor、`CAP_SYS_PTRACE`、`CAP_SYS_ADMIN` 和 `CAP_NET_ADMIN` 分别会使对应的
bwrap、PRoot 或 netns 模板需要更窄的 namespace、RuntimeClass、ServiceAccount 或自定义 admission
豁免。若能维护满足 bwrap 需求的 `Localhost` AppArmor profile，可避免为 suite 使用 `Unconfined`。

Kubernetes 的权限分三层，不能互相替代：

1. sandbox-server ServiceAccount 的 RBAC 只允许创建和管理 Carrier Pod。
2. Carrier Pod 中 Hostel container 的 `securityContext` 提供 Linux capability、seccomp 和
   AppArmor 配置。
3. 节点内核与 container runtime 实际执行 user namespace、ptrace、Landlock 和 netns 操作。

Pod Security Admission 或其他 admission policy 可能拒绝第二层声明；RBAC 通过不代表运行时能力
可用。可直接应用的 securityContext、server-side dry-run 和排障步骤见
[Kubernetes examples](../deploy/k8s/README.md)。

## 探测、降级与失败语义

Hostel 启动时先读取 daemon UID/GID、有效 capability、seccomp、LSM 与内核事实，再运行各机制自己的
smoke。候选缺少前提或 smoke 失败时，可以选择同一维度内可用的较低能力，并如实披露；某个机制一旦
被选中，运行时失败必须返回错误，不能静默退回更弱路径。

实例在 HTTP 服务启动前还通过临时 Bed 运行真实 command、shell 与 Service 进程，验证 BedFS、网络、最终 UID/GID、
capability 清理和 `NoNewPrivs` 的完整组合。自动候选可在完整清理后有限回退；required、清理失败或候选耗尽会阻止服务启动。网络是可选维度：网络启动
探测失败不影响 Hostel 提供共享网络服务，但显式请求网络策略时会返回不可用。

非 Linux 平台不支持切换 Bed 身份；只有 BedUser 与 daemon 身份相同时才可执行。Hostel 不尝试在运行
时获取缺失的部署权限。

## Status

`GET /v1/status` 中与权限相关的事实分属三个 owner：

- `components.privilege`：daemon 身份、Bed user 策略、支持等级、选择结果、`setpriv` 解析结果、身份切换所需与缺失的 capability；
  `preconditions_satisfied` 只表示这些静态前提满足。
- `host` 与 `components.filesystem.probes`：capability/seccomp/LSM、namespace、内核功能、ptrace 及
  各文件机制的启动探测。
- `environment.probe_status`：真实 Bed command、shell 与 Service 进程的完整组合是否通过。

诊断读取只返回启动时缓存的事实，不重新探测主机，也不授予权限。判断某项能力是否实际可用时，应先
看所属组件的最终 verdict，再结合 environment 组合结果；不能只凭 capability 存在推导成功。
