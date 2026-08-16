# Hostel Backlog

Hostel 当前未交付的演进项：

- 持久 namespace Executor + PID 1
- per-bed cgroup 硬限额（见 `resource.md`）
- 与数据隔离正交的 per-bed 网络隔离 / egress policy（覆盖命令与 amenity）
- bwrap 安全纵深（seccomp memfd / 真 setuid）
- overlay CoW（临时层）
- PTY WebSocket
- Jupyter amenity 实例
- 交互动作全集
- 上层调度系统对接
- 产品化外壳（API 版本化、独立发布）
