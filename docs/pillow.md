# Pillow：配置、执行与监督

Pillow 是 Bed 内的命名执行环境，在具体进程之前声明 env、配置引用、workdir 和 mounts。
命令、Session 和自动启动的主进程共用这个环境。它可以没有进程，也可以承载多个临时执行；
进程结束不会删除 Pillow。整体对象关系见 [kernel.md](kernel.md)，Bed 生命周期见 [bed.md](bed.md)。

## 声明与消费

BedSpec.pillows 是有序、不可变的声明，首项为默认 Pillow；名称在 Bed 内唯一。
创建 Bed 必须显式声明至少一个 Pillow；缺失、null 和空列表均拒绝。普通命令、Session 和文件 API 默认使用首项。
内部可信管理入口可以按名称选择 Pillow，数据面没有任意切换到管理 Pillow 的 header。
列表顺序只决定默认对象，不表达启动依赖。

Pillow 的 workdir 默认 /workspace，统一作为进程默认 cwd 与文件 API 相对路径基准。
每个 Pillow 显式声明 mounts 和环境配置，不继承其它 Pillow 的配置。
配置来源与覆盖规则见下文，卷及只读保证见 [volumes.md](volumes.md)。多个 Pillow 共享 Bed 根数据、用户身份、网络和资源。
挂载视图不同不等于同 Bed 内进程之间有完整安全隔离。

```yaml
volumes:
  - name: exchange
    managed:
      persistence: none
pillows:
  - name: workspace
    workdir: /workspace
    mounts:
      - volume: exchange
        path: /run/hictld
        read_only: true
  - name: hictld
    command: ["/usr/local/bin/hictld"]
    restart_policy: on-failure
    required: true
    mounts:
      - volume: exchange
        path: /run/hictld
```

示例中的程序参数和就绪配置由调用方按真实程序契约提供。Carrier 提供可执行程序和依赖，
Hostel 不内置 hictld 等业务程序目录，也不会因为镜像中存在程序就自动为每个 Bed 启动它。

## 配置来源

`Configuration` 描述 Pillow 的环境变量需求；Configuration Manager
负责读取宿主配置来源，在每次消费者启动时解析变量，并管理 Bed 的配置准备状态。Bed 不认识 Kubernetes
Secret 或 ConfigMap：部署方将它们或普通文件目录提供给 daemon，注册为命名来源。

daemon 通过 `--configuration-sources` 或 `HOSTEL_CONFIGURATION_SOURCES` 接收 JSON 对象，
将来源名映射到绝对目录，例如 `{"application":"/run/config/application"}`。
仅注册来源不会将内容注入所有 Bed，也不会把该目录挂载或复制进 BedFS。

每个 Pillow 使用以下字段选择所需配置：

- `env`：直接提供变量，仅放允许进入本地声明记录的非敏感值。
- `env_from`：按顺序导入命名目录，以文件名为变量名；后面的来源覆盖前面的同名变量。
- `env_value_from`：按变量名指定 `{source, key}`，选择或重命名单个文件值。
- `env_files`：按变量名引用 daemon 可读的绝对文件路径，供可信控制面使用。

显式变量覆盖目录导入；同一变量不能同时出现在多个显式字段。文件值去掉末尾一个换行，
拒绝 NUL 和超过读取上限的内容；保留变量名不能被任何来源覆盖。
命名来源允许目录内部的原子投影符号链接，但拒绝越出来源目录的访问。

### 解析时机与宿主观测

Configuration Manager 是 Bed 组件，不只是声明转换器：启动时以 daemon 的实际身份检查
来源目录是否可读，并在后续解析时更新观测。`components.configuration` 报告来源名、
最近观测时间、目录可读性及已准备的 Bed 配置数量，不报告宿主路径或配置值。
观测可读不保证所有键均存在、值均合法，也不代表 Bed 进程可以直接访问来源目录。

未被使用的来源缺失不会阻止 daemon 或其它 Bed 工作；实际消费者引用的来源不可用时，
该次准备或服务启动失败，不静默跳过。状态查询读取已有观测，不临时读取配置文件。

Bed 初始化检查所有 Pillow 引用的配置能否解析；启动每个消费者时重新读取所选 Pillow 的来源，
单次命令 env 可覆盖非保留变量。Session 保留创建时的进程环境，主进程重启重新解析，
已运行进程不自动刷新。Pillow 之间不隐式继承配置。

