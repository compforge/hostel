# 对外协议与结果契约

Hostel 的原生 API 与 OpenSandbox Execd adapter 共享 Bed、Pillow 和 Volume 模型。
协议负责寻址、校验以及结果与错误的表达；资源状态和生命周期由领域拥有。

`github.com/qiankunli/hostel/protocol` 为服务端和 Go 调用方提供 Bed 管理数据定义，覆盖
身份、生命周期、Pillow 声明与状态、端口及组件状态。它只依赖标准库，不包含 client 或运行时资源。

## 模块与工具链边界

`protocol/` 是独立 Go module，自己的 `go.mod` 声明最低 Go 1.25，不依赖 Hostel 主模块。
调用方只引入 `github.com/qiankunli/hostel/protocol`，不会继承 Hostel daemon 的 Go 版本或
Chromium 等运行时依赖。协议模块可按仓库提交或 `protocol/vX.Y.Z` tag 独立选择版本。

Hostel 主模块通过本地 replace 复用同一份协议源码，继续使用自己的工具链和运行时依赖。
构建镜像在下载主模块依赖前须复制 `protocol/go.mod`；源码复制后，协议与服务端一起编译。
根目录的 lint/test 入口也校验协议模块；可在 `protocol/` 独立运行 make lint/test，
并用 Go 1.25 验证调用方兼容性。

## 模型归属

公开字段和 JSON tag 由 `protocol` 定义。纯数据类型由内部 owner 直接复用；内部确有
额外字段时组合公开结构，例如 FilesystemStatus 组合公开 preparation 状态并保留内部路径，
Pillow 主进程状态组合公开字段并保留内部执行结果。不为内外两侧另造 DTO/View 名称或重复字段表。

Bed 的 `id` 与 `name` 沿用 Hostel 内部语义：调用方通过 Name 标记和寻址 Bed，
Hostel 自行生成 ID 唯一标识本地生命周期，避免调用方标识与内部资源归属冲突。
同一个 Name 在销毁后重建时会对应不同 ID；保留本地数据的 daemon 重启恢复沿用 ID。
上游自行维护业务标识与 Name/ID 的映射，Hostel 不解释上游业务身份。

内部 `bed` 组合 `protocol.Bed`，保留锁、Spec 和分域状态；`Bed` alias 仅供仓内组件共享。
组合的公开部分保存身份，API 快照从各 owner 读取其它字段，不增加第二套可变状态写权。

## 寻址与身份

创建响应、详情和 inventory 的 name 是调用方路由标识，id 是 Hostel 本地生命周期 ID。
请求中的 id 参数、Bed 路径及 X-Hostel-Bed 都接收 Name；用响应 name 做业务关联，用 id
判断本地生命周期是否更替。只读 inventory 不生成身份，尚未恢复本地身份或仅有远端数据
的记录可没有 id。Name、header 和 query 只用于寻址，入口认证及 Bed 授权由接入方负责。

## 声明与状态投影

`status.lifecycle` 表达 Bed 生命周期，`status.pillows` 表达每个 Pillow 的准备与主进程事实，
`status.port_mappings` 表达端口发现。生命周期 readiness 使用 `readiness.status`，
Pillow 使用 `prepared` 区分环境准备，`phase`、`ready` 和 execution 字段表达主进程状态。

创建声明使用有序 `pillows`；环境、workdir 和 mounts 归属 Pillow，卷来源归属 Bed。
首项是默认 Pillow；创建 Bed 必须提供非空列表，缺失、null 或空列表均返回 400。主进程使用直接字段
`command`、`args`、`restart_policy`，管理路径为 `/v1/beds/:bedId/pillows`。
Volume 使用互斥来源 `managed` 或 `host_path`；`managed.persistence` 为 `none` 或 `sync`。
Pillow 的挂载路径与 Volume 的持久身份独立，完整数据契约见 [volumes.md](volumes.md)。

Pillow 主进程的 outcome 保留既有诊断 JSON；底层 executor 类型不成为公开依赖，私有 Detail
继续清空。Amenity 的 status 是设施拥有的可扩展 JSON 值；公开契约不暴露内部 sealed Tenant
接口。路径、资源句柄、锁和发布策略留在内部实现。

