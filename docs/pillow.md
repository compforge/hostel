# Pillow：执行环境与主进程

Pillow 是 Bed 内的命名执行环境，在具体进程之前声明 env、配置引用、workdir 和 mounts。
命令、Session 和自动启动的主进程共用这个环境。它可以没有进程，也可以承载多个临时执行；
进程结束不会删除 Pillow。整体模型和默认规则见 [kernel.md](kernel.md)。

## 声明与消费

BedSpec.pillows 是有序、不可变的声明，首项为默认 Pillow；名称在 Bed 内唯一。
省略整个字段时创建 main，显式空列表拒绝。普通命令、Session 和文件 API 默认使用首项。
内部可信管理入口可以按名称选择 Pillow，数据面没有任意切换到管理 Pillow 的 header。
列表顺序只决定默认对象，不表达启动依赖。

Pillow 的 workdir 默认 /workspace，统一作为进程默认 cwd 与文件 API 相对路径基准。
每个 Pillow 显式声明 mounts 和环境配置，不继承其它 Pillow 的配置。
配置来源与覆盖规则见 [configuration-sources.md](configuration-sources.md)，卷及只读保证见
[filesystem.md](filesystem.md)。多个 Pillow 共享 Bed 根数据、用户身份、网络和资源。
挂载视图不同不等于同 Bed 内进程之间有完整安全隔离。

```yaml
volumes:
  - name: exchange
    empty_dir: {}
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

规范化声明及摘要保存在 daemon 私有的本地身份记录中；客户端不应填写 spec_digest。
读取旧记录时校验旧摘要再转换，新记录只保存 Pillow 模型。声明不进入工作区快照，跨实例
重建由调用方重新提交；状态和日志不包含环境配置值或 token。
