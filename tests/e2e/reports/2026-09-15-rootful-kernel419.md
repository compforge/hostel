# Rootful Bed 准备验证（2026-09-15）

## 对象与边界

基线 `ffed622` 加本次 rootful Bed 准备改动，工作树版本 v0.1.23。
SUT 为 Debian 10、Linux `4.19.0-27-amd64` 上运行的静态 Linux amd64
Hostel binary，不是已发布镜像。Hostel 以 root 运行且具备所需 capabilities；
`kernel.unprivileged_userns_clone=0` 在测试前后均未修改。

使用提取到任务目录的 Debian bubblewrap 0.4.1、PRoot 5.1.0 和 libtalloc 2.1.14，
没有安装或替换系统包。这不是 Bedbox 镜像内工具版本的联调证明。
最终 SUT SHA256：
`2d578f29a9645b6c3f8f2c20e444b91671f03f95cc7e5aa6429b0ba98ada4f13`。

## 入口与结果

在具备上述权限、依赖和静态构建能力的 Linux runner 上，项目入口为：

```sh
HOSTEL_E2E_REQUIRE_ROOTFUL=1 make e2e \
  E2E_ARGS="-run '^(TestBedRootServiceCommandFiles|TestRootfulPreparedMount|TestFilesystemPermissions)$' -timeout 3m"
```

本次在 macOS 交叉编译静态 daemon 和同一源码的 E2E test binary，传至目标机，
通过 `HOSTEL_E2E_BINARY` 指定 SUT、`HOSTEL_E2E_PROOT` 指定 run-local PRoot，
执行上述正则选中的原有 harness 用例。最终日志为任务目录内 `rootful-final.log`。

| 用例 | 结果 |
| --- | --- |
| BedRootServiceCommandFiles：mount / proot × local / supervisor × file-api / executor | 8 个叶子用例 PASS |
| FilesystemPermissions：dorm / room / suite / carrier × local / supervisor | 8 个叶子用例 PASS |
| RootfulPreparedMount：local / supervisor | 2 个叶子用例 PASS |

所选 18 个叶子用例全部通过，没有 skip。验证内容包括：

- 两个 Bed 的 Service 与 Executor 在同 Bed 共享绝对路径文件，跨 Bed 同名路径不覆盖；
  每个组合运行三轮，覆盖 `/mnt/jobs`、`/session-cache` 和 `/tmp`。
- suite 的文件机制实际为 bwrap/private，进程视图为 mount，读写及只读映射均可用。
  PRoot 的只读映射能力为 false；carrier 场景无原生 rootfs，按 API 能力验证，
  不把这些降级结果当成强隔离证明。
- 同 Bed 的两个 command、session 和常驻 Service 复用同一 mount namespace，
  以相同非 root UID 运行；CapInh/Prm/Eff/Bnd/Amb 均为零，NoNewPrivs 为 1。
- workload 环境变量仍可达最终命令。Linux 专项单测另验证可信 helper 不直接启用
  workload 环境，以及 rootful 必须具备真实 host capabilities、执行阶段不得重新准备或降级。

最终代码的 `make lint`、`make test` 和静态 Linux 构建通过。
早期一次本地全量测试失败，隔离包定向重跑及最终全量重跑通过，未确认早期失败原因。
早期远端试跑发现 command/Service 在 Wrap 后覆盖了环境封装；调整为先构建环境、
再 Wrap 后完成上述最终回归，没有降低断言。

## 生命周期与未覆盖项

Bed Manager 组织准备和回收；Filesystem 持有已准备 namespace/root 的描述符。
准备 helper 退出后不保留 keeper 进程，执行者只进入已准备环境并降权运行。
既有 harness 完成 Bed purge 和 daemon 清理；最终未发现本轮 daemon、执行或准备
helper 残留。测试 binary、工具与日志保留在任务目录供复核。

未执行完整 E2E suite、pathshim、rootless user namespace 正向场景、网络/cgroup
隔离矩阵、镜像构建发布或下游联调。本报告也不宣称完成了恶意 workload 安全审计。
