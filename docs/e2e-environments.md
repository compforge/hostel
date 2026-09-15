# E2E Environment

Hostel 的 E2E 验证同一套 Bed 运行契约在不同环境中的行为。local、devbox、
Kubernetes 是环境选择；binary / image 是被测产物形态；权限 profile 描述运行条件。
环境与 profile 不复制业务用例。

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
