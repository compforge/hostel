# hostel

[English](README.md) | 简体中文

**Hostel 是面向 AI Agent 的 sandbox runtime。** 一个 daemon 在笔记本、VM、CI runner
或容器内管理多个 sandbox，每个称为一张 **bed**。每张 bed 提供工作区，用于运行命令、
shell 会话和托管服务。

多张 bed 共享机器或容器，减少为每个任务单独创建运行环境的需要。Hostel 负责执行和本地资源管理；
实例调度、跨实例路由和 API 访问授权由上层控制面负责。

## 核心能力

- 执行命令，提供流式输出、取消和结构化退出结果；需要跨命令保留 shell 状态时使用会话。
- 读写工作区文件，上传、下载和传输文件。
- 声明常驻服务，管理启动、就绪检查和重启策略。
- 将指定路径持久化到 S3 兼容存储，在重新创建 bed 时恢复。
- 共享 Chromium 和远端 MCP 连接，按 bed 管理应用状态。

执行进程替换不会丢失 bed 的工作区。驱逐后仍需保留数据时，必须配置持久化 Store；
未配置时，驱逐会删除本地数据。

## 快速开始

在源码目录中执行，需要安装 Go 和 Make：

```bash
make build
./bin/hostel --isolation dorm --workspace-root ./.workspace --addr 127.0.0.1:8872
```

在另一个终端运行命令，并下载生成的文件：

```bash
curl -fsSN http://localhost:8872/command \
  -H 'Content-Type: application/json' \
  -H 'X-Hostel-Bed: agent-1' \
  -d '{"command":"echo hi > hello.txt; cat hello.txt","cwd":"/workspace"}'

curl -fsS 'http://localhost:8872/files/download?path=/workspace/hello.txt' \
  -H 'X-Hostel-Bed: agent-1'
```

首次执行会按需创建 bed。后续命令和文件请求使用相同的 `X-Hostel-Bed`；省略时使用 `default` bed。
普通命令各自在新进程中执行，`/session` 用于创建常驻 shell。
命令中建议使用 `cwd` 和相对路径：文件 API 的路径归属于 bed，shell 文本中的绝对路径则由实际进程环境解释。

也可以在本地构建容器镜像：

```bash
make image
# 按实际需要的隔离功能配置部署权限。
docker run --rm -p 127.0.0.1:8872:8872 hostel:dev
```

镜像包含文件系统辅助工具和 Chromium；`make image-lean` 构建不含 Chromium 的版本。
实际隔离能力取决于宿主和容器权限。

## 隔离与访问边界

`--isolation` 设置实例级房型预期：

| 房型 | 期望的边界 |
|---|---|
| `dorm` | 共享文件访问、身份和网络 |
| `room` | 限制跨 bed 文件访问、独立 bed 身份、共享网络 |
| `suite` | 私有文件视图、独立 bed 身份和独立 bed 网络 |
| `auto` | 期望 suite，允许降级到环境支持的机制 |

通过 `/healthz` 和 `/v1/status` 查看实际能力。房型受环境能力约束；共享 Chromium/MCP
的出站仍使用 carrier 网络，按 bed 统计资源用量也不代表施加了 CPU 或内存硬限额。
API 应置于可信访问边界内。执行恶意或不可信工作负载时，应使用具有相应隔离能力的 carrier，
例如独占 VM 或 microVM。详见[隔离设计](docs/isolation.md)和[部署权限](docs/privilege.md)。

## 深入使用

通过 `./bin/hostel --help` 查看参数。Daemon 也支持 `HOSTEL_*` 环境变量，
详见[启动配置](docs/configuration.md)。

| 需要做什么 | 文档 |
|---|---|
| 管理 bed、就绪状态与保活 | [运行模型与生命周期](docs/kernel.md) |
| 配置命令和服务的执行环境 | [Bed 配置](docs/bed-configuration.md) |
| 运行托管服务 | [Bed Service](docs/bed-service.md) |
| 理解路径与文件访问 | [文件系统](docs/filesystem.md) |
| 保存和恢复工作区，与 S3 传输文件 | [Store](docs/store.md)、[文件传输](docs/transfers.md) |
| 使用浏览器动作、CDP 或远端 MCP 工具 | [共享设施](docs/amenity.md)、[MCP](docs/mcp.md) |
| 配置网络和容量 | [网络](docs/network.md)、[资源管理](docs/resource.md) |
| 查看日志、Trace 与运行状态 | [可观测性](docs/observability.md) |
| 验证二进制或容器镜像 | [端到端测试](tests/e2e/README.md) |
| 了解尚未完成的能力 | [Backlog](docs/backlog.md) |

## 许可与致谢

Hostel 采用 [Apache-2.0](LICENSE) 许可证。资源与文件 API 参考
[OpenSandbox execd](https://github.com/alibaba/opensandbox)，命令执行使用 Hostel 自有协议。
归属说明见 [NOTICE](NOTICE)。容器镜像以独立 GPL-2.0 程序包含 PRoot，并在
`/usr/share/doc/proot/` 提供许可证、修改说明和对应修改源码。
