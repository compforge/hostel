# Hostel 核心架构

## 定位与目标

**Hostel 是面向 Agent 的 Pod 式工作负载运行时，通过 BedSpec 组织执行环境与共享资源，内建工作数据同步，并根据宿主能力分级兑现运行与隔离需求。**

Hostel 根据 BedSpec 为各 Pillow 准备执行环境，并在 Bed 的整个生命周期内提供基础设施、
进程监督、数据持久化与资源回收。准备执行现场与持续照料运行是同一生命周期的两个方面：

- 执行现场由 Bed 拥有根数据、身份、网络、资源和 Volumes，各 Pillow 组合自己的挂载视图、环境与工作目录。
- 运行照料由 Hostel 的领域组件持续承担：监督进程、维护共享设施、同步数据、观测状态，并在恢复或回收时完成资源交接。

组件按领域拥有状态与资源，跨准备、运行和回收阶段协作。例如 Store 在启动前恢复数据、运行中同步、
回收前最终同步；无需按阶段拆成两套组件。用户程序执行前的可信准备与最终权限切换，见
[权限与执行身份](isolation.md#权限与执行身份)。

Hostel 在一个 Carrier（一台机器或一个容器）内管理多个 Bed，复用工具链与重型设施。
调用方通过 BedSpec 声明工作负载，使用文件、命令、Session 和常驻服务；各领域根据宿主
实际能力选择实现。Docker/containerd 可以承载 Carrier，完整 namespace 和 cgroup 则是
可利用的能力，而非所有 Bed 的启动前提。

工作负载语义先于实现机制：明确要求必须兑现，否则失败；未声明的可选能力允许降级，
并报告实际结果。API 的任务可用性与隔离保证分别判断。例如缺少 cgroup 时仍可执行命令，
但不能声称已有资源硬限额；文件 API 可用也不代表进程拥有完整私有根视图。

当前面向可信或半可信代码。Hostel 负责实例内运行、资源管理与工作数据同步；上层负责
实例调度、跨实例路由、单写者归属与入口授权。各维度的信任边界见 [isolation.md](isolation.md)。

## 工作负载模型

Bed 声明必须包含非空 Pillows；首项为默认 Pillow。创建入口拥有声明，数据访问只等待已声明 Bed 就绪。

| 对象 | 身份与职责 |
|---|---|
| Bed | 路由、资源归属和生命周期单位；拥有 Pillows、Volumes、根数据、网络与资源需求 |
| Pillow | Bed 内的命名执行环境；持有 env、配置引用、workdir、mounts 和可选主进程声明 |
| Volume | Bed 内命名的数据来源；由 Hostel 管理或引用外部目录，可声明自动持久化 |
| Execution | 一次运行；属于 Pillow，由当前 Executor 承载，具有输出和结构化终态 |
| Amenity | Carrier 共享设施；按 Bed 分配 Tenant 状态，如 Chromium BrowserContext、MCP 连接 |

```text
Hostel / Carrier
├── Bed
│   ├── Volumes、Rootfs、网络、资源
│   └── Pillows
│       ├── env、workdir、mounts
│       └── Command / Session / 可选主进程
└── Amenities → 按 Bed 绑定 Tenant
```

Bed 的 Name 由调用方给定，用于寻址和定位远端快照；本地 ID 由 Hostel 生成，用于运行资源
归属。Pillow 名和 Volume 名在 Bed 内唯一。第一项 Pillow 是默认执行环境，顺序不表达启动依赖。
多个 Pillow 可把同一卷挂到不同路径；卷身份独立于挂载路径。Pillow 共享 Bed 的用户身份、
网络和资源，不因环境或挂载不同而获得独立容器的安全边界。

Executor 是 Bed 当前可替换的进程承载域；BedFS 是根数据、卷和各 Pillow 路径视图的实现。
替换 Executor 保留 Bed 资源与数据，重新绑定执行视图。Store 实现卷的同步、恢复和显式传输。
这些组件服务于工作负载模型，各自拥有实现资源与状态。

## 所有权与组件契约

`internal/bed` 定义共享模型与生命周期契约。Bed Manager 拥有 Spec、Lifecycle 和跨领域
协调；Filesystem、Privilege、Network、Resource、Configuration、Executor、Pillow、Store
各自持有资源句柄、任务及状态。Amenity Manager 直属 daemon，通过 Bed 生命周期适配释放 Tenant。

一个本地生命周期只有一个共享 Bed，初始化重试和 Executor 替换只替换所属资源。
状态按领域授权写入、按值提供快照；查询不能通过共享 map、slice 或指针获得第二个写入口。
Bed Manager 的请求句柄协调 operation/session 和清理进度，不另造一份领域 Spec/Status。

领域组件有三个驱动面：daemon 级 Start/Close、单 Bed 生命周期 hooks、可选后台 Run 循环。
Start 同步完成准备，Run 自己管理周期、合并与退避，Close 取消并等待退出。Bed Manager
显式安排跨领域依赖顺序；资源按具体 allocation 回收，失败保留清理责任。具体阶段与重试见
[bed.md](bed.md#组件参与生命周期)。

### 领域与 Host 机制

依赖方向为：入口 → 领域模型与流程 → 通用宿主机制。`internal/host` 提供 namespace、
挂载、进程、身份、cgroup 和事实采集能力，不依赖 Bed/Amenity，不解释房型或工作区。
领域赋予资源身份、策略与生命周期，决定是否允许降级，并发布实际状态。

启动采集 Host facts，领域再用真实操作验证可用性；单个工具通过不等于组合可运行。
状态读取只投影已观测事实。API handler 负责寻址、准入与协议转换，不能拼接内部路径或
操作资源句柄；原生和 Execd adapter 直接复用领域能力，彼此不依赖。代码地图见
[AGENTS.md](../AGENTS.md)，协议边界见 [protocol.md](protocol.md)。

## 跨领域主流程

### 实例启动

合并配置 → 构造领域组件 → 恢复本地身份与清理责任 → 探测并验证执行组合 → 开放 API。
启动失败逆序关闭已准备组件；正常运行由领域维护同步、采样和回收循环。

配置入口按 `显式 Options > CLI > HOSTEL_* env > 默认值` 生成确定的 Config，区分未指定
与显式零值。各组件只读自己的配置；参数按部署需求暴露，完整配置不进入状态响应。
工具策略与组合验证见 [isolation.md](isolation.md#能力选择与工具策略)，Pillow 配置见
[pillow.md](pillow.md#配置来源)。

### Bed 初始化与执行

```text
声明 Bed → 预占容量、合并同名初始化
  → 恢复并验证卷数据 → 准备身份、网络、资源与文件视图
  → 解析 Pillow 配置、启动必需主进程 → Ready
  → operation / session 准入 → 领域操作 → 输出与终态
```

准备完成前不开放数据面，远端恢复错误不能当作空 Bed。执行使用所属 Pillow 的环境，
在可信准备完成并降权后运行用户程序。已接纳的工作受生命周期保护，状态查询不保活。
详细准入和状态语义见 [bed.md](bed.md)，进程模型见 [pillow.md](pillow.md)。

### 同步、回收与恢复

```text
本地读写 → Store 合并同步触发 → 发布持久卷快照
Bed 回收 → 关闭准入 → 停止写入 → 最终同步
  ├─ 失败：保留本地数据与清理责任
  └─ 成功：释放资源 → 删除本地数据 → 释放本地身份
再次创建 → 恢复持久卷、重建临时数据与运行环境
```

Bed 内持久卷共同发布一个 generation；环境配置、进程状态和共享设施运行态不进入卷快照。
同步不是实时共享文件系统，上层须保证同名 Bed 的单写者归属。Purge 显式销毁本地及远端数据；
daemon 正常关闭停止工作和资源，保留本地数据供恢复。数据契约见 [volumes.md](volumes.md)。

实例停止先撤销准入、取消并等待后台及初始化任务，再清理 Bed 运行资源、逆序关闭组件和设施。
上层根据实例事实决定释放 Carrier；Hostel 不因空闲自行退出。完整回收、保留期限与失败重试由
[bed.md](bed.md) 定义。

## 领域文档

| 文档 | 回答的问题 |
|---|---|
| [bed.md](bed.md) | Bed 如何声明、准备、服务、保活、回收与恢复？ |
| [pillow.md](pillow.md) | 环境配置如何供命令、Session 和主进程使用，进程如何监督和替换？ |
| [volumes.md](volumes.md) | 数据属于谁、如何挂载访问、怎样持久化与传输？ |
| [isolation.md](isolation.md) | 信任、权限和能力选择如何兑现并披露实际保证？ |
| [network.md](network.md) | 网络、端口发现与出站策略如何管理？ |
| [resource.md](resource.md) | 容量、资源记账、准入与限制分别保证什么？ |
| [amenity.md](amenity.md) | 共享设施如何按 Bed 切分状态、凭据和生命周期？ |
| [protocol.md](protocol.md) | 如何寻址和表达请求、结果、错误及协议差异？ |
| [observability.md](observability.md) | 当前状态和执行过程怎样投影到诊断接口、日志与 Trace？ |
| [backlog.md](backlog.md) | 哪些能力尚未交付？ |

使用入口见项目 [README](../README.md)，验证入口见 [单机 E2E](../tests/e2e/README.md)。
