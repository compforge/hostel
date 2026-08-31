# hostel

[English](README.md) | 简体中文

**hostel 是一个面向 AI agent 的 sandbox runtime**：用一个进程管理多个互相隔离的 sandbox，对外提供 HTTP API——创建 sandbox、在其中执行命令与 shell 会话、读写它的文件。每个 sandbox 称为一个 **bed**。可跑在任何地方：你的电脑、一台 VM、CI、或一个容器里。

资源与文件 API 以 [OpenSandbox](https://github.com/alibaba/opensandbox) execd 为设计基线；命令执行采用 hostel 原生协议，每次运行都有稳定 execution id，并以结构化终态保留 exit、signal 与 termination cause。

## 初衷

给每个 agent（或用户、或任务）配一整个 VM / 容器，启动慢、且空闲时也占着真实的 CPU/内存——而 agent 的负载大部分时间是空闲的（墙钟大头在等模型、不在跑命令）。想同时跑很多个时，这种粒度很浪费。

hostel 走更轻的路：把多个隔离的 **bed** 装进一个进程。bed 创建近乎瞬时、空闲时几乎零成本，于是一台机器 / 一个容器能承载大量 bed，被多个 agent 复用。隔离是文件系统级的（bed 共享宿主内核）——适合**可信 / 半可信**代码；**不可信**代码应使用更强的隔离（microVM 或独立的 VM/容器）。

## 运行时模型

- **Bed 是一个持久的 sandbox 身份**：workspace 和生命周期不随当前进程承载域的替换而消失。
- **Executor 是 Bed 当前的进程域**：拥有 command 与 session 进程，可以在不替换 Bed 的前提下重建；**Execution** 是一次命令运行，同时记录 bed id 与 executor id。
- 常驻 shell 只属于显式创建的 `/session`；普通 `/command` 每次使用独立的新进程。
- **默认 bed**：请求不带 bed id 就落到 `default`——只需要一个 sandbox 时可完全无视 bed 概念。
- **选择 bed**：请求带 HTTP header `X-Hostel-Bed`（或 `?bed=`），空即默认。bed 之间互相隔离——一个 bed 的 shell 和文件对另一个不可见。

## 快速开始

```bash
make build
./bin/hostel --isolation dorm --workspace-root ./.workspace --addr :8872

curl -s localhost:8872/ping                                   # pong
curl -s localhost:8872/healthz | jq
# 前台命令（SSE 流）
curl -sN -XPOST localhost:8872/command \
  -H 'Content-Type: application/json' -d '{"command":"echo hi > /workspace/a.txt; cat /workspace/a.txt"}'
# 文件读回
curl -s 'localhost:8872/files/download?path=/workspace/a.txt'
# 指定 bed（另一个隔离单元，看不到 default 的文件）
curl -s 'localhost:8872/files/info?path=/workspace/a.txt' -H 'X-Hostel-Bed: conv-1'
```

## API（v1）

| 组 | 端点 |
|---|---|
| 基础 | `GET /ping`、`GET /healthz` |
| 指标 | `GET /metrics`、`GET /metrics/watch`（SSE） |
| 文件 | `GET /files/info`、`DELETE /files`、`POST /files/mv`、`POST /files/permissions`、`GET /files/search`、`POST /files/replace`、`POST /files/upload`、`GET /files/download` |
| 目录 | `GET /directories/list`、`POST /directories`、`DELETE /directories` |
| 命令 | `POST /command`(SSE)、`DELETE /command`、`GET /command/status/:id`、`GET /command/:id/logs` |
| 会话 | `POST /session`、`POST /session/:id/run`(SSE)、`DELETE /session/:id` |
| 隔离会话 | `/v1/isolated/session(s)`、`run`(SSE)、会话级文件/目录接口、`capabilities` |
| bed 管理 | `GET/POST /v1/beds`、`GET/DELETE /v1/beds/:id`、`POST /v1/beds/:id/checkpoint`、`GET /v1/beds/capabilities` |

隔离会话的资源模型直接把一个 isolated session 对应到一个非 default bed，run 流与 `/command` 使用同一套 hostel 原生 execution 事件，
不额外引入第二套生命周期对象。default bed 只服务原生 API 未指定 bed 的请求，不会出现在 session
列表中，也不能通过 session 接口 attach。创建当前支持 balanced profile、bed 自有的读写
`/workspace` 和共享网络；做不到的隔离参数会明确拒绝，不会静默忽略。diff / commit 返回
`NOT_SUPPORTED`。

指标跟随目标 bed（`X-Hostel-Bed` / `?bed=`）：cgroup v2 已委派时，CPU 用量和当前内存来自该
bed 的记账组，CPU 数量和总内存仍表示共享 carrier 容量；当前不施加限额。未委派 cgroup v2
时保留 execd 兼容的实例级 fallback，`/healthz` 和 capabilities 的 `resource_accounting`
会如实报告实际 backend。

路径语义由 Bed 持有的 **BedFS** 统一负责：客户端 `/` 是 `bed_home`，`/workspace/...`、`/tmp/...` 等任意绝对路径都单射 rebase 到该 BedFS，相对路径以 workspace 为基准；file API 的 `path` 和命令 `cwd` 等结构化字段在所有隔离档一致，`cwd: "/"` 即 bed_home。`bwrap` 下完整 bed_home 有内部 Executor 投影，workspace 另以规范路径 `/workspace` 挂载；dorm/room 启动时探测 pathshim，可用时尽力提供 `/workspace` 进程视图，其他绝对路径仍保持 Carrier 语义。`workspace_mount` 只表示 suite 的真实挂载，命令依赖字面 `/workspace` 时应检查 `workspace_view`；pathshim 是兼容层，不是安全边界。详见 `docs/filesystem.md`。

## 隔离

数据隔离按**青年旅社房型**分档，`--isolation dorm|room|suite|auto`（默认 auto=环境顶格），`effective=min(请求, env 上限)`，超上限诚实降级：

- `dorm`（通铺）：无强制隔离（=direct，全平台）；pathshim 只会尽力补 `/workspace` 进程视图；
- `room`（单间，厕所公用）：landlock 内核强制——兄弟数据不可访问但可见、`/tmp`/系统路径共享，无需任何 capability（Linux ≥5.13）；
- `suite`（套房，全私有）：bwrap mount ns——兄弟不可见 + 私有 `/tmp` + `/workspace` 规范挂载（需 userns 或 CAP_SYS_ADMIN）。

启动时 probe 环境上限，healthz/capabilities 报 `isolation.{level,mechanism,requested,effective,ceiling}`。详见 `docs/data.md`。

更强的隔离（真 setuid、seccomp、每个 bed 的 CPU/内存限制（cgroup）、写时复制 overlay workspace、PTY over WebSocket）在路线图上。

## 共享服务（Chromium / Jupyter …，规划中）

有些工具启动重、但天生支持多租户——浏览器、Jupyter server。hostel 会只跑一份共享实例，用工具自身的机制给每个 bed 一份独立切片（每 bed 一个浏览器 context、一个 kernel），产物存进该 bed 的 workspace。v1 先接好释放钩子（bed 删除或超时时释放它的切片），Chromium/Jupyter 的实际接入后续再加。

## amenity(共享设施)

重资产、自带多租能力的工具由 hostel **共享一份**、按 bed 切片。首个是 **Chromium**:一份共享浏览器,每 bed 一个隔离 BrowserContext,产物落 bed workspace。启用方式:镜像带 chromium 二进制(`--chromium-path`,或自动探测)或 attach 既有实例(`--chromium-cdp-url`)。北向只给 bed 级动作(**不透传 CDP socket**):

```
POST /v1/beds/:id/browser/goto        {url}
POST /v1/beds/:id/browser/screenshot  {path?}   # 存进 bed workspace
POST /v1/beds/:id/browser/text
POST /v1/beds/:id/browser/{click,type,press,scroll,wait}
POST /v1/beds/:id/browser/close
```

浏览器首次使用时启动、空闲后自停;capabilities 报 `amenities: {chromium: idle|running}`。

## 配置

Flag（或 `HOSTEL_*` 环境变量）：`--addr` / `--workspace-root` / `--isolation` / `--pathshim` / `--default-bed` / `--shell` / `--bed-idle-timeout` / `--max-beds` / `--max-pinned-beds` / `--admission-cpu-threshold` / `--admission-memory-threshold` / `--executor` / `--store` / `--s3-bucket` / `--s3-prefix` / `--s3-endpoint` / `--s3-path-style` / `--s3-region` / `--persist-interval` / `--luggage-high-bytes` / `--luggage-low-bytes` / `--chromium-path` / `--chromium-cdp-url` / `--chromium-idle-stop` / `--chromium-debug-port` / `--enable-tracing`。

OpenTelemetry Trace 默认关闭，通过 `HOSTEL_ENABLE_TRACING=true`（或 `--enable-tracing`）启用；出口使用 `HOSTEL_OTEL_TRACES_GRPC_ENDPOINT` 或 `HOSTEL_OTEL_TRACES_HTTP_ENDPOINT`，两者同时配置时优先 gRPC。

环境变量按 owner 分命名空间：`HOSTEL_*` 只配置 daemon，外部 `BED_*` 与 Hostel 管理的 CDP endpoint 也会被过滤；Hostel 随后注入真实 bed context。其余 Carrier 环境默认传给 bed，安全性由部署方负责。request `envs` 只覆盖本次执行，且不能占用保留的 `HOSTEL_*` / `BED_*` 命名空间。S3 使用 `HOSTEL_S3_REGION`、`HOSTEL_S3_ACCESS_KEY_ID`、`HOSTEL_S3_SECRET_ACCESS_KEY` 与可选的 `HOSTEL_S3_SESSION_TOKEN`，凭据只支持环境变量、不提供 CLI flag。

Executor backend：`--executor auto`（默认）优先探测 Linux `supervisor`，失败时使用 `local`；显式 `supervisor` 时探测失败会终止启动，显式 `local` 时命令由 hostel 直接派生。supervisor 拥有整个 Executor 进程树，IPC 可重连，`Start` 按 process id 幂等；Executor 丢失对外返回稳定的 `executor_lost`，不会泄漏裸 EOF。

Bed 初始化在管理面异步执行：`POST /v1/beds` 返回 `202` 与 `status.phase=initializing`；调用方通过 `GET /v1/beds/:id` 等待 `status.readiness.status=true`。快照检查、恢复、BedFS 准备和失败原因都投影到 readiness reason/message。原生数据面仍支持首次请求惰性创建，但会加入同一个初始化并等待 Ready，不会看到半成品 BedFS。

持久化在配置 `--s3-bucket` 后启用：

- 默认 `--store auto` 将新 bed 保存为约 32 MiB 的 immutable pack。
- auto 按 bed 识别已有提交点，以兼容老客户：
  - 既有 CAS bed 可继续读取，并可迁移到 pack。
  - 已有 pack / tar bed 保持原布局。
- 显式 `--store s3|pack|tar` 不识别或迁移其它布局。
- `tar` 每次全量覆盖一个 tar.gz，让每张 bed 始终只有一个对象。
- 未配置 bucket 时，auto 使用 noop。

同 id 再建时恢复，驱逐（DELETE / idle 回收）或显式 checkpoint 时持久化。普通 operation 与 pressure 只提交可合并的同步诉求，Store 同步循环统一负责串行、失败退避和 `--persist-interval` 周期兜底。bed 的持久身份是快照，本地目录只是工作副本。

- `DELETE /v1/beds/:id` 只驱逐，保留持久身份。
- `?purge=true` 删除该 bed 的全部布局并终结身份。
- 驱逐撞上并发流量时返回 `409 BED_BUSY`，不丢在途写入。

容量：`--max-beds N` 限制 resident tenant bed 数，`--max-pinned-beds M` 是 pinned 硬上限；有 operation，或 durable store 下最新数据尚未同步，任一成立即 pinned，noop 则只在 operation 期间 pinned。`M=0` 时继承 `N`，仅两者都为 0 时不限，default bed 不参与。pinned 达到 `M` 的 80% 时，`GET /v1/beds` 上报软 `bed_pressure`，供上层提前扩容和避让；达到 `M` 后，新 resident / dormant restore 以及未 pinned 的 idle bed 返回可重试的 `429 INSUFFICIENT_BED`。pinned bed 仍由当前 carrier 承接，`pinned` / `data_synced` 继续上报完整事实。

carrier 资源准入在数量限制之外读取容器父 cgroup：近期 CPU 或当前内存使用率达到水位时，新归属或未 pinned 的 idle bed 返回 `429 RESOURCE_PRESSURE`。pinned bed 与 default bed 不受影响；不可测时 fail-open 到数量策略。

## 容器镜像

`deploy/docker/Dockerfile` 多阶段构建：静态纯 Go 二进制 + `debian-slim` 运行时，内置固定 revision 的 **pathshim**（dorm/room `/workspace` 兼容视图）、固定版本且非 setuid 的 **bubblewrap**（suite 档）与可选 **chromium**（浏览器 amenity）。hostel 启动时 probe，能力不可用就诚实降级，受限 Pod 照常服务。

```bash
make image                     # 完整镜像(bwrap + chromium),当前架构
make image-lean                # 仅 bwrap(~150MB);浏览器走 --chromium-cdp-url 或缺席
make image-multiarch IMAGE=repo/hostel:tag   # linux/amd64 + arm64,推到镜像仓库
docker run -p 8872:8872 hostel:dev
```

镜像多架构（`linux/amd64`、`linux/arm64`）：Go builder 原生交叉编译，固定 revision 的 pathshim、固定版本的 bwrap 与 Debian runtime 按目标架构构建，使原生依赖与镜像架构一致。`make image-multiarch` 需要 `docker buildx` 且直接 push（多平台镜像无法 load 进本地 docker）。

容器内默认值（均可用 `HOSTEL_*` 覆盖）：`--isolation suite`、`--workspace-root /workspace`、`--pathshim /usr/bin/pathshim`、`--chromium-path /usr/bin/chromium`。`tini` 作 PID 1；`HEALTHCHECK` 用 `hostel --health`。bwrap 是否真隔离取决于 Pod 是否允许 user namespace/mount；不可用时如实降档，pathshim 也只在自身探测通过后启用。镜像默认以 root 运行，部署时用收敛 capability 的 `securityContext` 硬化。

## 许可与致谢

hostel 采用 **Apache-2.0**（见 [`LICENSE`](LICENSE)），与其来源保持一致。hostel **基于 / 派生自 OpenSandbox execd**（https://github.com/alibaba/opensandbox ，Apache-2.0）：起步是对其隔离执行模型的重实现，后续会逐步演化分化。归属细节见 [`NOTICE`](NOTICE)。
