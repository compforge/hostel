# OpenSandbox Execd API

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

Execution 在 Bed 目录的私有 executions 子目录保存后台输出，不进入 BedFS、默认
Store 快照或 64 KiB 片段缓存。运行中日志不清理；终态记录和文件至少保留 24 小时，
每小时清理到期记录。Executor 替换保留历史；Bed evict/purge/Forget 结束该本地身份后
清理，重用 Bed Name 不得读到旧历史。daemon 重启不恢复执行历史，不承诺跨 carrier 日志。
写入失败会停止执行并报告 output_failure，读取失败不会伪装为空日志。

Session 复用持久 Shell。每次运行的环境准备、cwd 展开与命令执行在同一 run lock 内
完成；cwd 支持 $NAME、${NAME} 和前导 ~，拒绝命令替换。准入回调的环境值作为受信任
session 环境更新保留。超时/取消关闭当前 Shell，后续请求返回 session 不可用，不隐式
创建新的 session。原生执行协议与 URL 保持不变。

命令流使用 OpenSandbox 的 init、stdout、stderr、ping，以及成功时的
execution_complete 或失败时的 error。CommandExecError.evalue 保留退出码；timeout
映射为 124，signal 映射为 128 + signal，其余终止原因保留在 traceback。
原生 execution_start/execution_end 的结构不变。

Bed 创建者可在不可变 base env 中设置 `EXECD_ACCESS_TOKEN`。配置后，两组入口都要求
`X-EXECD-ACCESS-TOKEN` 或 Bearer 凭据；未配置时沿用独立 Hostel 的可信调用方边界。
Bed Name 仅用于寻址，不能代替凭据。原生管理 API 的网络访问仍须由部署方约束。

可选 `EXECD_CONTROL_URL` 启用执行准入回调：在命令/文件操作前向
`<URL>/execd/v1/sandboxes/<Bed Name>/prepare` POST `{command?: ...}`，携带本 Bed 的
Bearer 凭据。返回 `{exit_code, stderr?, envs?}`，非零拒绝执行，回调不可用也拒绝。
回调负责上游策略与续期，文件字节和命令输出始终由调用方直连 Hostel。
配置来自已创建 Bed，命令请求中的 envs 和身份 header 不能改变回调目标或凭据。

凭据和回调配置面向可信 Bed 创建者；Bed base env 本身不是秘密存储。共享实例的
隔离保证取决于所选房型和宿主能力，见 isolation.md。
