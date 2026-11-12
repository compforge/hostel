# hostel

[English](README.md) | 简体中文

**Hostel 是面向 AI Agent 的“猴版 Docker”。** 一个 daemon 在一台机器或一个容器内管理多个
轻量工作空间（**Bed**），复用已有的系统和工具链，提供文件、命令、shell 会话和常驻进程管理，
并内建工作数据同步和共享设施管理。

## “猴”在哪里？

- **复用软件底座。** 多个 Bed 共享宿主或外层容器的程序和依赖，Hostel 不为每个 Bed
  单独拉取和启动一套容器镜像。
- **声明执行环境。** 配置环境变量、工作目录、卷挂载和可选主进程，再在这些环境中运行
  临时命令和常驻服务。
- **适应宿主条件。** 完整的 namespace、cgroup 和挂载权限并非所有 Bed 的启动前提。
  Hostel 协调各子系统，尽力利用可用机制并如实报告结果，由调用方决定是否使用；
  显式 required 工具与只读挂载等功能契约仍须兑现。
- **共享较重的设施。** 多个 Bed 可以共用 Chromium 和远端 MCP 连接，由 Hostel 按 Bed
  管理应用状态，减少重复的进程和连接。

Hostel 适合多个 Agent 任务复用已有软件环境的场景。它负责单实例内的执行、本地资源管理和
工作数据同步；集群环境下，实例调度、跨实例路由和 API 访问授权由上层控制面负责。

## 如何组织工作空间

**Bed** 是具有身份、生命周期和共享资源的工作空间，通过命名的 **Volume** 声明数据来源，
通过一个或多个 **Pillow** 声明执行环境。每个 Pillow 持有环境变量、工作目录和卷挂载，
可运行临时命令、shell 会话及可选的托管主进程；首个 Pillow 为默认环境。
同一 Bed 内的 Pillow 共享用户身份、网络和资源，不具有各自独立的容器隔离边界。

命令提供流式输出、取消和结构化退出结果；shell 会话跨命令保留 shell 状态；托管主进程
支持就绪检查和重启策略。文件 API 按所选 Pillow 的挂载视图读写、上传、下载和传输数据。

执行进程替换不会丢失 Bed 的工作数据。需要在驱逐后保留 managed volumes 时，应声明卷的
持久化策略并配置 S3 兼容存储。驱逐会删除 Hostel 管理的本地数据；通过 `host_path` 挂载的
外部目录仍由外部管理生命周期。详见 [Volume](docs/volumes.md)。

## 快速开始

在源码目录中执行，需要安装 Go 1.26+ 和 Make：

```bash
make build
./bin/hostel --beds-root ./.workspace --addr 127.0.0.1:8872
```

在另一个终端声明 Bed、运行命令，并下载生成的文件：

```bash
curl -fsS http://localhost:8872/v1/beds \
  -H 'Content-Type: application/json' \
  -d '{"id":"agent-1","pillows":[{"name":"agent","mounts":[{"volume":"workspace","path":"/workspace"}]}]}'

curl -fsSN http://localhost:8872/command \
  -H 'Content-Type: application/json' \
  -H 'X-Hostel-Bed: agent-1' \
  -d '{"command":"echo hi > hello.txt; cat hello.txt","cwd":"/workspace"}'

curl -fsS 'http://localhost:8872/files/download?path=/workspace/hello.txt' \
  -H 'X-Hostel-Bed: agent-1'
```

命令返回 SSE 事件流：`stdout` 应包含 `hi`，`execution_end` 结果中的 `process.kind`
应为 `exited`、`process.exit_code` 应为 `0`。最后的下载请求输出 `hi`。

创建 Bed 必须显式声明至少一个 Pillow；创建返回 HTTP 202 表示已接受初始化。
数据请求等待已声明 Bed 就绪，未声明返回 404。
后续命令和文件请求使用相同的 `X-Hostel-Bed`；省略 Bed 标识返回 400。
普通命令各自在新进程中执行，`/session` 用于创建常驻 shell。
命令中建议使用 `cwd` 和相对路径：文件 API 的路径归属于 bed，shell 文本中的绝对路径则由实际进程环境解释。

示例请求中的 `id: "agent-1"` 指定 Bed 的路由名称。管理响应以 `name` 返回该值，
另以 `id` 返回 Hostel 生成的本地生命周期身份。销毁后同名重建会改变 ID，保留本地数据的
重启恢复则沿用 ID。请求的 `id` 参数、Bed 路径和 `X-Hostel-Bed` 都使用 Name；
后续寻址应取响应的 `name`。详见[协议契约](docs/protocol.md)。

也可以在本地构建容器镜像：

```bash
make image
# 按实际需要的隔离功能配置部署权限。
docker run --rm -p 127.0.0.1:8872:8872 hostel:dev
```

镜像包含文件系统辅助工具和 Chromium；`make image-lean` 构建不含 Chromium 的版本。
实际隔离能力取决于宿主和容器权限。

## 能力边界与接入

通过 `/healthz` 和 `/v1/status` 查看实际能力。文件隔离、进程视图、网络与资源记账取决于
宿主条件；当前按 Bed 统计资源用量不代表施加了 CPU 或内存硬限额。共享网络可能保留
未执行的请求规则，状态会明确报告缺口；共享 Chromium/MCP 的出站使用宿主或外层容器的网络。Hostel 当前面向可信或半可信工作负载，API 应置于可信访问
边界内，并按工作负载需求选择具有相应隔离能力的宿主或外层容器。具体配置见[隔离能力](docs/isolation.md)。

快速开始使用 Hostel 原生 API。`/execd/...` 与 `/v1/beds/:bedId/execd/...` 提供 OpenSandbox
Execd adapter，当前不支持 code/context 端点。Go 调用方可引入独立的
`github.com/qiankunli/hostel/protocol` module 使用 Bed 管理数据类型。
寻址、兼容范围与结果契约见[对外协议](docs/protocol.md)。

- [Bed 生命周期](docs/bed.md)：声明、就绪与数据保留。
- [Pillow](docs/pillow.md)：执行环境、会话和托管主进程。
- [Volume](docs/volumes.md)：挂载、持久化与文件路径。
- [共享设施](docs/amenity.md)：Chromium 与 MCP 接入。
- [Kubernetes 示例](deploy/k8s/)：部署配置。

## 许可与致谢

Hostel 采用 [Apache-2.0](LICENSE) 许可证。资源与文件 API 参考
[OpenSandbox execd](https://github.com/alibaba/opensandbox)，命令执行使用 Hostel 自有协议。
归属说明见 [NOTICE](NOTICE)。容器镜像以独立 GPL-2.0 程序包含 PRoot，并在
`/usr/share/doc/proot/` 提供许可证、修改说明和对应修改源码。
