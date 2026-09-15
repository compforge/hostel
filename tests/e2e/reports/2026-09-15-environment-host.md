# Environment / Host 接入验证

目标是验证同一 Hostel suite 接受 Environment 配置、记录实际条件，并区分环境错误与
用例失败。环境为本机 macOS / arm64，实测 Darwin kernel 25.6.0；Hostel 测试构建
v0.1.20，基于 b240313 加本次工作树改动。harness 依赖固定为 2969dd5a1c30。

## 已执行

| 验证 | 结果 |
| --- | --- |
| Hostel make build / make lint / make test（race） | 通过 |
| Linux amd64 E2E runner 交叉编译 | 通过，仅构建验证 |
| TestRuntimeContract | 通过 |
| TestFeaturePoliciesOff | 通过 |
| TestFeatureRequiredStartupFailure | missing-pathshim / conflicting-boundaries 两个子用例通过 |
| 本机声明 require.os=linux | 预期 error；实测 os=darwin，产品用例未执行 |
| 本机全量 suite（failfast） | fail；首个 unzip 用例要求用户态路径视图，本机缺少 pathshim/PRoot |

通过用例的项目入口：

```sh
make e2e "E2E_ARGS=-failfast -run 'TestRuntimeContract|TestFeaturePoliciesOff|TestFeatureRequiredStartupFailure'"
```

最终通过运行 ID：`20260915T034517.382245000Z`。
全量首个用例失败运行 ID：`20260915T034009.857340000Z`。
报告位于 `runs/hostel-runtime/<run-id>/`，含 verdict 与 Environment 快照。
最终通过报告的 variant 保留 test_filter，不代表全量 suite 通过。

首次全量尝试还遇到默认 Chromium CDP 端口 9222 被占用，daemon 未就绪；该次运行
被中止。fixture 已改为每个实例使用独立 CDP 端口，并明确不连接环境变量指定的外部
浏览器。现有浏览器进程未被终止。修正后的运行已成功启动真实 daemon。

## 覆盖边界

环境报告实测了 runner 与 colocated binary 的 OS / architecture / kernel，并验证了
不满足必需条件时产生 error、原生用例失败时保留 fail、局部通过时保留测试选择器。
本机没有执行 Linux ptrace、AppArmor 或 cgroup 条件组合。

devbox / Kubernetes 配置及 Pod runner Dockerfile / Job 是可审阅的运行入口，尚未在
目标环境执行；没有安装 Kubernetes，也没有将模板检查或交叉编译当作权限矩阵通过。
全量 unzip 失败的原断言保留，不能通过忽略该失败声称全量覆盖完成。