服务端先取得领域快照再构建响应，不通过协议对象改变资源状态。`protocol` 的字段分组也不
表达调用方的即时/延迟落库要求；这些规则由调用方维护。

## 验证

`protocol` 的外部包测试使用既有 wire shape 验证解析和往返；API 测试使用公开类型读取实际
Bed 和 Pillow 主进程响应，检查 readiness 与执行身份。在 `protocol/` 中可用 `go list -deps ./...`
检查依赖，本包的传递依赖应全部为标准库。

## OpenSandbox Execd

Hostel 在 API 层适配 OpenSandbox Execd，内部仍由 Bed Manager、Executor 和 BedFS
拥有执行与文件状态。原生 API 使用 Hostel 的执行协议。

`/execd/...` 从 `X-Hostel-Bed` 读取 Bed Name，缺省使用 default；
`/v1/beds/:bedId/execd/...` 从路径读取同一标识。两组路由共用 handler，入口只负责
绑定已有 Bed。路径优先于 header，不存在的 Bed 返回 404，不会隐式创建。

`internal/api/apiv1/{handler,view}` 拥有原生协议；
`internal/api/execd/{handler,view}` 拥有 OpenSandbox 适配。两者直接调用 Bed Manager、
Execution、Session 和 BedFS，不互相依赖。`internal/api` 仅组装 server、中间件与路由；
Bed 层不含协议族事件、错误码或权限编码。

支持 ping、前台/后台 command、状态、日志、取消、session 创建/执行/删除，以及文件
info/search、上传下载、移动、删除、权限、内容替换和目录创建/列表/删除。
命令支持 stdin、cwd、envs 和毫秒 timeout；UID/GID 覆盖仍明确拒绝。code/context 路由
返回 501 NOT_SUPPORTED，本版本不提供代码 kernel，也不将 code 转交 shell。

权限在 Execd JSON 中以 644/755 表达，在 BedFS 中保持 Unix mode bits。owner/group
只能确认符合 Bed 身份的现有文件属主，不能静默忽略或任意切换属主。mode=0 沿用上游
“不修改 mode”的行为。元数据包含 owner/group、mode、modified_at 和 created_at；Linux
无可移植 birth time 时使用 ctime，其他平台按本机可用时间提供回退。

下载支持 HTTP Range（206/416）和 1-based offset/limit 行读取，二者互斥。字节读取
复用 BedFS 的受限文件描述符并流式返回，mutation 不使用宿主读取回退。目录 depth=0
返回空列表；列表保持字典序，symlink 仅作为条目返回，不递归跟随。

后台启动返回 init + execution_complete，其中 complete 只确认成功启动；进程的最终
结果通过 status 查询。日志为 stdout/stderr 合并文本，返回 EXECD-COMMANDS-TAIL-CURSOR。
本地对照 OpenSandbox 57ea79511：规范文字写行游标，但 runtime/command_status.go 实际
按字节偏移读取；本 adapter 对齐实际 execd 的字节游标，超出末尾时夹到真实 EOF。

执行身份、Session、输出保留与 Executor 替换见 [pillow.md](pillow.md)。
准备失败投影为 preparation_failed；没有启动命令时不伪造进程退出结果。

命令流使用 OpenSandbox 的 init、stdout、stderr、ping，以及成功时的
execution_complete 或失败时的 error。CommandExecError.evalue 保留退出码；timeout
映射为 124，signal 映射为 128 + signal，其余终止原因保留在 traceback。
原生 execution_start/execution_end 的结构不变。

Bed 创建者可在默认 Pillow 的不可变 env 中设置 `EXECD_ACCESS_TOKEN`。配置后，两组入口都要求
`X-EXECD-ACCESS-TOKEN` 或 Bearer 凭据；未配置时沿用独立 Hostel 的可信调用方边界。
Bed Name 仅用于寻址，不能代替凭据。原生管理 API 的网络访问仍须由部署方约束。

Execd 请求在本实例完成 Bed 绑定、鉴权和本地操作。上层控制面负责业务准入、租期与
跨实例路由；需要逐请求策略时，由控制面代理 Execd 请求，Hostel 不反向调用控制面。
命令、Session 和文件操作复用 Bed 自身的生命周期与运行时约束。