先停止消费者，再释放 Bed 配置准备状态。本地身份记录保存非敏感声明与来源引用；
从文件读取的值不会写回声明、状态或快照。主进程自动生成的鉴权值属于该次 Execution，
不加入同 Pillow 普通命令的基础环境。

### 配置访问边界

控制面决定哪些 Bed 可以声明哪些来源；来源注册不是租户授权机制。尤其是 `env_files`
能够读取 daemon 可访问的宿主文件，不能向不可信调用方直接开放。进程环境独立注入也不等于
进程或文件隔离；共享身份、共享宿主视图的实际限制仍由对应组件报告。

这里落实的是文件来源到进程环境变量的消费，不提供按 Bed 挂载配置文件的能力。
文件视图、只读挂载和访问边界属于 Filesystem 的职责，不能用环境变量注入代替这些保证。

环境按 Carrier 环境、Hostel 注入的 Bed 上下文、Pillow 配置、单次执行 env 的顺序组合。
过滤 Carrier 中的 HOSTEL_*、外部 BED_* 和受管 CDP endpoint，其余环境默认继承；部署方
负责其中敏感信息。Session export 只改变该 shell，不能改写声明或其他进程的环境。
声明在本地生命周期内固定，Executor 替换和保留目录的 daemon 恢复继续使用它。

## 可选主进程

command 和 args 直接属于 Pillow，拼接后作为 argv 执行，不隐式经过 shell。
未声明 command 时只准备环境；有 command 时环境就绪后自动启动一个主进程。
空 command、孤立 args 和没有 command 的监督或 HTTP 声明拒绝。
多个主进程使用多个 Pillow，通过相同 Volume 共享所需数据。

required 表示 Bed 持续依赖该主进程就绪，不表示启动依赖或一次性 init 任务。
主进程的 restart_policy 支持 never、on-failure、always，自动重启受退避和 max_restarts 约束。
startup_seconds 限制首次就绪等待，stop_seconds 限制 TERM 后等待，再由 KILL 清理进程组。
成功退出且不再重启报告 completed，失败或重试耗尽报告 failed。

http.ready_path 用于周期就绪检查；没有 HTTP 检查的主进程只以进程存活判定就绪。
运行中就绪失败保留进程，不消耗重启预算；必需主进程未就绪阻止新工作，不取消已有执行。
Pillow 环境准备状态与主进程阶段分别报告；没有主进程的 Pillow 仍有 prepared 状态。

主进程引用 Bed port_mapping；端口预留、监听核验、发布和释放由 Network 拥有。
HTTP 鉴权支持每 Execution 生成凭据或读取声明的环境配置；进程本身负责验证请求。
生成 token、运行地址、内部路径不进入 PillowSpec，连接发现绑定当前 Execution 与端口实例。
细节见 [network.md](network.md) 和 [protocol.md](protocol.md)。

## 执行与恢复

Bed Manager 准备所有 Pillow 的环境，通过 Bed 唯一 Executor 运行进程。
Pillow Manager 负责主进程期望、就绪和监督，Executor 负责派生、收尸和进程域回收。
主进程内部启动使用已准备的环境，避免调用必须等待 Bed Ready 的公开命令入口。
所有 Pillow 均无 command 时，Executor 按首次命令或 Session 请求创建。

每次运行生成 Execution ID，记录 Pillow name 和 Executor ID；Session 创建后绑定固定 Pillow。
主进程重启只替换该主 Execution，同 Pillow 的命令、Session 与卷数据继续保留。
Executor 丢失先清理旧域，再绑定新的各 Pillow 视图并恢复主进程；普通命令和 Session 不自动重放。
旧实例回调不能覆盖新实例状态，恢复过程不重做 Store Stage-in。

Bed 回收先关闭工作和代理准入、停止自动重启，再停止主进程、命令、Session 和 Transfer。
确认消费者不再写入后持久化，最后释放视图、端口和卷。清理失败保留资源责任，不提前报告完成。

## 管理入口

管理 API 使用 `/v1/beds/:bedId/pillows` 查询，按 Pillow name 查询详情。
`/:pillow/process/restart` 只重启主进程；没有主进程时拒绝该操作。
访问发现、日志和有界 hold 沿用 Bed 准入和当前实例绑定规则。
外部调用方只消费公开协议，敏感 access 响应禁止缓存；hold 保护一次请求或异步工作，
主进程常驻本身不永久占用 Bed activity。

