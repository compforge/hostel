# Component Config / Feature Policy 验证

- Subject：基于 `9bcc54da295ff485de6d5e81537bff4e82c253d4` 的 `worktree-component-features` 工作区，VERSION `v0.1.4`。
- Runner/Target：AMD Linux devbox 上的独占临时源码目录，fixture 启动真实 daemon，HTTP 通过本机 loopback 访问。
- 控制：测试构建接收内部 Options，关闭可选功能、要求缺失 helper、提交互斥配置；未修改宿主权限、sysctl 或共享服务。
- AMD 测试二进制 SHA256：`39e3da3783ac5012c5ede14aeb0bb1d0a01d623d672b34a6e76c18a7e208a04b`。

## E2E

```bash
make e2e E2E_ARGS="-run '^Test(Feature|StatusScopes|BedEvict|ExecutionLifecycle|StatefulSession|BedStoreAPI|HelperProbeFailure)'"
```

最终执行 8 个顶层用例，全部通过，无跳过，Go 报告 2.386s：

| 用例 | 结果 |
|---|---|
| TestFeaturePoliciesOff | PASS：状态 off/not_probed，Bed 命令正常，网络策略接口返回 503 |
| TestFeatureRequiredStartupFailure | PASS：缺少 pathshim、房型与 Bwrap 冲突均及时失败 |
| TestBedEvictStartsFreshWithNoopStoreAndPurge | PASS |
| TestBedStoreAPIOverridesInstanceDefault | PASS |
| TestExecutionLifecycle | PASS |
| TestStatefulSessionLifecycle | PASS |
| TestHelperProbeFailureKeepsCommandAPIAvailable | PASS |
| TestStatusScopesAndTenantIdentity | PASS |

## 编译与单元测试

- macOS：`make lint`、最终单独执行的全量 `make test`（race）通过。
- 生产构建：`make build linux` 通过，包含本机及 Linux amd64/arm64；正常构建不接受内部测试配置入口。
- AMD Linux：`make test TEST_PACKAGES='./internal/config ./internal/bed/filesystem/isolation ./internal/bed/network ./internal/bed/resource'`，四个包的 race 测试通过。
- 配置测试覆盖 S3 普通参数、凭据继承、显式空字符串/false/0、类型化路径列表及 Feature 策略冲突。
- 选择测试使用真实 selector，覆盖 Host 提供 suite 时请求 room、Required UID 覆盖默认优先级、Required pathshim 覆盖可用 PRoot。

本机使用 `GODEBUG=goindex=0` 绕过 Go 模块索引问题。一次与跨架构构建同时执行的全量测试报告
filesystem 包失败；devloop 仅输出最后若干行，缺少具体失败断言，不能确定原因。
随后定向测试及保留完整输出、未并发额外构建的全量测试均通过；该次失败保留为未定位现象，不视为已修复问题。

## 覆盖与清理边界

本次证明配置限制、Required 失败和相邻 API 契约。未执行特权 netns 流量、真实 seccomp/AppArmor
拒绝、可写 cgroup、真实 S3 持久化、镜像 userland 或 ARM 运行 E2E；这些不计入通过范围。

fixture 已结束所有测试 daemon，并检查本轮二进制无残留进程。远端独占临时源码、构建产物与日志已清理，
原有 devbox 仓库未修改。原始输出取回本机 `/tmp/hostel-component-features-amd-e2e.log`、
`/tmp/hostel-component-features-amd-tests.log` 和 `/tmp/hostel-component-features-full-test.log`。
