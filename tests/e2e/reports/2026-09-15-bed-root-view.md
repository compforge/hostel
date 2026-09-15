# Bed 原生根视图验证（2026-09-15）

## 对象与边界

基线 `b240313a22817b7e712e10bec9952cc73b01c2d4` 加本次 Bed 根视图改动，
工作树版本 v0.1.20。SUT 为 Linux amd64 devbox 上由同一源码构建的测试 binary，
不是已发布镜像或线上环境。PRoot 为 v5.1.107.92-dirty，系统 libtalloc2 为
2.4.0-f2；已移除测试 shell 的 LD_LIBRARY_PATH，动态链接实际命中系统 `/lib`。

复验入口（将 HOSTEL_E2E_PROOT 指向 runner 的 PRoot 可执行文件）：

```sh
make e2e E2E_ARGS="-run 'TestBedRootServiceCommandFiles|TestIsolationLevels' -timeout 5m"
```

## 结果

| 场景 | 结果 |
| --- | --- |
| 原生根视图 mount / local | PASS |
| 原生根视图 mount / supervisor | PASS |
| 原生根视图 proot / local | PASS |
| 原生根视图 proot / supervisor | PASS |
| 隔离等级回归 dorm / room / suite | PASS，按实际能力解释 |

补强后的最终 Linux 测试总耗时 9.042s，exit 0。根视图矩阵显式要求所选机制可用，
没有用降级或跳过替代通过。每项同时启动两个 Bed 的常驻 Service，三轮写入同名输入，
通过临时 command 与文件 API 复核 `/mnt/jobs`、`/session-cache`、`/tmp` 的输出。
测试同时验证同 Bed 数据互通、跨 Bed 同名文件不覆盖与 `process_view.rootfs=true`。

四个机制/Executor 组合各包含 `file-api` 与 `executor` 两个写入子用例，共 8 项全部通过。
每个子用例独立跑三轮，两个 Bed 的输入内容不同，防止 Service 误读别床输入却仍写入自己
输出目录的错误被掩盖。Executor 直接写入三个目录，Service 读取并生成输出，再由新的
Executor 命令与文件 API 在两床都完成写入后复核全部输入、输出。这直接覆盖同 Bed
Executor → Service → Executor 双向文件共享，以及不同 Bed 相同路径的数据分离。

隔离回归中 room 的文件等级降级为 shared；suite 的文件机制实际为 bwrap/private，
但综合 isolation.effective 仍是 dorm。因此该回归不证明完整 room/suite 能力成立。
PRoot 的路径兼容性也不构成恶意进程隔离保证。

macOS 最终代码的 `make lint`、`make test`（race）、`make build` 和
`git diff --check` 通过。早期运行遇到缺失 libtalloc、Go 校验源配置与测试正则
shell 引号问题；修正 runner/命令后执行上述最终运行，没有降低测试断言。

## 清理与未覆盖项

由既有 E2E harness 停止测试进程并清理临时目录；测试清理各 Bed 时要求 purge 成功。
源码和 binary 保留在 devbox 的任务工作目录，系统 libtalloc2 按要求保留。

没有执行完整 E2E suite、pathshim 原生根视图、静态 Go HTTP Service、外部对象存储
或下游业务联调；没有构建发布镜像或更新在线部署。本报告只证明本地原生进程协作契约。
