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

下面是 main + hictld 的完整声明示例。可执行路径、参数、健康路径和环境变量由调用方按
实际程序提供；Hostel 不内置 hictld 等业务程序知识。此例要求 Carrier 提供该二进制，
`/healthz/ready` 无需业务认证，业务 token 由 Carrier 环境或 Pillow 配置来源提供。

```yaml
volumes:
  - name: workspace
    managed:
      persistence: none
  - name: shared-data
    managed:
      persistence: none
pillows:
  - name: main
    workdir: /workspace
    env:
      HIBOT_SESSION_ID: session-demo
    mounts:
      - volume: workspace
        path: /workspace
      - volume: shared-data
        path: /mnt
  - name: hictld
    workdir: /workspace
    command: ["/opt/hibot/bin/hictld"]
    args: ["--listen", "${LISTEN_ADDR}"]
    env:
      HIBOT_SESSION_ID: session-demo
      HICTLD_WORKSPACE_PATH: /mnt/session
      HIBOT_SKILL_DOWNLOAD_TRUSTED_NETWORKS: '[{"cidr":"10.5.125.33/32","ports":["8060"]}]'
    mounts:
      - volume: workspace
        path: /workspace
      - volume: shared-data
        path: /mnt
    ports:
      - name: hictld
        protocol: TCP
        dynamic: true
        publish: true
    readinessProbe:
      httpGet:
        path: /healthz/ready
        port: hictld
      timeoutSeconds: 2
      periodSeconds: 2
      failureThreshold: 3
    required: true
    restart_policy: always
```

示例选择动态监听端口，使多个 Bed 在共享 Carrier 网络上也可并存。需要固定监听端口时将
`dynamic: true` 换成 `containerPort: 18089`，运行时必须使用该端口，冲突直接失败。
多个 Bed 都使用同一固定端口要求实际隔离的网络空间；不能把共享网络伪装成独立 Pod 网络。

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

Configuration Manager 是 Bed 子系统，不只是声明转换器：启动时以 daemon 的实际身份检查
来源目录是否可读，并在后续解析时更新观测。`subsystems.configuration` 报告来源名、
最近观测时间、目录可读性及已准备的 Bed 配置数量，不报告宿主路径或配置值。
观测可读不保证所有键均存在、值均合法，也不代表 Bed 进程可以直接访问来源目录。

未被使用的来源缺失不会阻止 daemon 或其它 Bed 工作；实际消费者引用的来源不可用时，
该次准备或服务启动失败，不静默跳过。状态查询读取已有观测，不临时读取配置文件。

Bed 初始化检查所有 Pillow 引用的配置能否解析；启动每个消费者时重新读取所选 Pillow 的来源，
单次命令 env 可覆盖非保留变量。Session 保留创建时的进程环境，主进程重启重新解析，
已运行进程不自动刷新。Pillow 之间不隐式继承配置。

先停止消费者，再释放 Bed 配置准备状态。本地身份记录保存非敏感声明与来源引用；
从文件读取的值不会写回声明、状态或快照。业务凭据与其它配置采用同一解析规则，Hostel 不赋予特定变量名额外的优先级。

### 配置访问边界

控制面决定哪些 Bed 可以声明哪些来源；来源注册不是租户授权机制。尤其是 `env_files`
能够读取 daemon 可访问的宿主文件，不能向不可信调用方直接开放。进程环境独立注入也不等于
进程或文件隔离；共享身份、共享宿主视图的实际限制仍由对应子系统报告。

这里落实的是文件来源到进程环境变量的消费，不提供按 Bed 挂载配置文件的能力。
文件视图、只读挂载和访问边界属于 Filesystem 的职责，不能用环境变量注入代替这些保证。

普通业务变量的覆盖顺序从高到低为：

1. 单次执行显式 env。
2. 当前 Pillow 的 `env` / `env_value_from` / `env_files`（同名不能重复声明）。
3. 当前 Pillow 的 `env_from`（后一个来源覆盖前一个）。
4. Carrier 的可继承环境。

Hostel 注入的 Bed 上下文属于保留变量，不能通过以上业务配置覆盖。
过滤 Carrier 中的 HOSTEL_*、外部 BED_* 和受管 CDP endpoint，其余环境默认继承；部署方
负责其中敏感信息。Session export 只改变该 shell，不能改写声明或其他进程的环境。
声明在本地生命周期内固定，Executor 替换和保留目录的 daemon 恢复继续使用它。

Carrier 环境在 daemon 启动时获取；Kubernetes Pod 的 Secret/ConfigMap envFrom 更新不自动
改变已运行 daemon 的环境，需要按部署流程重建容器。Pillow 命名文件来源则在消费者启动时
重新读取；两条路径的刷新时机不同。