凭据配置面向可信 Bed 创建者；Pillow env 本身不是秘密存储。共享实例的隔离保证
取决于所选房型和宿主能力，见 isolation.md。

## 错误契约

Hostel 的通用错误契约位于 `internal/error.go`。领域层决定失败事实，API 层将事实转换为所属协议；领域错误不携带 HTTP 状态码、OpenSandbox 错误码或响应 JSON。

### 分类与原因

`hostel.ErrorKind` 表达稳定类别：ErrInvalidArgument、ErrNotFound、ErrConflict、ErrUnavailable、ErrPreparationFailed。使用 `hostel.WrapError(kind, op, cause)` 添加操作上下文，通过 `errors.Is` 判断类别、通过 `errors.As` 读取结构。底层 cause 保留，文件不存在等已有标准库错误仍能通过 `errors.Is` 识别；取消和截止时间复用 `context.Canceled` / `context.DeadlineExceeded`。

Op 使用固定操作名，例如 `session directory`，不拼入 command、env、凭据、调用方内容或宿主路径。`Error()` 保留内部错误链；API 使用 `Message()` 或自身的固定消息，不将底层 OS/IPC 错误直接暴露。错误分类禁止依赖字符串比较。

领域 sentinel 保留调用方需要的精确含义。类别表示失败种类，不替 API 决定状态码；例如查询不存在的文件和执行前 cwd 不存在属于不同操作，协议映射可不同。

### 执行结果

程序执行后返回非零 exit code 是正常的进程终态，不能当成参数校验错误。Execution 将终止原因与进程事实分开保存。

Session 的 env/cwd 准备失败使用 ErrPreparationFailed，保留根因；命令没有执行时 `ExecutionResult.Process` 为 nil，错误通过 `Err` 传递，终态原因为 `preparation_failed`。原生 API 返回 `process: null` 和安全错误说明；Execd 根据自身事件规范投影为 error/125。Shell 仍可复用，不能因此宣称 Executor 丢失。实际 transport/进程丢失继续使用 `lost / executor_lost`。

同步拒绝在产生执行流前返回；已经生成 Execution 的异步失败必须进入其唯一终态。底层函数只包装和返回错误，拥有生命周期的入口负责一次带 bed/execution 上下文的终态记录，避免逐层重复日志。

## 文件与传输入口

文件操作复用所选 Pillow 的 BedFS 路径：`/files/*`、`/directories` 用 X-Hostel-Bed 寻址，
Transfer 在 `/v1/beds/{bed}/transfers` 下管理。普通上传下载通过 HTTP 交换字节；Transfer
提交源和目标，由 Hostel 在后台搬运。数据、幂等与取消保证见 [volumes.md](volumes.md#显式文件传输)。

| 方法 | 行为 |
|---|---|
| POST transfers | 创建或重放任务，运行中 202，已有终态 200 |
| GET transfers/{id} | 查询进度或终态，未知／过期 404 |
| DELETE transfers/{id} | 请求取消，仍运行 202，已结束 200 |

```json
{
  "id": "export-001",
  "sync": "copy",
  "source": {"type": "bed", "path": "/workspace/project"},
  "destination": {"type": "s3", "key": "exports/project/"},
  "overwrite": false
}
```

必须一端为 bed，另一端为 s3；下载交换两端。sync 省略为 copy，也支持 restic；自动同步
的 auto/cas/pack/tar/noop 不适用于 Transfer。Restic 下载的 S3 source 还需提供完整 ref。
相对 Bed 路径从默认 Pillow Workdir 解析，不使用宿主文件读取回退。

状态包含操作及实例身份、state、files、bytes 和起止时间；终态为 succeeded、failed、
canceled，超时属于 failed。错误经过脱敏，不返回宿主路径、凭据或原始 SDK 错误正文。
POST 的 instance_id 和 DELETE 的同名 query 可绑定目标实例，不符返回 409；响应头
X-Hostel-Transfer-Instance 提供当前实例身份，包括 404。file_transfers capability 仅表示
已配置 bucket，不证明远端可达或授权成功；实际支持的策略由 transfer_syncs 报告。
