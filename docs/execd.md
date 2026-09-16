# OpenSandbox Execd API

Hostel 在 API 层适配 OpenSandbox Execd，内部仍由 Bed Manager、Executor 和 BedFS
拥有执行与文件状态。原生 API 使用 Hostel 的执行协议。

`/execd/...` 从 `X-Hostel-Bed` 读取 Bed Name，缺省使用 default；
`/v1/beds/:bedId/execd/...` 从路径读取同一标识。两组路由共用 handler，入口只负责
绑定已有 Bed。路径优先于 header，不存在的 Bed 返回 404，不会隐式创建。

支持 ping、前台 command、按 execution id 取消 command、files/upload、files/download
和批量 directories 删除。命令支持 stdin、cwd、envs 和毫秒 timeout；UID/GID 切换、
background、下载范围参数以及尚未实现的接口会明确失败，不返回假成功。

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
