# 启动配置与工具选择

## 概念

Hostel 的 daemon 启动配置按组件组合。Filesystem、Network、Resource、Privilege、Executor、Store、Configuration
分别拥有自己的 Config；总配置负责把它们组装起来。Config 不仅承载 Tool 开关，也包含普通运行参数，
例如 Store 的 S3 endpoint、bucket、凭据和同步策略。

Bed Service 的完整声明、Pod IP 发布地址及 daemon 统一 TCP 端口池参数见
[bed-service.md](bed-service.md#四部署配置与-api)。Carrier 包含服务程序不会自动为每个 Bed 启用服务。
普通执行与 Service 的环境变量、命名文件来源及 `--configuration-sources` 见
[bed-configuration.md](bed-configuration.md)。

输入 Options 使用指针区分未指定与显式零值。启动入口按下列优先级生成确定的 Config：

```text
显式 Options > CLI 参数 > 对应 HOSTEL_* env > 默认值
```

显式 Auto、false、0 和空字符串均能覆盖 env。只有已有或有部署需求的参数才提供 env/CLI 映射，
内部配置完整不意味着全部对外开放。组件只读取解析后的 Config，状态接口也不输出包含凭据的完整配置。
环境 PATH 等操作系统执行上下文仍由真实运行环境提供，不伪装成组件配置。

## Component 与 Tool

- Component 拥有领域行为、生命周期、配置和状态。
- 组件拥有自己的能力类型与支持集合，结合 Config 选择机制，并在类型化 Status 中报告实际结果。
- 房型由文件隔离保证决定，其他组件的能力分别报告。
- Tool 是组件为兑现 Bed 能力而选择、组合的工具，既包括外部程序，也包括内核机制和身份技术，例如 Filesystem 的 Bwrap、Landlock、UID、PRoot、Pathshim，
  Network 的 NetNS，以及 Resource 的 cgroup accounting。
- Requirements 是使用该 Tool 的环境前提，包含 Linux Capabilities、外部程序和系统条件。
  CAP_NET_ADMIN、CAP_SYS_ADMIN 保留 Linux Capability 的含义。

Tool 使用统一的 Policy：

| Policy | 语义 |
|---|---|
| auto | 允许探测和选择；可用不代表一定被选择，不可用按组件原有契约降级 |
| off | 排除该工具，不执行其运行探测，不创建其运行资源 |
| required | 必须可用且实际采用，不允许其他工具替代 |

`--isolation` / `HOSTEL_ISOLATION` 和内部 `BedOptions.RoomType` 设置实例的文件房型目标和默认增强偏好。
这是实例级配置，Filesystem 的内部预期等级由房型推导。dorm 请求 shared 文件、shared 身份、shared 网络；
room 请求 confined 文件，偏好 dedicated 身份、shared 网络；suite/auto 请求 private 文件，偏好 dedicated 身份、private 网络。
房型是否达标只取决于文件保证；身份和网络偏好未兑现时由各组件解释原因。
Tool Policy 控制采用哪个实现。例如 room、UID=required 要求使用 UID/DAC 文件机制。互斥功能同时 required，
或房型与 required 实现冲突，在探测前拒绝配置。Required process path helper 会排除互斥的 Bwrap 挂载视图。

基础 BedFS 路径归属和必要的执行降权没有通用关闭开关。关闭可选隔离不绕过这些契约。
Requirements 由实现声明，caller 不给环境前提设置 Policy；实际准备顺序和依赖校验由所属组件负责。
共享 `internal/bed/tool` 包只提供策略和观测类型，不执行通用依赖调度，也不新增工具箱 Manager。
各领域 Manager 根据 Bed 的需求和宿主条件，从工具箱中选择并组合工具；工具更多不代表每次全部启用，也不自动提供更高的隔离保证。

## 启动、失败与观测

启动入口合并配置并校验冲突，各组件按策略探测、选择和准备，再以临时 Bed 验证 command/session/Service 的组合。
组合失败时先保留文件保证，依次尝试可选进程、网络和身份组合；耗尽后才更换文件候选，并恢复原始增强偏好重新探测。
整体选择、单次组合执行与清理都有独立的有界预算；候选耗尽或选择预算耗尽则失败。
Required 功能启动时无法满足就启动失败；清理失败也终止回退，保留资源 owner 和错误，不继续尝试下一组。
启动后具体 Bed 准备失败则该 Bed 不进入 Ready。已经采用的隔离实现执行失败，不能静默降级或放开边界。
组件准备与失败清理继续走同一生命周期。

实例状态按组件报告 Tool 的最终 Policy、Requirements、探测状态、是否选中及原因，原有执行探测
证据继续保留，JSON 字段为 `tools`。off 报告 disabled_by_config 和 not_probed；auto 工具可以探测成功却因优先级未被选中。
Requirements 是声明，不能仅凭权限位或工具存在就声称可用，执行探测仍是最终证据。
Filesystem 的 ceiling 表达已探测候选的最高可用档位；禁用候选后不能用它推断未探测的物理环境上限。
房型变低不删除已探测出的支持等级；Tool off 仍不执行探测，所以 Supported 是已知支持集合，不是未经探测的能力推断。
状态读取不触发探测或分配。

## 测试入口

E2E 通过内部 Options 在同一台机器上控制功能组合，仍启动真实 daemon、调用公开 API。
`make e2e` 构建带 e2e 标签的独立二进制；只有该构建读取 fixture 提供的 HOSTEL_E2E_CONFIG JSON 文件。
正常 build/image 没有这条输入路径，测试配置不是公开部署 API。

配置限制验证选择和降级；真实容器权限、系统调用拒绝、只读挂载验证实际探测和失败处理，两者不能互相替代。
运行与覆盖边界见 [E2E 说明](../tests/e2e/README.md)。

## Executor 进程命名空间

`--executor-pid-namespace=off|auto|required` / `HOSTEL_EXECUTOR_PID_NAMESPACE`
控制 Executor 的 PID namespace Tool，默认 `off`。`auto` 在启动时尝试独立进程域，
不支持或无法与文件视图组合时允许回退；`required` 必须实际采用，否则启动失败。
前提包括 Linux supervisor、PID/mount namespace 创建权限和 procfs 挂载权限。
这一策略与文件、网络隔离分别配置；选定后，live Bed 和替换 Executor 不重新降级。

一个 Executor 的 command、session 和 Service 共享 PID namespace 和最终文件挂载视图。
Supervisor 是域内 PID 1，负责收尸与终止；Executor 丢失会结束该域。替换时保留 BedFS，
重新创建进程域和对应文件视图，旧资源清理成功前不发布新 Executor。
对 daemon 返回的 PID 仍属于 daemon 可见的命名空间，供 Service 监听端口归属检查使用。

Component 的 `tools.pidns` 报告 policy、requirements、probe、selected 和 reason；
Bed 的 `executor.private_pid_namespace` 报告所属进程域是否实际隔离。启动组合回退
保留原始 Policy 和探测证据，不通过改写 Policy 来伪装成调用方关闭了隔离。

共享控制目录仍使用通用 `PathMapping`。需要真实挂载点的调用方必须选择已兑现 mount
视图的文件机制；PRoot/pathshim 路径投影不能作为内核挂载能力的证明。Hostel 不解释
目录中的上层控制协议或迁移资格标记。

### 扩展能力与降级

新 namespace 或可选组件归入实际拥有资源的领域。该领域声明 Tool 的策略、依赖与探测，
提供有限且单向推进的组合回退，并负责资源准备、回收和实际状态。内核版本、权限和程序存在性
可用于前置排除，是否可用以真实执行探测为准，Bed Manager 不维护按内核版本分叉的规则。

进程 namespace 的选择与冻结由 Executor 拥有；新增进程工具接入它的组合候选入口和工具状态，
文件组合入口无需识别新 namespace 名称。跨领域依赖由 Bed Manager 协调，在同一文件保证内
先尝试可选增强组合，之后才更换文件机制。Required 成员始终保留，清理失败停止后续尝试。
选定组合用于所有命令、session、Service 和替换 Executor；运行时失败明确返回错误。
