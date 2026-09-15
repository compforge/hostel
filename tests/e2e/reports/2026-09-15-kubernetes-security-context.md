# Devbox Kubernetes securityContext 实验

## 环境与产物

- devbox：`10.37.94.68`，veLinux 2，amd64，kernel `5.15.120.bsk.3-amd64`。
- cgroup v1；内核启用 seccomp，`CONFIG_SECURITY_APPARMOR` 未启用。
- 原生单节点 K3s `v1.34.11+k3s1`，containerd `2.2.7-k3s1`；未使用嵌套 Docker 节点。
- K3s systemd 服务 `hostel-k3s`；配置 `/etc/rancher/hostel-k3s/config.yaml`；
  kubeconfig `/etc/rancher/hostel-k3s/kubeconfig.yaml`；数据 `/data00/hostel-k3s`。
- Hostel 运行时源码 `82a2c2fc24c7e2293780e819d8202ffdc3b75743`（合入 PR #133 的 main）。
  daemon 与测试 runner 均在 macOS 交叉编译为 Linux amd64；userland 固定为 bedbox `0.0.71`。
- 测试镜像 `hostel-e2e:82a2c2f`；K3s 中 manifest digest
  `sha256:8b73e11553404362246f3d7c279d34b608f52a480c72be6a66c49d77dd5b1521`。
- 部署 fixture 通过开发中的 `harness-toolbox` 0.2.2 源码运行，使用 `kubernetes-asyncio` 原生 API；
  没有把本地源码联调描述为 SDK 已发布。

## 实际执行

| 场景 | 观察 | 结论 |
|---|---|---|
| PSA restricted + ptrace-allowed | API 403：root、SYS_PTRACE、Unconfined seccomp 等违反 restricted | 准入拒绝已留证；Hostel suite 未执行，记录为 environment error |
| apparmor-required | Pod Failed，reason=AppArmor；节点报告 AppArmor 未启用 | 节点启动拒绝已留证；未验证 AppArmor 约束下的 Hostel |
| runtime-default | Seccomp=2，ptrace handshake passed | 三个基础契约用例通过 |
| ptrace-allowed | Seccomp=0，SYS_PTRACE，ptrace handshake passed | 同一组基础契约通过 |
| ptrace-denied | Seccomp=2，ptrace TRACEME 返回 operation not permitted | 同一组基础契约通过，ptrace 不可用未使基础 API 整体失效 |
| drop-all | CapEff/CapBnd=0，NoNewPrivs=1，ptrace handshake 仍 passed；启动拒绝指定的 Bed 用户 | native suite fail；镜像显式要求 1000:1000，当前权限不能切换到该用户 |
| drop-all-inherit-user | 保持 drop-all，只移除镜像的 HOSTEL_BED_UID/GID | 同一组基础契约通过；首次失败单独保留 |
| admission webhook 开启后的变更／拒绝 | 未执行；查询结果无 mutating/validating webhook | 当前不能声称已覆盖 webhook 行为 |

原生 API 的最终 AppArmor 运行是 `native-apparmor-final`，namespace
`hostel-e2e-f98c3b543a92`。原生 PSA 运行是 `native-psa-restricted`，namespace
`hostel-e2e-a812e7a834f9`。均已完成 namespace 删除；SDK 删除使用服务端 UID precondition。

运行时对比使用 `TestRuntimeContract`、`TestFeaturePoliciesOff` 和
`TestFeatureRequiredStartupFailure`；未运行完整 Hostel E2E suite，也未验证网络跨 Pod、
webhook mutation 或完整 cgroup 隔离。各组使用同一个镜像；额外用户选择变化只出现在
命名明确的 drop-all-inherit-user profile 中。

证据在访问 Host 的 `~/hostel-k8s-lab/runs/`，已复制到本工作区
`runs/kubernetes-native-20260915/`。包含提交的 Pod、admission 后的 Pod、节点与准入配置、
事件和 summary。没有启动容器的场景不产生 native suite 的环境证据，不能作为 suite pass。
较早 `native-apparmor-required` 的 summary 记录了取日志的 400 错误；原始 Pod 和事件仍保留
AppArmor 拒绝。最终 fixture 优先报告节点的启动拒绝原因。

## 未完成的环境前提

API 当前仅监听 localhost，K3s agent 尝试经节点 IP 回连时失败，CoreDNS 未就绪。
同 Pod、loopback 的上述基础契约不依赖集群 DNS，已在保留这个环境缺口的条件下执行。
自动审批拒绝了改为监听 `0.0.0.0`，理由是扩大持久网络暴露面、缺少具体授权。
已向用户请求只监听 devbox 指定内网地址 `10.37.94.68:6443`，保留 TLS 和身份认证；
截至本报告该请求待回复，监听配置未改。

AppArmor 的实际约束实验需要支持并启用它的内核。当前节点只能证明请求该能力时的拒绝行为。
测试专用 seccomp profile 已安装到 `/var/lib/kubelet/seccomp/hostel/deny-ptrace.json`，
只有显式引用它的测试 Pod 使用；节点默认策略未改。cgroup v2／委派不是当前节点的事实。

## 开发检查

- toolbox：155 项测试通过，Python 工程整体 lint 通过，0.2.2 wheel 构建通过。
- 原生 API stub 覆盖 admission 返回值、403 不重试、namespace 边界、UID 删除、Pod 完成等待、
  日志大小限制和替代 Pod 身份拒绝。
- Hostel：build 与 lint 通过。默认包并行 race 测试两次在
  `TestRequiredPathshimOverridesAvailablePRoot` 的 helper 探测中超时；相关包单独运行通过。
  `GOFLAGS=-p=1 make test` 完整 race 用例通过。未修改断言、探测超时或产品代码；
  默认包并行下的两次失败仍保留，不能据此宣称该执行条件已通过。
