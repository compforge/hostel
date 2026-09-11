# 文件操作与传输

## 文件操作入口

调用方根据数据从哪里来、到哪里去选择 API；这些入口共享 BedFS 的路径归属与文件访问边界。

| 场景 | 入口 | 数据流与生命周期 |
|---|---|---|
| 查询、修改 Bed 内文件 | `/files/info`、`/files/search`、`/files/mv`、`/files/replace`、`/files/permissions`、`DELETE /files`；目录走 `/directories` | 直接操作所选 Bed，结果随请求返回 |
| 客户端上传、下载文件 | `POST /files/upload`、`GET /files/download` | 文件字节通过 HTTP 客户端与 Bed 之间传递 |
| Bed 与 S3 直接复制文件 | `/v1/beds/:id/transfers` | Hostel 后台传输；客户端提交源、目标，查询进度或取消，详见下文的传输契约 |

`/files/*` 与 `/directories` 通过 `X-Hostel-Bed` 选择 Bed，Transfer 通过 URL 中的 Bed ID
选择。两类入口都使用 Client path，不要求调用方知道 carrier 上的实际目录。
自动持久化由 Store 的同步与生命周期策略触发，独立于这些显式文件操作，详见 [Store](store.md)。

## 远端传输的概念与边界

Transfer 是一次 Bed 内文件与 S3 对象之间的单向复制。Hostel 只认识源、目标和传输选项，
不解释数据是否用于备份、恢复、素材导入或产物导出。`/files/*` 用于 HTTP 客户端与 Bed
之间的文件操作；`/v1/beds/:id/transfers` 让 Hostel 直接搬运数据，客户端只控制操作。
共同的路径空间、文件归属与隔离语义见 [BedFS](filesystem.md)。

Store Manager 同时拥有自动持久化和显式传输。两者共用 S3 client，但具有不同的生命周期：
自动持久化由 `bed.store` 与 Bed 生命周期驱动；Transfer 由调用方触发，不读写 Bed generation、
持久化提交点或同步完成标记。`bed.store=noop` 仍可传输，未配置 S3 时传输返回不可用。

S3 使用实例既有的 bucket、endpoint、凭据及 prefix 配置。Transfer 的 key 相对
`<HOSTEL_S3_PREFIX>/.transfers/` 解析，与自动持久化布局分开。该名称不能成为合法 Bed ID，
Bed purge 和自动持久化 GC 不删除这里的数据。对象 key 不自动包含 Bed ID；上层负责目标分配
与访问授权。Transfer API 不接受临时凭据、任意 bucket 或 endpoint。

## 流程

调用方先创建 Bed 并等待 Ready，再发起 Transfer。Store 在注册任务时通过 Bed manager
取得一个 `file` operation，覆盖整个传输周期；HTTP 返回不会结束该 operation。请求断开不取消
后台传输。查询、重放与取消不会创建 Bed，也不额外刷新 Bed 活跃度。

传输直接使用 BedFS 的受限文件访问，不经过 Executor 或 `/command`。一个文件完成发布后，
进度增加对应的文件数和字节数。最终释放 operation，再公布终态。写入本地目录与普通 file API
一样更新活动水位；对于启用了自动持久化的 Bed，后续同步可感知这些写入。

普通 evict 在传输运行时拒绝回收。Purge 和 daemon 关闭则取消并等待传输退出，之后才能关闭
BedFS 或删除本地目录；等待失败保留现场供清理重试，不能把取消请求已接受当作传输已退出。

## 远端传输 API

| 方法与路径 | 语义 |
|---|---|
| `POST /v1/beds/:id/transfers` | 发起或重放一次传输；运行中返回 202，已有终态返回 200 |
| `GET /v1/beds/:id/transfers/:transfer_id` | 查询运行进度或终态；未知或过期返回 404 |
| `DELETE /v1/beds/:id/transfers/:transfer_id` | 请求取消；仍运行返回 202，已结束返回 200 |

上传目录内容：

```json
{
  "id": "export-001",
  "source": {"type": "bed", "path": "/workspace/project"},
  "destination": {"type": "s3", "key": "exports/project/"},
  "overwrite": false,
  "timeout_ms": 300000
}
```

下载目录内容时交换两端：

```json
{
  "id": "import-001",
  "source": {"type": "s3", "key": "exports/project/"},
  "destination": {"type": "bed", "path": "/workspace/imported"}
}
```

