# Rootfs / PathMappings 权限实验（2026-09-15）

## 对象与入口

SUT 为 main 基线 `7b5660bd5acdf84e9224e8bb3ce89ba28304cc2e`（v0.1.22），
加本次 E2E 用例；运行时代码未修改，版本未递增。Linux amd64 daemon 与 runner 由同一
工作树交叉编译，放入相同 userland。候选标识 `7b5660b-fs-5e690d83748f`，
镜像 config ID 为
`sha256:553d2d41b16307735023b39851d22f006814ce1b6acc0a088c4e1c93ee580766`。
镜像保留 `HOSTEL_BED_UID/GID=1000`，未发布生产镜像。

在 devbox 自建单节点 K3s 的独立测试 namespace 中运行，runner 与 daemon 同 Pod，
经实际 loopback HTTP 就绪后调用公开 API。节点内核为 Linux 5.15.120.bsk.3-amd64，
Pod userland 为 Debian 12。操作入口使用访问 Host 持有的 kubeconfig，
以及正式 PyPI 的 harness-common 0.3.1 / harness-toolbox 0.2.2。

~~~sh
python3 tests/e2e/environments/kubernetes/run.py \
  --kubeconfig <access-host-kubeconfig> --host <access-host> \
  --image hostel-e2e:filesystem-permissions --profile <profile> \
  --test-filter '^TestFilesystemPermissions$' --output <run-directory>
~~~

每组运行 dorm / room / suite 的 Auto 文件机制，加全关闭机制的 carrier 对照，
分别覆盖 local / supervisor。Network、cgroup 在本用例中关闭。
每个组合创建两个 Bed 和可被 Bed UID 访问的测试源目录；不访问现有业务数据。
具体断言与 profile 使用见 [环境说明](../e2e-environments.md#rootfs-与-pathmappings-权限矩阵)。

## 实测结果

五组各 8 个子用例，共 **40 PASS，0 FAIL，0 SKIP**。两个执行后端结果一致。
下面的视图来自实际 status，并由文件操作验证；房型列表示请求值。

| Pod profile | dorm / room 实际视图 | suite 实际视图 | 根视图与进程映射效果 |
|---|---|---|---|
| runtime-default | PRoot / shared | PRoot / shared | 原生根与读写映射可用；只读映射未接入进程 |
| ptrace-allowed | PRoot / shared | bwrap / private | PRoot 同上；bwrap 另兑现只读映射 |
| ptrace-denied | pathshim / shared | bwrap / private | pathshim 支持读写映射，没有完整根视图；bwrap 根与只读映射均成立 |
| drop-all | PRoot / shared | PRoot / shared | 即使 capabilities 全部移除，真实 ptrace handshake 仍通过，PRoot 根与读写映射可用 |
| filesystem-privileged | PRoot / shared | bwrap / private | 请求仍影响选择；高权限不会使 dorm 自动选择 bwrap |

每组的 carrier 对照都报告 rootfs=false、进程映射不可用；原生绝对路径读到 Carrier
fixture，API 与结构化 cwd 仍访问各自 Bed 数据。所有组合的 API 均能读取显式映射源，
写入读写源、拒绝只读写入。两个 Bed 根数据相互独立，显式共享源互通；
purge 两 Bed 后源目录与原始数据仍存在，再由 fixture 回收。

PRoot 未兑现的只读映射使用 Bed 本地候选目录：API 先读已有本地数据，无本地数据时
读源目录。该结果不表示源目录已被进程只读保护。bwrap 的只读断言使用 DAC 可写源目录，
先证明进程能读，再验证进程不能创建文件，避免把普通目录权限拒绝算成只读映射成功。
读写映射还验证了持久 session 的读写与 API 互通。

允许 ptrace 的 profile 同时取消 seccomp 过滤并添加 SYS_PTRACE；拒绝 ptrace 的实验
seccomp 只拒绝 ptrace、允许其他调用。因此这里不能把 suite 的 mount 可用性归因于
SYS_PTRACE，也不能把 ptrace-denied 解释为全面收紧的生产策略。
本轮 room 请求的文件等级实际都是 shared，未验证 confined 的 Landlock / UID 屏障。

## 证据与边界

原始证据位于运行 Host 与本地工作树的 `runs/filesystem-permissions-20260915/`：
`commands.json` 保存完整执行命令和退出码，`source.json` 保存源码与二进制指纹，
每组保存提交/admission/最终 Pod、节点、事件、环境事实和 suite 日志。
`postflight.json` 独立核对 40 个通过项、实际镜像一致性和无残留测试 namespace。
未修改节点配置、集群 admission webhook 或 API 监听地址。

macOS 的四个 local 组合通过；Linux 以真实 Pod 执行上述 40 个组合。
初次本机 smoke 发现 Make 会展开测试正则中的单个美元符号，修正选择器后运行；
只读 API 的现行错误契约为带明确拒绝原因的 HTTP 500，测试检查拒绝原因与无写入副作用，
未把 HTTP 错误码重构混入本轮。运行时错误码仍有改进空间。

这不是完整 E2E suite。Service 根视图协作已有独立用例，本轮未重跑；
未验证 AppArmor、confined 文件边界、恶意绕过 helper、远端持久化或跨 Pod 网络。
PRoot/pathshim 的路径体验不构成安全隔离保证。
