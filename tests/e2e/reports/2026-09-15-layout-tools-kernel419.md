# API / Tool 重构：Linux 4.19 E2E

## 对象与执行条件

2026-09-15，基线 `7ce48e5e256c1540594dca4a4989764920a932bf` 加
`refactor-status-supervisor-layout` 工作区改动；Hostel v0.1.25、status schema v8。
macOS 交叉编译静态 Linux amd64 daemon 与同源码 E2E runner，二者在 Debian 10、
Linux `4.19.0-27-amd64` 上以 root 运行，数据面使用 fixture 的 loopback HTTP。

沿用目标机任务目录中的 bubblewrap 0.4.1、PRoot 5.1.0 与 libtalloc 2.1.14。
ptrace handshake 通过；`kernel.unprivileged_userns_clone=0` 前后不变。
本轮没有安装系统包、修改内核配置或部署镜像。

- SUT SHA256：`751a9a72d48d92c3bbe8ccc4bec530e4cb56bcda0bce51aa826ef92e76033408`
- Runner SHA256：`9e22f389abcb9555ac3d1e28a55f8379b02b442ba67889b4575a3c9cb4d80a9e`

## 入口与结果

使用项目 `tests/e2e` 原有完整 binary suite，无 test filter、无重试。
项目入口等价于在目标机具备构建工具时执行：

```sh
HOSTEL_E2E_REQUIRE_ROOTFUL=1 HOSTEL_E2E_PROOT=<prepared-proot> \
  make e2e E2E_ARGS='-timeout 10m'
```

本次目标机无 Go，按既有交叉编译方式执行同一 suite：
`./hostel-layout-e2e.test -test.v -test.count=1 -test.timeout=10m`。
通过 `HOSTEL_E2E_BINARY`、`HOSTEL_E2E_ENVIRONMENT_FILE`、
`HOSTEL_E2E_ON_TARGET=1` 指定同机 SUT、kernel419 环境和现场观察。

原生退出码 **1**，harness verdict **fail**：19 个顶层用例 PASS、1 个 FAIL、9 个 SKIP。

失败为 `TestStatusScopesAndTenantIdentity`：Bed 已 Ready，详情实际返回 7 个
component（filesystem、privilege、network、store、executor、resource、services），
测试仍断言数量为 6。基线的 `internal/instance/status.go` 已包含这 7 项，
基线测试也已写死为 6；这是既有测试契约滞后，不是本次目录迁移新增字段。
失败发生在首次详情检查，因此该用例后续全局 schema 与 Tenant 身份断言未执行。
该首轮未修改断言或重试，首次失败证据保留；后续修复验证见下节。

重点通过：

- BedRootServiceCommandFiles：mount / PRoot × local / supervisor × file-api / executor，8 个叶子通过。
- FilesystemPermissions：dorm / room / suite / carrier × local / supervisor，8 个叶子通过。
- RootfulPreparedMount：local / supervisor，2 个叶子通过。
- ServiceExecutableUsesBedView：mount / PRoot × local / supervisor，4 个叶子通过。
- Tool policy、命令/session、文件 API、Bed 回收、MCP SSE/Streamable HTTP 与基础 runtime 契约通过。

9 个顶层跳过项为 image-only PRoot 回退、3 个 network profile/helper 用例、
rootful 网络解析、真实 S3 往返以及 PyPI/npm/Chromium 3 项。
pathshim 未提供，本轮不证明真实 pathshim 路径重定向；网络/cgroup 降级不算强隔离验证。
helper 为目标机旧 userland，不能替代镜像中升级后工具的联调。

## 证据与清理

远端任务目录 `/tmp/hostel-layout-e2e.O2WjIX` 保留构建、配置、运行脚本和 `suite.log`。
harness run ID：`20260915T091611.875218543Z`；其 `verdict.json`、`environments.json`
与日志已取回本机 `/tmp/hostel-layout-k419-evidence/`。

测试结束后未发现 Hostel/PRoot/bwrap 进程、指向本次构建目录的进程 FD 或
`hostel-e2e-beds-*` 工作目录残留。fixture 已完成清理；证据和构建有意保留。

## 状态用例修复验证

随后修复 `TestStatusScopesAndTenantIdentity`：分别断言 Bed 与实例的完整
component 名称集合，不再只使用无名称的数量常量；Bed 包含 privilege/services，
实例包含 privilege/configuration。生产 SUT 未变，重新编译测试 runner：
SHA256 `dcf91d78b9a4d6747a138068687f4d772c100dcc555ef2a49f234686958f9a0c`。

在同一环境定向执行 `-test.run=^TestStatusScopesAndTenantIdentity$ -test.count=1`
通过（0.59 秒），原生退出码 0、harness verdict pass，无 skip。
此次完整走过 schema v8、状态读取不分配 Tenant/不续租、inventory 不展开详情、
同名 Bed 重建后 Tenant 身份改变等后续断言。`make lint` 通过。

修复后 run ID：`20260915T092031.978267327Z`，日志 `status-fixed.log` 与原生报告
取回同一本机证据目录。清理检查未发现测试进程或 Bed 工作目录残留。
此轮是定向通过，不把首轮全量 fail 改写为全量 pass。
