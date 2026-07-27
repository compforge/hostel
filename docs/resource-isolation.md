# bed 资源记账与隔离方案（per-bed cgroup）

> **状态：Phase 1 资源记账已落地；Phase 2 限额待实现。**

资源治理分两阶段：先建立可信的 **per-bed 记账**，再基于同一 cgroup 边界增加限额，避免一个
bed 跑飞后拖垮同一 hostel 的其它 bed。数据隔离见 `data-isolation.md`，持久化见
`persistence.md`。

## 一、理念

1. **多 bed 密度最终需要资源公平**：当前记账能定位"吵闹邻居"，但尚不会阻止一个 `while(1)` 或内存泄漏吃光 pod 配额；限额是下一阶段。cgroup 是两阶段共用的边界，避免先做一套观测、再为隔离推倒重来。
2. **复用内核原语，不发明配额器**：cgroup v2 已提供层级化的 cpu/memory/pids 控制，hostel 只做两件事——**给每个 bed 建一个子组、把 bed 的进程放进去**。不在用户态做任何"测量-驳回"式的自制限流。
3. **与 Isolator 正交**：隔离（namespace 视图）与资源治理（记账 / 限额）是两个维度——bwrap 不管 cgroup。当前 `Tracker` 与 `Isolator` 并列，未来 `Limiter` 延续同一边界；direct 模式（无 bwrap）同样可记账和加限额。

### 当前落地：只记账，不设限

hostel 启动时探测当前容器是否获得 cgroup v2 的 `cpu` / `memory` 委派。可用时为每个 bed
创建子 cgroup，并用 `CLONE_INTO_CGROUP` 让 bed-init（或非 Linux fallback 前的直接命令）
从第一条指令起落入该组；不写 `cpu.max`、`memory.max`、`pids.max`，所有 bed 仍共享 carrier
的大 cgroup 盘子。

OpenSandbox-compatible `GET /metrics` / `GET /metrics/watch` 读取目标 bed 的 `cpu.stat` 与
`memory.current`。cgroup 的累计 CPU 记账覆盖已退出的短命令，避免 `/proc` 周期扫描漏掉瞬时
进程；`healthz` / capabilities 的 `resource_accounting` 如实报告 cgroupv2 或 noop。非 Linux
或未委派环境保留 execd 的实例级 CPU/内存 fallback，仅用于协议兼容，不声称 per-bed 精确归因。

## 二、流程

```
hostel 启动 → 探测 cgroup v2 可写（当前容器 cgroup 能建子目录且
              cgroup.subtree_control 可开 cpu memory）
                ├─ 否 → Tracker=noop，capabilities 报 resource_accounting.available: false
                └─ 是 → 每个 bed 首次启动进程时：
                        1. mkdir <scope>/hostel-bed-<bedID>/
                        2. 启动进程时经 CLONE_INTO_CGROUP 直接落入该组
                           （Go: SysProcAttr{UseCgroupFD, CgroupFD}，Linux 5.7+）
                        3. Phase 1 只读 cpu.stat / memory.current
                        4. Phase 2 再写 cpu.max / memory.max / pids.max
bed 删除 / idle GC → 杀进程组 → rmdir cgroup 子目录
```

关键点：用 `CLONE_INTO_CGROUP` 而非"启动后写 `cgroup.procs`"——后者在 fork 与写入之间有窗口，短进程可能已退出，或已 fork 出逃离记账边界的子进程（未来也会逃出限额）。

## 三、关键设计

### 1. Tracker → Limiter

Phase 1 的 `Tracker` 只负责建组、读取累计用量和回收。Phase 2 在同一 package 增加以下限额
契约，不把策略塞进 bed 或 HTTP 层：

```go
type Limits struct {
    CPUMax    string // cgroup v2 cpu.max 语法，如 "50000 100000"（0.5 核）
    MemoryMax int64  // bytes；0 = 不限
    PidsMax   int64
}
type Limiter interface {
    Available() bool
    Prepare(bedID string, l Limits) (cgroupFD int, err error) // 建组+写限额，返回可 CLONE 的 fd
    Release(bedID string) error                               // rmdir
}
```

backend：`noop`（默认 / 非 Linux / 无写权限）· `cgroupv2`。与 `Store`、`Isolator` 同一模式：core 只依赖接口。

### 2. 限额来源：默认值 + 每 bed 覆盖

配置 `--bed-cpu-max` / `--bed-memory-max` / `--bed-pids-max` 给全局默认；`POST /v1/beds` body 可带 `limits` 覆盖（调用方按租户等级差异化）。**默认建议偏保守**（如 1 核 / 2GiB / 256 pids）：宁可让重任务显式申请，不让默认值放任吵闹邻居。

### 3. 前提：pod 内 cgroup v2 委派

容器里能否建子组取决于运行时把容器 cgroup 以何种权限挂给进程：
- K8s + cgroup v2 节点：容器内 `/sys/fs/cgroup` 通常挂 ro，需要 pod 配置（`securityContext` 或运行时支持）拿到自己 scope 的写权限；
- 拿不到 → `Available()==false` → noop 降级 + capabilities 如实上报（同 bwrap 缺失时的哲学：不假装隔离）。
- 部署侧要求写进 helm/values 注释，属部署契约而非代码逻辑。

### 4. managed-service 的位置

Chromium/Jupyter 等共享服务**不进任何 bed 的 cgroup**（它们是 per-hostel 单例），放独立的 `<scope>/services/<name>/` 子组单独限额。per-tenant（bed）粒度的用量归因是已知难点——浏览器进程模型不按租户划分——先接受服务级限额，租户级归因推后。

### 5. 测试策略

- **mac/CI 可跑**：metrics 契约、bed 选择、累计 CPU 解析和 noop 降级路径。
- **Linux 真验证**（devbox）：Phase 1 断言短进程计入目标 bed 且删除后 cgroup 回收；Phase 2
  再用 `stress` / fork 炸弹断言 CPU throttle、OOM kill、EAGAIN 和邻居延迟。

## 非目标

- 磁盘配额（io.max 管带宽不管容量；容量配额靠 overlay 上限或 fs quota，与持久化/overlay 一并考虑）；
- 网络带宽限速；
- 租户级（bed 级）managed-service 用量归因。
