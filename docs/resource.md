# Hostel 资源治理：采集、汇报、策略与隔离

> **状态：carrier 资源采集、资源汇报、容量准入与 per-bed 记账已落地；per-bed 硬隔离后置。**

Hostel 的资源治理分成四层：**采集事实 → 汇报事实 → 执行策略 → 内核硬隔离**。前三层已经可以帮助 Hostel 判断自己
是否“客满”；最后一层解决单个 bed 跑飞和邻居公平问题，暂不阻塞高密度承载能力。

数据治理见 `data.md`，持久化见 `store.md`。

## 一、理念与概念

### 1. 事实、投影、策略、执行分层

- **采集**回答“现在有多少资源、用了多少”，来源必须是运行时事实。
- **汇报**把同一份事实投影给 healthz、inventory、capabilities 和 metrics，供人或上游调度器观察。
- **策略**回答“是否继续接纳新负载”，当前包括数量安全阀和 carrier 资源水位。
- **隔离**回答“已经接纳的负载最多能用多少”，应由 cgroup 等内核原语强制执行，而不是用户态轮询。

这四层不能混为一谈：采集到高水位不等于已经隔离资源；拒绝新 bed 也不能保护现有 bed 免受
吵闹邻居影响。反过来，per-bed 硬限额尚未实现，也不妨碍 Hostel 先依据 carrier 总体资源做准入。

### 2. carrier 与 bed 是两个资源粒度

- **carrier 粒度**：Hostel 所在容器的总盘子，包含 daemon、amenity、default bed 和所有 tenant
  bed。容量准入看这一层，因为这些消耗最终共享同一个 CPU/内存上限。
- **bed 粒度**：每个 bed 的用量归因和未来硬限额。它适合解释谁消耗了资源、保护邻居，但不能
  代替 carrier 总量判断。

Hostel 进程读取的是自身**容器 cgroup**，不把它泛化成整个 Pod：单容器 Pod 中二者近似等价；
存在 sidecar 时，Hostel 只对自己所在容器的容量负责。

### 3. 配额来自 cgroup，不依赖 Kubernetes env

Kubernetes 的 container limit 最终由 runtime 落到 cgroup。Hostel 通过 `/proc/self/cgroup` 定位
carrier cgroup，再读取 `cpu.max`、`memory.max` 等文件；不要求 Pod 通过 Downward API 把
request / limit 注入 env，也不访问 Kubernetes API。

容量准入使用 **limit** 作为分母。request 是调度与保障语义，不是容器的硬上限，不参与当前准入。
某个 cgroup 维度为 `max` 时，该维度没有有限分母，Hostel 不伪造使用占比。

## 二、当前流程

### 1. carrier 资源采集

`internal/resource.Carrier` 是只读采集边界，不要求 cgroup 子树委派：

| 事实 | cgroup v2 来源 | 语义 |
|---|---|---|
| CPU 上限 | `cpu.max` | `quota / period`；`max` 表示不限 |
| CPU 用量 | `cpu.stat: usage_usec` | 累计 CPU 时间，不是瞬时百分比 |
| 内存上限 | `memory.max` | 字节数；`max` 表示不限 |
| 内存用量 | `memory.current` | 读取时刻的当前 charge，包含可回收 cache |

CPU 利用率必须用两个时点的累计值计算：

```text
CPU usage ratio = Δusage_usec / Δwall_time / cpu_limit_cores
```

内存本身可直接读取当前值；实现上与 CPU 一起周期性缓存，是为了让 admission 请求路径只读内存
中的 verdict，不在 Bed Manager 的生命周期锁内访问 cgroupfs。这里的内存口径比 Kubernetes
working set 更保守，更贴近 cgroup OOM 边界，适合“还能不能接客”的判断。

### 2. 资源汇报

同一份资源事实按受众投影，不要求调用方自己拼装结论：

- `resource_admission`：在 `/healthz`、`GET /v1/beds` 和 capabilities 中报告有限 cgroup 配额、
  最新 CPU/内存占比、配置水位、采样状态、reason 和 `accepting` verdict。
- `resource_accounting`：报告 per-bed 精确记账是否可用及 backend；它与 carrier admission 的
  available 状态相互独立。
- `/metrics` / `/metrics/watch`：按目标 bed 返回其累计 CPU 与当前内存；未获得 per-bed cgroup
  能力时保留协议兼容 fallback，但不声称精确归因。

`internal/resource` 只提供资源领域事实和 verdict；JSON shape 仍由 `internal/web` 负责，Bed Manager
只消费 `Admitter.Check()`，不依赖 cgroup 文件或 HTTP 类型。

### 3. 当前准入策略