- 必须一端为 `bed`，另一端为 `s3`。Bed 路径与 file API 一致：绝对路径相对 bed_home，
  相对路径从 `/workspace` 开始，不启用 dorm 的宿主文件读取回退。
- S3 source key 以 `/` 结尾表示按前缀复制目录；否则读取一个精确对象。目录前缀没有对象时
  成功复制零个文件。单文件上传的目标 key 是精确对象名，不得以 `/` 结尾。
- key 必须为相对路径，不能含空路径段、`.`、`..` 或反斜线。下载遍历也检查远端对象名，
  防止不规范的 S3 key 在本地被折叠为其它路径。
- `overwrite` 默认 false。同名文件存在则失败；true 替换同名文件。两种模式都保留目标端
  多余文件，不做镜像同步删除。
- `timeout_ms` 省略或为 0 时使用 5 分钟；允许 1 毫秒到 2 小时，超限返回 400。

状态响应包含 `id`、`bed_id`、`instance_id`、`state`、`files`、`bytes`、`started_at`，
结束后包含 `finished_at`，失败时包含经过脱敏的 `error` 分类。`state` 为
`running / succeeded / failed / canceled`。`files`、`bytes` 统计已完整发布的文件，不是传输中
的网络字节数；超时属于 failed。失败日志包含 Bed、操作 ID、阶段、相对于本次传输目录的
文件路径和错误分类；不记录原始 SDK 错误、凭据或 carrier 绝对路径。

`GET /v1/beds/capabilities` 报告 `file_transfers` 与 `transfer_instance_id`。
`file_transfers` 表示配置了 S3 bucket，不代表已经验证远端权限或可达性。
所有 Transfer API 响应通过 `X-Hostel-Transfer-Instance` 返回当前实例身份，包括 404。

## 重试与生命周期

调用方提供 `id`，范围为当前 Store Manager 实例内的 Bed。相同 ID 和参数在记录保留期内
返回原操作；不同参数返回 409。并发请求先注册再执行，只启动一次。记录仅在内存中保存，
完成后最多保留 24 小时；最多同时运行 4 个传输，运行并发满时返回 429。总记录容量为
1024 条，新操作获准后按完成时间淘汰最旧的终态记录，运行中的记录不会被淘汰。幂等重放
仅在记录仍被保留时有效；容量淘汰可能早于 24 小时，旧 ID 再次 POST 会成为新操作。

需要抵御实例重启和延迟请求时，先从 capabilities 获取实例身份，在 POST 的 `instance_id`
以及 DELETE 的 `?instance_id=...` 中携带该值；实例不符返回 409。实例重启后，旧记录不恢复，
查询返回当前实例身份与 404。调用方不能据此推断旧传输成功，也不能将未携带实例身份的重试
当作跨实例幂等执行。保留期以外需要使用新的操作 ID，并由调用方决定如何处理目标端已有文件。

取消会停止后续复制，并等待正在执行的 I/O 收口；已经完整复制的文件保留。大文件使用 S3
multipart upload，失败或取消后尝试 Abort；取消响应的 202 不承诺清理已经完成。

## 数据保证

- 目录复制保持文件的相对路径，不添加源目录的 basename；空目录不生成远端对象。
- 只支持普通文件与目录，拒绝遇到的符号链接和特殊文件。普通对象复制不携带完整 POSIX
  元数据，下载新文件使用 0644，覆盖已有普通文件保留其权限位，由 BedFS 处理所属用户。
- 本地下载写入临时文件，完整后发布；不覆盖模式使用原子创建，覆盖模式原子替换目标文件。
  S3 不覆盖模式在 PUT 或 CompleteMultipartUpload 时使用条件写，避免 HEAD 后 PUT 的竞争。
  远端必须支持该条件写语义，不支持时传输失败，不退化为无条件覆盖。
- 每个文件独立提交。目录整体不是原子快照，失败可能留下已复制的部分；不会自动回滚。
- 传输不会冻结 Bed 内的写入进程。源文件并发变化时不保证目录一致性，也不承诺文件系统快照。
- 这里传输普通文件对象，不解析 restic repository、snapshot ref、CAS 或 pack 布局；不能用
  普通 Copy 冒充对这些格式的备份或恢复。格式化数据搬运由对应格式能力承接。
