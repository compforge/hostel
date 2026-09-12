# Bed identity / Status 验证

2026-09-12，`bed-identity-status` 工作树，基于 `main@f6939ab`，版本 `v0.1.1`。
测试覆盖本次未提交改动；不使用旧分支的结果替代。

## 代码与制品

- 源码摘要（排序后的 cmd/internal/tests Go 文件、go.mod、go.sum、VERSION，路径与内容以 NUL 分隔）：
  `2b14284996ff9630207ebd8900262de11ace196f6b23d4f553515a39bcbbdf71`
- AMD Hostel binary SHA-256：
  `05b3c8a16f9a16476e501b1a30ad6f6b2ee4a346d5f192068731b0eaf4b9917f`
- AMD E2E runner SHA-256：
  `284208224b942c526c885fe4dd1fcc9b2a9ca59ea62ed6536eb1ca51370f13ee`

## 验证结果

| 检查 | 环境与入口 | 结果 |
|---|---|---|
| 静态检查 | macOS，devloop `run_validate.py` → `make lint` | PASS |
| 全量单测 | macOS，devloop → `make test`，race detector | PASS |
| 全量单测 | AMD devbox，`make test`，race detector | PASS |
| 原失败用例 | AMD，pathshim v0.1.6，定向 `make e2e` | PASS：8 Bed 并发 unzip 替换 |
| Core E2E | AMD，最终工作树 `make e2e` | PASS：11 个顶层场景通过、9 个显式跳过、0 失败 |
| 网络专题 | 独立 Docker 网络的临时特权容器，最终 binary/runner | PASS：策略流量与 namespace 两个场景 |
| 真实 S3 | 临时 MinIO，最终 binary/runner，`TestMixedBedStoresRoundTrip` | PASS：cas/pack/tar/noop、跨 daemon 恢复及 purge |

macOS 使用独立 `GOMODCACHE=/private/tmp/hostel-gomodcache`。项目尚无 `make fix`，本次通过临时
Makefile 的 `fix: fmt` 调用已有格式化入口，没有新增项目 target；随后 lint/test 针对稳定代码运行。

新增单测覆盖 Name/本地 ID、同名重建、重启恢复、共享 Bed 重初始化、Forget 清除状态、孤立身份记录恢复、
Executor 退出/替换、并发策略发布、策略在途时清理超时、实例计数、跨 Bed 关闭总预算和清理 owner 保留。

## pathshim 的实际结论

此前 AMD 使用 v0.1.3 时的失败不能归因为 Hostel 将 `probe` 放在 `--bind` 前。
镜像已锁定的 v0.1.6 支持 `probe --bind ...`；本次从现有 Bedbox 镜像提取并确认该版本，
保持这一合法顺序，让 probe/run 共用 bind 构造，并使 fake helper 严格检查参数。

指定 `HOSTEL_E2E_PATHSHIM` 后，dorm unzip 用例在测试专属 PATH 中禁用 PRoot，明确断言
`workspace_view=pathshim`。定向及最终完整 core 均通过，未用 PRoot 的成功替代 pathshim 证据。

## 覆盖边界与清理

- AMD core 实际实现 dorm/direct + pathshim、suite/bwrap + mount；请求 room 降级为 dorm，
  room 的独立 unzip 子用例跳过，不能宣称验证了原生 room。
- 网络专题覆盖 local/supervisor × dorm/room/suite 请求矩阵；容器内实际文件机制为 dorm/direct
  或 room/uid，suite 请求降级为 room。网络必须启用，检查独立 netns、零 capability、命令/session
  一致、策略变更、兄弟 Bed 不受影响及 purge/recreate；不把文件档位降级算作 suite 验证。
- Core 中网络/S3 的 gate 跳过已由上述独立专题补验。`TestNetworkTrafficProbe` 是流量专题调用的
  子进程入口，其顶层 skip 不表示流量专题未执行。
- PRoot 回退专题及 PyPI/npm/Chromium image userland 未执行。没有构建或推送新镜像。
- 每个 daemon、工作空间、网络容器与 MinIO 均为本次测试专用；MinIO bucket 由 suite 创建并删除，
  Docker 容器退出清理。完成后未留运行中的 `hostel-identity-*` 容器；未修改任何已部署服务。

原始日志保存在本机 `/private/tmp/`：
`hostel-identity-validation-delivery.log`、`hostel-identity-linux-tests.log`、
`hostel-identity-core-e2e-delivery.log`、`hostel-identity-network-e2e-delivery.log`、
`hostel-identity-s3-e2e-delivery.log`。