```text
新 resident / dormant restore，或未 pinned 的 idle bed 准备进入 active
  → pinned 接近 max-pinned-beds → inventory 上报 BED_PRESSURE
  → max-pinned-beds 硬上限检查
      └─ 数量已满 → 429 INSUFFICIENT_BED（携带容量快照）
  → 读取缓存的 carrier resource verdict
      ├─ CPU 或内存达到配置水位 → 429 RESOURCE_PRESSURE
      ├─ 未达到                    → 接纳
      └─ 不可测                    → fail-open，由数量上限兜底
```

`pinned` 是复合容量事实，不是新的 bed state：`inflight > 0`，或 durable store 下
`data_synced=false`，任一成立即 pinned；noop 表示调用方不要求数据完整性，因此 operation 结束即可解除 pinned。
数量限制简单可预测，资源水位更接近真实成本；两者都是“停止接新归属”的信号，不是已归属 bed 的硬执行上限。两者组合后：

- durable store 下，operation 结束只触发同步诉求；数据到达 store 后才释放 pinned 名额。同步节奏由 Store 控制，不由请求路径直接上传。
- noop 下没有待完成的远端同步步骤，瞬时 operation 结束即释放 pinned 名额。
- pinned bed 的后续 operation 在承诺范围内，不做资源准入；未 pinned 的 idle bed 可重新调度。
- `BED_PRESSURE` 是提前扩容和调度避让的软信号；剩余 pinned 容量保留给已有 source carrier
  的兜底承接。达到硬上限后，`INSUFFICIENT_BED` 才表示本次 activation 无法准入。
- 已接纳的 bed 不会因采样越线被暂停或杀死；default bed 也不参与资源准入。
- 采集失败、无有限 limit 或非 Linux 环境均诚实上报 unavailable，并 fail-open 到数量策略。

`--admission-cpu-threshold` 与 `--admission-memory-threshold` 只表达策略水位；具体默认值和当前字段
shape 以配置代码及 README 为准，避免设计文档随调参漂移。

## 三、关键设计与后续边界

### 1. per-bed 记账已落地，但不等于隔离

获得 cgroup v2 子树委派时，`Tracker` 为每个 bed 建立子 cgroup，并用 `CLONE_INTO_CGROUP` 让
bed-init 或 local Executor 的直接命令从第一条指令起进入目标组。这样 `cpu.stat` 能累计已经退出的短命令，避免
`/proc` 扫描漏记，也为未来硬限额复用同一资源边界。

使用 `CLONE_INTO_CGROUP` 而不是启动后再写 `cgroup.procs`，是为了消除 fork 与迁移之间的窗口：
短进程可能在迁移前退出，也可能先 fork 出逃离记账与限额边界的子进程。

### 2. per-bed 硬隔离暂缓

当前不会向 bed 子组写 `cpu.max`、`memory.max` 或 `pids.max`。这部分先放在后续阶段，原因是需要
先明确限额来源、默认公平模型、不同 workload 档位以及 amenity 如何单独预算；过早固定一个统一
per-bed 配额，容易把高密度、强突发的 agent workload 错配成传统常驻服务。

后续实现仍应遵循以下边界：

- cgroup 负责硬限制，用户态不靠采样循环暂停、杀死或节流已经接纳的进程。
- carrier admission 与 per-bed limit 正交：前者决定是否接新客，后者保护已经入住的邻居。
- per-bed 默认值与调用方覆盖属于配置/API 契约，确定需求后再落，不提前固化 speculative schema。
- Chromium/Jupyter 等共享 amenity 不归入任一 bed；需要时进入独立 service cgroup，先做服务级预算，
  不虚构无法可信归因的 tenant 用量。

### 3. cgroup 能力分两级诚实降级

- **只读 carrier cgroup**：普通 Kubernetes 容器通常即可使用，足以支持资源采集、汇报和准入。
- **可写子树委派**：per-bed 记账与未来硬隔离需要创建子组、开启 controller；拿不到时 Tracker
  降级为 noop，但 carrier admission 仍可工作。

因此 `resource_admission.available` 与 `resource_accounting.available` 必须分别上报，不能用一个
布尔值概括全部资源能力。

### 4. 演进方向与测试

当前 CPU 使用短窗口利用率，内存使用 `memory.current / memory.max`。后续若真实负载表明单阈值
抖动或误判，再引入 headroom、hysteresis、CPU throttling、PSI 或基于历史 per-bed 成本的预测；
在有证据前保持策略简单，不把 admission 演化成用户态调度器。

测试分层：

- mac/CI：cgroup 文本解析、CPU 窗口计算、memory verdict、不可用 fail-open、Bed admission 与 HTTP
  背压契约。
- Linux 容器：验证 carrier 父 cgroup 聚合 daemon + bed 子组、真实 limit/usage 上报，以及 CPU/内存
  压力达到水位后拒绝新的 carrier 归属。
- per-bed 硬隔离落地时，再补 CPU throttle、OOM、pids exhaustion 与邻居延迟验证。

非目标仍包括磁盘容量配额、网络带宽限制，以及共享 amenity 的虚假 tenant 级资源归因。