Amenity 仍是 Carrier 设施，按 Bed 分配 Tenant，其进程不自动进入任意 Pillow。
Bed 的共享设施使用边界见 [amenity.md](amenity.md)。

| 接口 | 行为 |
|---|---|
| `GET /v1/beds/{bed}/pillows` | 查询所有 Pillow；不创建 Bed、不续租 |
| `GET /v1/beds/{bed}/pillows/{pillow}` | 环境准备、主进程阶段、Execution/Executor ID 与 PortMapping 引用 |
| `GET /v1/beds/{bed}/pillows/{pillow}/logs?cursor=0` | 当前主 Execution 的有界日志、下一游标及截断状态 |
| `POST /v1/beds/{bed}/pillows/{pillow}/process/restart` | 异步重启主进程，返回 202；不重建 Pillow |
| `POST /v1/beds/{bed}/pillows/{pillow}/access` | 使用 `{"hold_seconds":300}` 获取当前端口快照、可选 token 与限时 hold |
| `DELETE /v1/beds/{bed}/pillow-holds/{hold}` | 提前释放 hold，幂等 |

hold 为 1–7200 秒，覆盖直连操作及 HTTP 返回后仍在执行的异步任务；期间 eviction 等待，
到期自动结束 operation，daemon shutdown 仍可强制终止。提前释放不缩短已有 retained_until。
诊断与内部 readiness 探测不续租，主进程常驻不等于 Bed 有业务活动。

## 启动、监听与凭据

可执行绝对路径、相对路径和 PATH 查找均在降权后的 Pillow 视图内完成；workdir 决定 cwd，
Pillow 的 PATH 决定裸命令查找范围。Carrier 提供的解释器、动态库和程序目录须在该视图可见。
command / args / env 中的 `${PORT}` 和 `${LISTEN_ADDR}` 用于注入分配的监听地址，
不把其它配置当作 shell 表达式求值。程序必须前台运行，子进程不能通过 daemonize/setsid 脱离监督进程组。

主进程策略默认值：required=false、restart_policy=never、max_restarts=5（0 使用默认值，上限 100），
退避从一秒增长至三十秒；startup_seconds=30（1–120），stop_seconds=5（1–30）。
Executor 丢失也受恢复预算约束；正常成功退出在 never/on-failure 下停止，在 always 下重启。

HTTP 主进程引用 Bed 的 port_mapping，并声明 http.ready_path；authentication 可省略。
省略时不生成凭据，readiness 不附加认证头，access 不返回 token。配置认证时，scheme=bearer，
token_env 指定注入变量，token_source 支持：

- generated：每次主 Execution 生成新 token；不得与显式环境来源冲突。
- environment：从 Pillow 配置读取指定变量；每次启动读取，缺失或空值使启动失败。

敏感值使用 env_files/env_value_from 引用，避免明文进入持久化声明。readiness 和 access
使用主进程启动时的同一份 token，文件轮换不会改变已运行实例。程序负责校验 Bearer token，
Hostel 的 TCP 发布不代替程序认证。探活不跟随重定向，单请求两秒超时，最多 16 个并发探测。
独立环境变量不提供对同 UID 进程或 /proc 的保密保证。

监听归属检查区分当前进程组、其他进程占用、尚未监听和无法检查。明确冲突才触发换端口，
自动或偏好端口最多尝试三次，硬要求不换号；缺权限不能当作冲突。无法检查时可降级为
进程存活且 HTTP readiness 成功，并披露 listener 检查缺口。暂时失去 readiness 保留当前
Execution、凭据、端口及已有连接；拒绝新 access，不阻断已发现地址的直连流量。
端口分配与回收契约见 [network.md](network.md)。

规范化声明保存在 daemon 私有的本地身份记录中，不进入卷快照；
跨实例重建由调用方重新提交。
状态和日志不包含环境配置值或 token。

## 命令、Session 与进程承载

Command 每次运行独立进程，文件可在命令之间延续；Session 显式保留 shell 的 cwd/export。
主进程由 Pillow Manager 监督，普通命令和 Session 由请求驱动；三者使用同一环境与执行契约。
一个 resident Bed 同时至多有一个 Executor，归属其中的运行以 Execution ID 区分。

```text
hostel daemon
├─ Chromium 等共享设施进程
└─ Bed（领域身份，非 OS 进程）
   └─ Executor（当前进程承载域）
      └─ supervisor（Linux backend）
         ├─ 一次性命令进程（绑定 Pillow）
         ├─ 显式 session shell（固定 Pillow）
         └─ Pillow 主进程（可选）
```

