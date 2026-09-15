# E2E Environment

Hostel 的 E2E 验证同一套 Bed 运行契约在不同环境中的行为。local、devbox、
Kubernetes 是环境选择；binary / image 是被测产物形态；权限 profile 描述运行条件。
环境与 profile 不复制业务用例。

自建 Kubernetes 是可控制运行前提的实验环境：管理员能决定 Pod securityContext、
namespace 的 Pod Security Admission 策略，以及是否安装 admission webhook。
这些策略共同决定被测条件；仅有集群管理员权限不能补齐节点内核缺少的机制。
Environment 保存访问位置，profile 声明本轮条件，节点与 Pod 的观察结果证明实际前提。

## Environment 与可选 Host

Environment 复用 quality-harness common 模型。Host 是可选组成部分：主机环境直接在
Host 上运行目标，Kubernetes 环境通过 Host 操作集群。kubeconfig 路径属于这台 Host，
不能当成本机路径或 Pod 节点身份。环境名不隐含 OS、发行版或 kernel 版本。

配置按 `tests/e2e/environments/<name>/config.yaml` 组织，suite 入口接收解析后的
Environment。Host / Pod 的启动归部署调用方，测试 runner 与 Hostel binary 在同一个
执行环境内运行，沿用现有 loopback HTTP、临时进程与 testing.T cleanup。
每个实例使用独立 HTTP/CDP 端口，不依赖开发者机器上的默认浏览器端口。
在本机设置远端环境名不会自动把测试发送到远端。

## 执行与证据

`make e2e` 默认使用 local 配置。devbox 上运行：

```sh
HOSTEL_E2E_SSH_HOST=devbox HOSTEL_E2E_ON_TARGET=1 make e2e E2E_ENVIRONMENT=devbox
```

SSH 地址描述 Environment.Host 的访问方式；上面的命令必须已经运行在该主机。
部署调用方可以使用 toolbox Host command 执行 SSH 命令，或在 SSH 会话中调用项目入口。

suite 先采集 OS、architecture、kernel、发行版、ptrace handshake 与可读的进程权限信息，再检查
`custom.require.*` 声明的前提，随后执行原有用例。未知或不满足的必需条件产生
error，测试失败保持非零退出；每个用例的原生通过、失败与跳过情况保留在 go test 输出。

结果写入 `runs/hostel-runtime/<run-id>/`：`verdict.json` 表达 suite 级结论，
`environments.json` 保留实际条件与来源。它们不替代逐用例报告，也不能把 suite 内的
跳过项解释成已验证覆盖；variant 的 test_filter 保留原生测试选择器，列举用例不生成运行报告。
Host、runner 与 target 分别留证；Pod 的事实来自 Pod 内，
不使用部署机事实替代。这里记录的是 carrier 进程上下文，Bed 的实际隔离由已有公开 API
用例验证。

## Kubernetes runner

`tests/e2e/environments/kubernetes/Dockerfile` 把同一版本的测试 binary 与 Hostel
测试构建放入给定 userland 镜像。这样现有 config.Options 测试和网络 fixture 也能在
Pod 内复用；它验证的是该源码构建，不是镜像里原先携带的生产 Hostel binary。

在构建机执行并将镜像导入目标集群可用的仓库或节点：

```sh
docker build -f tests/e2e/environments/kubernetes/Dockerfile \
  --build-arg RUNTIME_IMAGE=hostel:dev \
  --build-arg SOURCE_REVISION="$(git describe --always --dirty --tags)" -t hostel-e2e:dev .
```

使用环境 Host 上对应的 kubeconfig，先修改 job.yaml 的镜像及 Host 元数据，再用
`kubectl create -f` 创建有独立名称的 Job。禁止自动重试，以保留首次失败；运行后采集
Job/Pod 状态、events 和 suite 日志，再删除本次 Job。集群拉镜像、调度、准入或容器启动
失败都属于环境错误，不属于测试通过。当前入口不会自动安装 Kubernetes 或修改节点策略。

默认 Job 显式使用 RuntimeDefault seccomp，并要求 Pod 内读到 Seccomp=2。
比较不同权限时保持源码和 userland 镜像相同，只改变 securityContext 与 profile 的
前提要求。Unconfined seccomp 应要求 Seccomp=0；AppArmor profile 需要节点实际支持，
自定义 profile 还需要预先装载，见
[Kubernetes AppArmor 文档](https://kubernetes.io/docs/tutorials/security/apparmor/)。

Seccomp 模式、SYS_PTRACE capability 或 AppArmor profile 名本身不能证明某次 ptrace
调用会成功或拒绝。ptrace handshake 记录 passed / failed / unavailable 及失败原因；
失败不自动归因为权限拒绝。特定 AppArmor 拒绝与受限 cgroup 的行为矩阵需要对应断言；
环境入口及配置文件的存在不代表这些矩阵已经运行。

## SecurityContext 实验

`tests/e2e/environments/kubernetes/run.py` 在 Environment 的访问 Host 上运行，使用显式
kubeconfig，通过 harness-toolbox 的原生 Python API 在独立 namespace 中启动同一套测试。
环境需要安装带资源 API 的 `harness-toolbox[kube]`（0.2.2 起）；开发联调可直接安装对应
源码包。连接池、超时、Pod 完成等待、日志及 UID 条件删除由 toolbox 提供。
security-contexts.json 声明可选条件；
`--pod-security` 决定本轮 namespace 的准入等级。默认 privileged 等级允许测试各类
securityContext，并不把 Pod 自动设为 privileged。

```sh
python3 tests/e2e/environments/kubernetes/run.py \
  --kubeconfig /etc/rancher/hostel-k3s/kubeconfig.yaml \
  --host devbox --image hostel-e2e:<revision> \
  --profile runtime-default --output runs/k8s-runtime-default
```

启动器保存提交的 Pod、admission 后的 Pod、节点信息、webhook/policy 配置、事件与
native suite 日志，并在结束时删除本轮 namespace。它不安装或关闭集群级 webhook；
配置 webhook 时应以测试 namespace 标签 `hostel-e2e=true` 限定作用范围。
对比记录区分准入拒绝、容器未启动、环境前提不满足、用例失败与用例通过。
已有容器成功退出不代表测试通过，必须存在环境证据与实际执行的用例。

ptrace-denied 需要节点事先把 deny-ptrace.json 放到 kubelet 的
`seccomp/hostel/deny-ptrace.json`。此实验 profile 只用来单独拒绝 ptrace，其余系统调用
默认允许，不能视为生产安全基线。ptrace-allowed 同时移除 seccomp 过滤并添加
SYS_PTRACE；两组都检查真实 ptrace handshake，而不由 capability 名称推断结果。

镜像内的进程配置也属于实验条件。drop-all 保留镜像建议的 Bed UID/GID；
drop-all-inherit-user 保持同一 securityContext，在启动测试 runner 时移除镜像内的
`HOSTEL_BED_UID/GID`，作为未配置建议值的对照。两者分别留证，历史失败仍保留。
`TestPreferredBedUser` 在 local/supervisor 两种 Executor 下核对实际 UID/GID、降级原因、
command/session 的 capability 与 no_new_privs，以及 File API 的读写归属；可加入 `--test-filter` 运行。

AppArmor 的实际约束测试必须使用编译并启用 AppArmor 的节点。显式请求 AppArmor
而被节点拒绝是可保留的环境错误证据，不等于已验证 AppArmor 约束下的 Hostel。
同理，cgroup v1、v2 和 cgroup 委派是不同前提，不能靠切换 Pod 的 capability 相互替代。
