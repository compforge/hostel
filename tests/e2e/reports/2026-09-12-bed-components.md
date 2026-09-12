# Bed 组件化重构验证（2026-09-12）

## 测试对象与边界

Runner 为 macOS arm64 本机，SUT 为当前工作树构建的 Hostel v0.0.48 binary。
代码基线为 `9e2b242eedf85b3dce1885a6954f382c5076e243`，加本次未提交的 Bed 组件化重构。
测试仅访问 runner 本机端口、临时 workspace 和测试自带的假 S3/MCP endpoint；没有部署或操作 QA/dev。
测试入口、临时进程及目录清理由现有 `tests/e2e` harness 负责。

## 验证结果

- devloop 完整验证：`make lint`、`make test` 均通过；test 使用 race detector 和 `-count=1`。
- `make linux`：Linux amd64、arm64 二进制构建通过。
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -exec /usr/bin/true ./...`：Linux 专属测试编译通过；没有执行 Linux 测试程序。
- `make e2e`：原生结果 **FAIL**，make exit 2；20 个顶层场景中 10 passed、9 skipped、1 failed。

通过的场景：

- `TestBedEvictStartsFreshWithNoopStoreAndPurge`
- `TestBedStoreAPIOverridesInstanceDefault`
- `TestExecutionLifecycle`
- `TestStatefulSessionLifecycle`
- `TestFilesystemAPIContract`
- `TestIsolatedSessionCompatibility`
- `TestIsolationLevels`（按本机实际降级能力）
- `TestHelperProbeFailureKeepsCommandAPIAvailable`
- `TestMCPRemoteTools`
- `TestRuntimeContract`

失败场景为 `TestConcurrentUnzipReplaceAcrossBeds/dorm`：该场景要求有效的用户空间路径视图，
本机没有 pathshim / PRoot，能力报告为 `mode=carrier, available=false`，在前置能力断言处失败。
在改造前 HEAD 的临时 checkout 构建原 binary，单独运行同一场景，得到相同能力报告和失败；
因此这项失败属于当前 runner 能力与测试要求不匹配，不能将完整 E2E 标为通过。
未修改或跳过该场景的断言。

9 个 gated/skipped 场景分别涉及 PRoot/pathshim 回退、网络 namespace/策略/流量、真实 S3 往返、
PyPI/npm/Chromium userland。当前结果不证明 Linux 特权隔离、真实 S3 数据完整性或镜像环境已验证。

## 新增回归覆盖

共享 Spec/Status 的可变成员复制、并发分域写入不覆盖对方、LocalID 与 InstanceID 的寿命区分；
daemon Close 等待后台循环后才释放 BedFS、停止准入并保留本地数据；启动失败回滚。
现有初始化网络回滚、策略在 Ready 前生效、清理重试、UID 保留及同名 Bed 替换测试继续通过。

## 复验

所有命令在该 Hostel 工作树运行。本次使用 `GOMODCACHE=/private/tmp/hostel-gomodcache`。
按 [E2E 入口](../README.md) 在带有所需路径工具的 Linux runner 运行 `make e2e`；
特权网络、真实 S3 和镜像 userland 按各自显式开关验证，跳过不计为通过。