### Kubernetes 部署中的两种配置供给

整台 Bedbox 共享的业务默认值可以直接注入 Hostel 容器：

```yaml
containers:
  - name: hostel
    envFrom:
      - configMapRef:
          name: sandbox-defaults
      - secretRef:
          name: hictld-sync-secret
```

这些普通变量会被该 Carrier 的所有 Bed/Pillow 继承。只有某个 Pillow 需要的配置则继续使用
已支持的命名文件来源：部署方把 Secret/ConfigMap 投影成 daemon 可读目录，注册
`HOSTEL_CONFIGURATION_SOURCES='{"hictld-config":"/run/config/hictld","hictld-secret":"/run/secrets/hictld"}'`，
然后在 hictld Pillow 声明：

```yaml
env_from: [hictld-config, hictld-secret]
env:
  HIBOT_SKILL_DOWNLOAD_TRUSTED_NETWORKS: '[{"cidr":"10.0.0.0/8","ports":["443"]}]'
```

显式白名单覆盖文件来源中的同名键。main 不继承 hictld 的导入；仅注册来源也不会让所有
Pillow 获得其内容。Hostel 读取已注册的本地文件，不自行访问 Kubernetes Secret/ConfigMap API。


## 可选主进程

command 和 args 直接属于 Pillow，拼接后作为 argv 执行，不隐式经过 shell。
未声明 command 时只准备环境；有 command 时环境就绪后自动启动一个主进程。
空 command、孤立 args 和没有 command 的监督、ports 或探针声明拒绝。
多个主进程使用多个 Pillow，通过相同 Volume 共享所需数据。

required 表示 Bed 持续依赖该主进程就绪，不表示启动依赖或一次性 init 任务。
主进程的 restart_policy 支持 never、on-failure、always，自动重启受退避和 max_restarts 约束。
stop_seconds 限制 TERM 后等待，再由 KILL 清理进程组。成功退出且不再重启报告 completed，
失败或重试耗尽报告 failed。环境准备状态与主进程阶段分别报告，没有主进程仍有 prepared 状态。

### 端口声明

`ports` 是该 Pillow 的命名 TCP 监听列表；名称在整个 Bed 内唯一。同一固定监听端口不能
由两个 Pillow 重复声明。每项支持：

- `name`：探针和状态引用的名字，不能为纯数字。
- `protocol`：默认 `TCP`，当前仅支持 TCP。
- `containerPort`：1–65535 的固定监听端口，无法使用时失败，不自动换号。
- `dynamic`：Hostel 扩展，显式申请动态监听端口，与非零 containerPort 互斥。
- `publish`：Hostel 扩展，默认 false；是否通过 Carrier 发布外部访问端口。

固定端口或 dynamic 必须二选一。声明不会替程序调用 bind，程序必须实际使用指定地址。
多端口命令使用 `${PORT_<name>}` / `${LISTEN_ADDR_<name>}`；只有一个端口时也可用
`${PORT}` / `${LISTEN_ADDR}`。仅 command/args 做地址展开，所有 env 值保持原样，
不会因字符串含占位符而改写业务 token 等配置。分配结果只在运行状态中返回，重启后重新发现。
分配、发布和回收归 Network，细节见 [network.md](network.md)。

### 通用就绪探针

`readinessProbe` 使用 Kubernetes 风格的 camelCase 字段，必须选择一种 action：

- `httpGet: {path: /ready, port: hictld}`：无认证的 HTTP GET；200–399 为成功，不跟随重定向。
- `tcpSocket: {port: hictld}`：能建立 TCP 连接即成功，不证明业务请求能完成。

port 可以是当前 Pillow 的命名端口，也可以是已声明的固定 containerPort 数字。
动态端口必须按名称引用。当前没有 exec、HTTPS、gRPC、livenessProbe 或 startupProbe；
未知字段拒绝，不把 Kubernetes 完整 Probe API 当作已实现能力。

`initialDelaySeconds` 默认 0；`timeoutSeconds` 默认 1；`periodSeconds` 默认 10；
`successThreshold` 默认 1；`failureThreshold` 默认 3。每次检查完成后等待 periodSeconds，
同一 Pillow 不并发探测；timeoutSeconds 包含等待全局探针并发槽与监听归属检查。
计数按连续结果计算，反向结果清零。初始延迟最多 86400 秒，超时/间隔最多 3600 秒，
连续阈值最多 1000；负数拒绝。