Linux supervisor 负责派生、收尸与整域回收，可重连 IPC 和幂等 Start 避免重试重复执行。
Start 的大进程配置经匿名文件描述符传递，标准输入保持独立；配置超限或进程拒绝启动
只终结本次 Execution，不表示共享 Executor 丢失。
local backend 由 daemon 直接派生并管理进程组，不承诺清理已脱离进程组的后代。
两种 backend 都通过同一 Executor/Process 契约提供终态；容器内 tini 是进程收尸的额外兜底。

Execution 区分进程退出、信号终结和丢失，并独立记录 timeout/cancel/teardown 等终止原因。
Executor 丢失终结其所属 Execution，不能把传输 EOF 当成正常退出。协议和观测见
[observability.md](observability.md)，实现锚点在 `internal/bed/executor`、`internal/bed/executor/supervisor`
与 `internal/bed/manager/execution.go`。

### 可信执行入口

、session 和 Pillow 主进程每次启动进程时共用的可信初始化路径：
按 Bed Manager 绑定的执行方案进入环境、切换身份及权限、应用 env/cwd，最后 `exec`
用户程序。它使用 Hostel 自身二进制的内部入口，不是用户镜像的 ENTRYPOINT，
也不是 Bed 生命周期的 Prepare hook、常驻服务或 PID 1。Executor 只负责启动和监督；
资源分配、机制选择与降级仍由 Bed Manager 及领域组件完成。
Bed Manager 为新 Executor 绑定执行环境后才发布它：Filesystem 可从 Bed 模板派生
与该进程域匹配的各 Pillow 最终视图；command、Session 和主进程使用所属 Pillow 的视图，
Executor 退出后释放。卷数据与模板归 Bed，Executor 替换不改变它们。
准备失败的 Executor 保留清理责任，清理成功前不得复用或替换。

### Session 与输出

Session 的 env/cwd 准备与运行持有同一 run lock；cwd 支持变量及前导 ~，拒绝命令替换。
准备失败没有进程退出结果，shell 可继续使用；超时或取消关闭当前 shell，后续请求失败，
不隐式重建或重放。Session 固定绑定创建时的 Pillow、Executor 及文件视图。

后台输出保存在本地身份下的私有 executions 目录，不进入卷快照。Executor 替换保留历史，
Bed 身份终结时清理，daemon 重启不恢复执行历史。运行中日志不清理，终态日志按有界保留
策略清理；磁盘日志和内存片段缓存的保留独立。写入失败终止运行并报告 output_failure，
读取失败明确返回错误。输出协议见 [protocol.md](protocol.md)。

### Executor 进程命名空间

`--executor-pid-namespace=off|auto|required` / `HOSTEL_EXECUTOR_PID_NAMESPACE`
控制 Executor 的 PID namespace Tool，默认 `off`。`auto` 在启动时尝试独立进程域，
不支持或无法与文件视图组合时允许回退；`required` 必须实际采用，否则启动失败。
前提包括 Linux supervisor、PID/mount namespace 创建权限和 procfs 挂载权限。
这一策略与文件、网络隔离分别配置；选定后，live Bed 和替换 Executor 不重新降级。

一个 Executor 的所有进程共享 PID namespace；command、Session 和主进程进入所属 Pillow 的最终文件挂载视图。
Supervisor 是域内 PID 1，负责收尸与终止；Executor 丢失会结束该域。替换时保留 BedFS，
重新创建进程域和对应文件视图，旧资源清理成功前不发布新 Executor。
对 daemon 返回的 PID 仍属于 daemon 可见的命名空间，供 Pillow 主进程监听端口归属检查使用。

Component 的 `tools.pidns` 报告 policy、requirements、probe、selected 和 reason；
Bed 的 `executor.private_pid_namespace` 报告所属进程域是否实际隔离。启动组合回退
保留原始 Policy 和探测证据，不通过改写 Policy 来伪装成调用方关闭了隔离。

共享控制目录通过 Bed `volumes` 和各 Pillow 的 `mounts` 声明。需要真实挂载点的调用方必须选择已兑现 mount
视图的文件机制；PRoot/pathshim 路径投影不能作为内核挂载能力的证明。Hostel 不解释
目录中的上层控制协议。
