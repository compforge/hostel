# Hostel Backlog

Hostel 当前未交付的演进项与待修复项（隔离目标及组合约束见 [isolation.md](isolation.md)）：

- CDP 代理：完整指令与 target/session 授权校验、Playwright 实际流量覆盖；当前有限过滤不承诺对抗性隔离（见 [amenity.md](amenity.md)）。

- 持久 namespace Executor + PID 1
- per-bed cgroup 硬限额（见 `resource.md`）
- per-bed egress policy 注入 API 与凭据代理（见 `network.md`）
- Chromium：创建各 Bed 的 BrowserContext 时指定 `proxyServer`，代理按 Bed 执行出站策略；验证 bypass、QUIC/WebRTC 等路径后再声明浏览器网络治理能力（见 `network.md`）
- bwrap 安全纵深（seccomp memfd / 真 setuid）
- overlay CoW（临时层）
- PTY WebSocket
- Jupyter amenity 实例
- 交互动作全集
- 上层调度系统对接
- 产品化外壳（API 版本化、独立发布）
