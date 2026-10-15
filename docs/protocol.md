# 公开 Bed 数据契约

`github.com/qiankunli/hostel/protocol` 是服务端和 Go 调用方共用的 Bed 管理数据定义。
本包目前覆盖 Bed 摘要、详情、生命周期、服务、端口映射及 Bed 级组件状态；不代表所有
Hostel API 都已迁入。本包只依赖标准库，不包含 client、资源操作、运行时对象或上游发布策略。

## 模型归属

公开字段和 JSON tag 由 `protocol` 定义。纯数据类型由内部 owner 直接复用；内部确有
额外字段时组合公开结构，例如 FilesystemStatus 组合公开 preparation 状态并保留内部路径，
Service 状态组合公开字段并保留内部执行结果。不为内外两侧另造 DTO/View 名称或重复字段表。

Bed 的管理 API `id` 仍表示调用方提供的 name；内部 Bed.ID 表示本地生命周期身份。
本次提取不改变这一历史 HTTP 契约，也不把两个身份合并。Bed 详情由多个 owner 的快照组成，
完整 API Bed 结构不替代内部 Bed 的锁、Spec、分域写权和资源生命周期。

## HTTP 兼容

保持 `status.lifecycle`、`status.services`、`status.port_mappings` 等现有路径，
以及 JSON tag、omitempty/omitzero、空集合和数值类型。生命周期 readiness 使用
`readiness.status`，Service readiness 使用 `ready`。

Service 的 outcome 保留既有诊断 JSON；底层 executor 类型不成为公开依赖，私有 Detail
继续清空。Amenity 的 status 是设施拥有的可扩展 JSON 值；公开契约不暴露内部 sealed Tenant
接口。路径、资源句柄、锁和发布策略留在内部实现。

服务端先取得领域快照再构建响应，不通过协议对象改变资源状态。`protocol` 的字段分组也不
表达调用方的即时/延迟落库要求；这些规则由调用方维护。

## 验证

`protocol` 的外部包测试使用既有 wire shape 验证解析和往返；API 测试使用公开类型读取实际
Bed 和 Service 响应，检查 readiness 与执行身份。构建时可用 `go list -deps ./protocol`
检查依赖，本包的传递依赖应全部为标准库。
