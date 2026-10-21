# 公开 Bed 数据契约

`github.com/qiankunli/hostel/protocol` 是服务端和 Go 调用方共用的 Bed 管理数据定义。
本包目前覆盖 Bed 摘要、详情、生命周期、Pillow 声明与状态、端口映射及 Bed 级组件状态；不代表所有
Hostel API 都已迁入。本包只依赖标准库，不包含 client、资源操作、运行时对象或上游发布策略。

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

## HTTP 身份迁移

从 v0.1.39 开始，创建响应、详情和 inventory 中 `id` 表示 Hostel 本地生命周期 ID，
新增 `name` 表示原先响应 `id` 中的调用方标识。caller 必须改用 `name` 做路由和业务关联，
通过 `id` 判断本地生命周期是否更替，不应继续把响应 `id` 直接拼入请求路径。
现有请求中的 `id` 参数、Bed 路径和 `X-Hostel-Bed` 仍接受 Name；本次不改变请求寻址。
仅扫描到磁盘残留、尚未恢复本地身份的记录，以及只有远端数据的 purge，`id` 为空；
只读 inventory 不生成身份，也不会用 Name 冒充 ID。

## 其它 HTTP 字段

`status.lifecycle` 表达 Bed 生命周期，`status.pillows` 表达每个 Pillow 的准备与主进程事实，
`status.port_mappings` 表达端口发现。生命周期 readiness 使用 `readiness.status`，
Pillow 使用 `prepared` 区分环境准备，`phase`、`ready` 和 execution 字段表达主进程状态。

创建声明使用有序 `pillows`；环境、workdir 和 mounts 归属 Pillow，卷来源归属 Bed。
首项是默认 Pillow，省略列表创建 main，显式 null 或空列表拒绝。主进程使用直接字段
`command`、`args`、`restart_policy`，管理路径为 `/v1/beds/:bedId/pillows`。
Volume 使用互斥来源 `managed` 或 `host_path`；`managed.persistence` 为 `none` 或 `sync`。
Pillow 的挂载路径与 Volume 的持久身份独立，完整数据契约见 [filesystem.md](filesystem.md)。

Pillow 主进程的 outcome 保留既有诊断 JSON；底层 executor 类型不成为公开依赖，私有 Detail
继续清空。Amenity 的 status 是设施拥有的可扩展 JSON 值；公开契约不暴露内部 sealed Tenant
接口。路径、资源句柄、锁和发布策略留在内部实现。

服务端先取得领域快照再构建响应，不通过协议对象改变资源状态。`protocol` 的字段分组也不
表达调用方的即时/延迟落库要求；这些规则由调用方维护。

## 验证

`protocol` 的外部包测试使用既有 wire shape 验证解析和往返；API 测试使用公开类型读取实际
Bed 和 Pillow 主进程响应，检查 readiness 与执行身份。构建时可用 `go list -deps ./protocol`
检查依赖，本包的传递依赖应全部为标准库。