初始就绪前以及运行期间，探针失败都只改变就绪状态，不杀进程、不消耗重启预算。
必需主进程首次就绪前 Bed 保持 initializing；就绪丢失阻止新工作，不取消已有执行或连接。
没有探针时只以进程存活判定就绪，不宣称应用已能响应。进程退出、显式停止和确认的
端口归属冲突仍按执行生命周期处理；调用方可通过 Bed 生命周期取消初始化。

健康检查不验证或推导业务认证。Hostel 不读取某个特定 token 变量、不发送业务 Authorization，
也不从 access 返回凭据。业务进程与调用方各自持有配置，认证由业务进程执行。

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
| `GET /v1/beds/{bed}/pillows/{pillow}` | 环境准备、主进程阶段、Execution/Executor ID 与 ports 名称列表 |
| `GET /v1/beds/{bed}/pillows/{pillow}/logs?cursor=0` | 当前主 Execution 的有界日志、下一游标及截断状态 |
| `POST /v1/beds/{bed}/pillows/{pillow}/process/restart` | 异步重启主进程，返回 202；不重建 Pillow |
| `POST /v1/beds/{bed}/pillows/{pillow}/access` | 使用 `{"hold_seconds":300}` 获取当前 ports 快照与限时 hold |
| `DELETE /v1/beds/{bed}/pillow-holds/{hold}` | 提前释放 hold，幂等 |

hold 为 1–7200 秒，覆盖直连操作及 HTTP 返回后仍在执行的异步任务；期间 eviction 等待，
到期自动结束 operation，daemon shutdown 仍可强制终止。提前释放不缩短已有 retained_until。
诊断与内部 readiness 探测不续租，主进程常驻不等于 Bed 有业务活动。

## 启动与监听归属

可执行绝对路径、相对路径和 PATH 查找均在降权后的 Pillow 视图内完成；workdir 决定 cwd，
Pillow 的 PATH 决定裸命令查找范围。Carrier 提供的解释器、动态库和程序目录须在该视图可见。
程序必须前台运行，子进程不能通过 daemonize/setsid 脱离监督进程组。

主进程策略默认值：required=false、restart_policy=never、max_restarts=5（0 使用默认值，上限 100），
退避从一秒增长至三十秒；stop_seconds=5（1–30）。Executor 丢失也受恢复预算约束；
正常成功退出在 never/on-failure 下停止，在 always 下重启。

探针前的监听归属检查区分当前进程组、其他进程占用、尚未监听和无法检查。
确认动态端口冲突时最多尝试三次分配/启动，固定 containerPort 不换号；缺权限不能当作冲突。
无法检查时使用通用探针判断可用性，并披露 listener 检查缺口。暂时失去 readiness 保留当前
Execution、端口及已有连接；拒绝新 access，不阻断已发现地址的直连流量。
全实例最多 16 个并发 HTTP/TCP 网络探测。端口分配与回收契约见 [network.md](network.md)。

规范化声明保存在 daemon 私有的本地身份记录中，不进入卷快照；跨实例重建由调用方重新提交。
运行状态与运行时日志不记录环境配置值；业务进程自己的 stdout/stderr 仍按普通执行日志收集。
独立环境变量不提供对同 UID 进程或 /proc 的保密保证。

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
资源分配、机制选择与降级仍由 Bed Manager 及领域子系统完成。
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
Executor 自身探测不支持时允许回退；`required` 必须实际采用，否则启动失败。
前提包括 Linux supervisor、PID/mount namespace 创建权限和 procfs 挂载权限。
这一策略与文件、网络隔离分别配置；选定后，live Bed 和替换 Executor 不重新降级。

一个 Executor 的所有进程共享 PID namespace；command、Session 和主进程进入所属 Pillow 的最终文件挂载视图。
Supervisor 是域内 PID 1，负责收尸与终止；Executor 丢失会结束该域。替换时保留 BedFS，
重新创建进程域和对应文件视图，旧资源清理成功前不发布新 Executor。
对 daemon 返回的 PID 仍属于 daemon 可见的命名空间，供 Pillow 主进程监听端口归属检查使用。

Subsystem 的 `tools.pidns` 报告 policy、requirements、probe、selected 和 reason；
Bed 的 `executor.private_pid_namespace` 报告所属进程域是否实际隔离。Executor 内部回退
保留原始 Policy 和探测证据，不通过改写 Policy 来伪装成调用方关闭了隔离。

共享控制目录通过 Bed `volumes` 和各 Pillow 的 `mounts` 声明。需要真实挂载点的调用方必须选择已兑现 mount
视图的文件机制；PRoot/pathshim 路径投影不能作为内核挂载能力的证明。Hostel 不解释
目录中的上层控制协议。
