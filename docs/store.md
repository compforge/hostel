# Store：bed 持久化与恢复

Store 是 Hostel 直管的组件，统一负责各个 bed workspace 的持久化与 Stage-in。bed 的
workspace 是本地目录，pod 重启 / 换 pod 即丢；Store 把本地目录作为工作副本，把 durable
snapshot 作为跨进程 / pod 的持久身份。上层调度系统只消费 Hostel 上报的同步与恢复成本事实，
不直接驱动 Store。数据治理见 `data.md`，资源治理见 `resource.md`。

## 一、理念

1. **Hostel 直管**：initialization、operation、pressure 等入口只向 Store 提交同步诉求；Store 自己掌握合并、串行、周期和重试节奏，并在 bed lifecycle 边界完成 Persist / Stage-in。
2. **持久身份 + 可弃计算**：bed 的持久身份是对象存储里的一份快照（`s3://bucket/<prefix>/<bedID>/`），本地 workspace 只是它的**工作副本**。计算（pod、hostel 进程、bed 内进程）随时可弃，数据不随之陪葬。
3. **为什么不是共享文件系统**：直觉方案是把 workspace 直接放 NFS/共享盘。两个障碍——**内核 overlayfs 的 upper 不能放网络 FS**（不支持 whiteout/xattr，未来上 overlay CoW 就堵死）；且共享 FS 的每次读写都付网络往返，而 bed 活着时的读写是热路径。**本地目录 + 边界同步**把网络成本从"每次 IO"移到"生命周期边界"。
4. **文件粒度快照，比 microVM 便宜一个量级**：这即 OSEP-0013 Phase 2（diff/commit/persist，OpenSandbox 自己未实现）的更简单实现——同步的是普通目录，不是 overlay upper，也不是内存镜像。

## 二、流程

```
InitializeBed(bedID)       ──→ 立即返回 initializing，后台 StageInBedFS
StageInBedFS               ──→ store.Stat(bedID)?
                                ├─ 有快照 → 本地 luggage 的 generation ≥ 快照的？
                                │            ├─ 是 → warm start（免下载，直接用现场）
                                │            └─ 否 → 旁路目录 Restore，完整后原子替换现场
                                └─ 无快照 → 空 workspace（或 noop 下的遗留现场）
prepare + publish          ──→ readiness=true，Bed 进入 resident map
bed 活着                  ──→ 本地读写，零网络往返；operation/pressure 只提交同步 trigger
Store 同步循环            ──→ 合并 trigger + 自有周期/重试 → 静默 bed → Persist 到 S3
delete / checkpoint       ──→ 回收边界或显式请求直接等待 Persist
evict 完成                ──→ 本地目录留作 luggage（现场缓存），交磁盘水位 GC 管
```

接入点（锚点）：`bed.Manager.InitializeBed`（异步初始化）、`store.StageInBedFS`（数据准备）、`Ensure`（原生 API 等待同一初始化）、Evict / idle GC（persist）、`POST /v1/beds/:id/checkpoint`（显式持久化）；capabilities 报 `persistence: noop|auto|s3|pack|tar`。

## 三、关键设计

### 1. Store 抽象（与 Isolator 同构，core store-agnostic）

```go
type Store interface {
    Stat(bedID string) (*SnapshotInfo, error)      // nil=无快照；含 generation/bytes，S3 上是 HEAD，免下载
    Restore(bedID, dir string) error               // create/resume 时，放行前拉下来
    Persist(bedID, dir string, generation int64) error // idle/delete/checkpoint 时，推上去
}
```

`Store` backend 只负责远端事实与传输；`StageInBedFS` 负责本地发布语义。需要 Restore 时，它先写入
bed 目录旁的 staging 目录，成功后才用 rename 替换 stale luggage；失败则清理 staging 并保留原
luggage。Bed manager 只在 Stage-in、BedFS/isolation 准备全部成功后发布 resident，因此远端数据
故障既不会得到一个空 Bed，也不会得到一个半恢复目录。

可选 backend：

- `auto`：默认值，只负责选择和兼容，不实现存储格式。
- `noop`：laptop 零依赖模式。
- `s3` / `cas`：CAS 内容寻址增量。
- `pack`：packfile 增量。
- `tar`：全量 tar.gz。

AWS、MinIO、火山 TOS、Ceph 等 S3 兼容 API 共用以下配置：

- `--s3-bucket`、`--s3-prefix`、`--s3-endpoint`：对象存储位置。
- `--s3-path-style`：bucket addressing 默认 virtual-hosted，仅在 endpoint 要求时开启 path style。
- AWS SDK 标准环境链：提供凭据。
- `--store-auto-pack-file-threshold`：默认 100，设为 0 关闭既有 CAS 自动切换。
- `--persist-interval`：控制周期同步兜底。

`auto` 的路由规则：

- 未配置 bucket 时返回 `noop`。
- 配置 bucket 后，并行 HEAD 三种布局的提交点，选择 generation 最高者；多个布局处在相同最高 generation 时拒绝猜测并报错。
- 没有远端快照的新 bed 默认使用 pack。
- 已有 CAS / pack / tar bed 继续使用原布局，以兼容老客户。
- 当前 CAS bed 的可持久化非目录条目数超过阈值时，下一代快照写成 pack，之后不因文件数下降而回退。
- CAS → pack 先发布更高 generation 的 pack 提交点，旧 CAS 对象保留到显式 purge，避免后台策略切换隐式删除持久数据。
- purge 清理该 bed 的全部三种布局。
- 显式 `s3` / `pack` / `tar` 只操作指定布局，不探测或迁移其它布局。

### 2. persist 触发：入口表达诉求，Store 掌握节奏

initialization、operation 开闭、session 流量和 carrier pressure 都只向同步循环提交“尽快同步”的 trigger；trigger
可合并，不在请求路径直接上传。同步循环统一串行处理、按 `--persist-interval` 做周期兜底，并对失败
退避重试。evict 与显式 checkpoint 是必须得到结果的生命周期边界，仍同步等待 Persist。这样入口可以
不断表达诉求，而 Store 保留自己的并发、节奏和重试权。

### 3. 粒度：CAS、packfile 与全量 tar

三种 S3 布局都保持“一张 bed 一个原子提交点”，取舍点是增量粒度与远端对象数量：

| backend | 远端单位 | 优点 | 代价 |
|---|---|---|---|
| `s3` / `cas` | 一个 chunk 一个对象 | 小改动只上传受影响 chunk；在线 GC 简单且完整 | chunk 数就是对象数 / MinIO inode 数；请求数也高 |
| `pack` | 多个压缩 chunk 合成约 32 MiB pack | checkpoint 的对象数和请求数显著下降，更适合 inode 紧张的 MinIO | 小改动至少新增一个 pack；当前无在线 compaction/prune，历史对象追加保留到 purge |
| `tar` | 一张 bed 固定一个 tar.gz | 永远只有一个远端对象；实现、恢复和 purge 最直接 | 内容是否变化都要全量打包、上传和下载；无增量复用 |

#### CAS 布局

s3 backend 的布局：复用 **desync 库**（casync 的 Go 实现，BSD-3；catar 序列化 + CDC 滚动哈希切块 + 并发装配都是现成的，hostel 只写对象 IO 适配和编排）。bed 目录序列化成 catar 流 → CDC 切块（64K/256K/1M）→ **只上传上代快照没有的块**（上代 index 就是"已在库"清单，未变数据零请求）→ index 对象作为提交点（一次小 PUT 原子发布整份快照，携带 generation）。内容没变时 catar 流稳定 → 块序列相同 → **块上传 no-op**，仍会用一次小 index PUT 推进 generation，保证跨 carrier 的 luggage 新鲜度判定正确。**小文件海**（node_modules，per-object sync 的经典死穴）不是问题：切块作用在 catar 流上，与文件数解耦。

```text
<prefix>/
└── <bedID>/
    ├── index.caibx                     # 提交点：chunk 顺序 + generation / bytes metadata
    └── chunks/
        ├── <chunkID>                   # 一个 zstd chunk 一个对象
        └── ...
```

**cas 的 blob 空间按 bed 隔离**（`<prefix>/<bedID>/`，index 为 `index.caibx`，数据块在 `chunks/`）：不做跨 bed 去重，换来 GC 只是"提交后删掉 index 不引用的块"的本地 diff——其正确性只依赖上层调度的单写者保证，不需要跨 manifest/跨实例的分布式清扫（restic/kopia 都只能靠显式加锁的离线 prune 解这个问题）。跨 bed 重复的大头（模板/基础工作区）留给将来的共享 base 快照，不靠 blob 级全局去重。GC 失败不算 persist 失败（快照已提交，孤儿块由下次 persist 清扫；崩溃的 persist 留下的孤儿块同理）。

#### Packfile 布局

`pack` 仍用 desync 生成稳定 chunk ID，但把本次新出现的压缩 chunk 顺序装入目标约 32 MiB 的 immutable pack；上代 manifest 已引用的 chunk 不重传。对象树不带版本目录：

```text
<prefix>/
└── beds/
    └── <bedID>/
        ├── head.json                    # 唯一提交点：generation / snapshot / bytes
        ├── snapshots/
        │   └── <snapshotID>.json        # immutable：caibx index + chunk → pack/offset/length
        └── packs/
            └── <first2>/
                └── <packID>.pack        # immutable：多个 zstd chunk 的拼接
```

Persist 顺序固定为 **pack → snapshot manifest → head**，所以读者只会看到完整旧快照或完整新快照；pack 和 manifest 都按内容摘要校验，取出 chunk 后仍由 desync 复核解压内容的 chunk ID。内容完全不变时只 PUT 一次 `head.json` 推进 generation；小改动通常是一个新 pack + manifest + head，而不是数十到数百个 chunk PUT。

当前 pack / manifest 按 bed 隔离并追加保留，只有显式 purge 删除整个 bed 前缀；还没有在线 retention、compaction 或 prune。这把对象增长从“workspace chunk 数”降为“发生内容变化的 checkpoint pack 数”，但高频长寿 bed 仍会积累，后续需要基于保留策略做离线 compaction。也因此当前不宣称零拷贝 fork：Store 还没有一等 Snapshot/ref，若先把 pack 全局共享，purge 会变成有竞态的跨 bed GC。fork 共享应与 Snapshot/ref/lease 模型一起引入。

#### 全量 tar 布局

`tar` 不做 CDC 或跨 checkpoint 复用：每次 Persist 都重新遍历 bed 目录，排除顶层 `*.local` 与 `data/tmp/`，生成完整 tar.gz 到临时文件，然后用一次 PUT 原子替换固定 key。即使内容没有变化，也会完成一次全量打包和上传；相应地，一张 bed 始终只有一个远端对象，不产生历史 manifest、chunk 或 pack。

```text
<prefix>/
└── tar/
    └── <bedID>/
        └── snapshot.tar.gz             # 提交点：完整快照 + generation metadata
```

Restore 下载完整对象，在 `os.Root` 约束下解包；绝对路径、`..` 逃逸、越界 symlink、重复路径和特殊文件会直接失败，普通文件 / 目录 / 相对 symlink 的普通权限位与 mtime 按归档恢复，不恢复 owner、setuid 或 setgid。`tar` 适合 workspace 较小、checkpoint 较少，或者对象数比传输字节更敏感的场景；大 workspace 高频同步应使用 CAS 或 pack。

### 4. 一致性：静默后快照

活着的 bed 边写边传会拿到撕裂的快照。后台同步只在发起时选择**无 operation** 的 dirty bed；session
可以长期存在而不产生写入，不能仅因连接仍在就永久阻塞持久化。快照准备时记录 activity watermark，
session 流量或新 operation 若在上传期间发生，仍会让 bed 保持 dirty/pinned，由下一轮同步覆盖，不能被
本轮成功上传误标为已同步。

### 5. 单写者：generation 冲突探测 + 上层调度系统权威

两个 hostel（不同 pod）同时 resume 同一 bedID → persist 互相覆盖（last-writer-wins，**静默丢数据**）。"一个 bedID 同时只在一个 hostel 活着"的**权威保证属于上层调度系统**（对 bed 归属做类 RWO 独占），hostel 不硬解分布式锁——但静默覆盖的失败模式太重，hostel 侧留一道**冲突探测器**兜底：s3 `Persist` 在 PUT 前 HEAD 一次，若远端 generation ≥ 本次要写的（说明本实例初始化之后有别的实例 persist 过），返回 `store.ErrConflict` 拒绝覆盖——**first-writer-wins + 响亮报错**替代静默丢失。这是探测不是原子 CAS（HEAD→PUT 之间仍有窗口），但真实双活持续秒到分钟级，实践上抓得住；收成真 CAS 要等条件写（`If-Match`）在目标 S3 兼容存储（MinIO/TOS）上确认可用。

## 四、bed 生命周期与流转

bed 在单个 hostel 里是**瞬时的**（可驱逐、可恢复），因此需要显式生命周期，而不是"在 map 里/不在 map 里"的隐式状态。

### 状态

```
   ABSENT / DORMANT ── InitializeBed ──→ INITIALIZING ── Ready ──→ IDLE ←──┐
                                             └─ error ──→ FAILED             │
                                                                    BeginOperation
                                                                          ▼ │
                                                                       ACTIVE

   IDLE ── retained_until 到期 / 显式驱逐 ──→ EVICTING
     ▲                                     │       │
     └──── 新 operation 取消驱逐 ───────────┘       │ persist 成功
                                                   ▼
                                                LUGGAGE
                                                   │ InitializeBed
                                                   └────────→ INITIALIZING
```

`state` 只表达当前操作态，四个值互斥：

- **ACTIVE**：至少一个 Bed operation 正在执行。operation 包括 Exec、文件、浏览器/CDP、checkpoint 等所有会使用 Bed runtime 或数据的动作。
- **IDLE**：Bed 仍 resident、占 `max-beds` 名额，但没有 operation。
- **EVICTING**：正在 persist 和释放 runtime。期间新 operation 优先获得服务权并取消驱逐；最终移除与 operation 准入使用同一锁序，二者只能有一个获胜。
- **LUGGAGE**：不再占 runtime 名额，只保留本机数据副本。

`phase/readiness` 表达 Bed 是否可服务；`state` 只表达 resident Bed 的操作态。`generation`（数据版本）和 `retained_until`（最早安全回收期限）与二者正交。Stage-in 的等待边界通过 readiness reason 暴露（例如 `InspectingSnapshot` / `RestoringSnapshot`），失败保留为 `failed`，不会伪装成空的 idle Bed。

### 动词与 API 语义

| 动作 | 语义 | API |
|---|---|---|
| **evict**（驱逐） | 释放计算、保留身份：persist → 出 map → 名额释放 | idle GC 自动；`DELETE /v1/beds/:id`（默认） |
| **purge**（清除） | 身份终结：驱逐 + 删除 S3 快照 | `DELETE /v1/beds/:id?purge=true` |
| **checkpoint** | 作为 operation 打快照，完成后回到 IDLE | `POST /v1/beds/:id/checkpoint` |
| **initialize** | ABSENT/DORMANT/LUGGAGE → INITIALIZING → RESIDENT | `POST /v1/beds`；原生数据面 `Ensure` 会加入并等待 |

`GET /v1/beds` 给出调度器要的本机全图：实例容量 + 全部本机 Bed（ACTIVE/IDLE/EVICTING resident + DORMANT luggage）；DORMANT 集合的权威仍是对象存储和上层调度系统。

### luggage：现场缓存与 generation

**共享 store 模式下，快照是唯一事实，其余一切都是缓存。** evict 不再删本地目录——它留下来成为 **luggage**（寄存行李）：DORMANT bed 的本机热副本。同机 resume 时若现场足够新就直接用（warm start，免下载）；判"够新"用 **generation**——meta.json 里单调递增的 persist 计数，随快照进对象元数据（`Stat` 一次 HEAD 就能比对）。现场落后于快照（bed 期间在别的实例跑过）则整目录丢弃后重新 Restore，**只换不合**。为什么不用时间戳判序：bed 跨机迁移时钟有偏差，序会反转；时间戳只做观测（`last_persisted_at` / `last_active_at`），判序只认 generation。

共享 store 模式下 luggage 是纯缓存，删错只会多付一次 Restore，所以磁盘上限走独立水位而不占 max-beds：超过 `--luggage-high-bytes` 时按"generation 过期优先（纯垃圾）→ LRU"的顺序删到 `--luggage-low-bytes` 以下。这个排序是 cost-aware 驱逐的演化缝，v1 只认新旧。

`GET /v1/beds` 把容量、`phase_counts`、`activity_counts` 和每个本机 Bed 的 `status/generation/retained_until` 一次给上层调度器。上游据此优先命中 resident Bed，其次选择最高 generation 的 luggage；回收 carrier 前则确认不存在 resident Bed。inventory 是一次事实快照，不代替上游的单写者约束：同 bedID 双活仍由调度器归属 + store 侧 generation 冲突探测兜底。

### 恢复成本与后续增量 Restore

调度器判断数据亲和时需要的是“在这个 carrier 上把 bed 准备好还要搬多少数据”，不是 generation
相差多少。Hostel 因此在 inventory 中同时上报本地 generation、最近观测到的 durable generation、
快照大小、本地目录大小和预计 Restore 字节数；resident 目录大小与 durable snapshot 事实由
initialization / Store 同步循环在自己的节奏里刷新，`GET /v1/beds` 不为它们扫描 resident 目录或访问
S3。dormant luggage 仍沿用 inventory 的本地目录扫描，后续可随 luggage 索引一起缓存。

当前 Restore 仍是完整快照恢复：本地副本与 durable generation 一致时预计恢复量为 0；缺少本地副本
或本地副本过期时，预计恢复量就是完整 `snapshot_bytes`。generation 只判断相等与新旧，不能作为版本
距离或增量字节数使用。

后续可在快照中增加目录 / 文件级 hash manifest，再由 Restore 对比本地与远端 manifest，按文件执行
create / update / delete / keep。只有完成这套文件粒度对账后，才把 `restore_bytes` 收敛为真正的增量
传输估算；在此之前不根据 generation 差值猜测增量，避免遗漏删除、重命名和内容回退。

### noop store 下的退化语义

没有快照，luggage 就是唯一副本：evict 后同机 resume 仍然有效（比 v1 的"evict 即销毁"更好），但 luggage GC 删掉它 = 数据销毁，且 bed 不可跨实例迁移（inventory 的 `store: "noop"` 明示这一点）。部署要么接受 bed 数据只活在本机，要么开 s3。healthz 的 `persistence` 字段让调用方能区分这两种世界。

### bed 目录分层（配套）

```
{workspace-root}/{bedID}/        ← 快照打包的根（meta + data 一起上 S3）；evict 后整体留作 luggage
  meta.json   # hostel 私有：created_at、last_persisted_at、generation、last_active_at（将来：manifest、lease）
  *.local     # 约定：本机私有元数据，不进快照（当前无，留位）
  data/       # bed_home：默认进快照
    tmp/      # bed_home 的 /tmp；显式排除，不跨 carrier 恢复
```

**快照内容 = meta（可移植部分）+ bed_home（排除 `/tmp`）**：DORMANT 的唯一存在形式是快照，元数据若只留本地，驱逐即丢、换一台 hostel 复活就残缺。约定"默认可移植"——meta.json 和 `data/` 的其余内容一起打包；`data/tmp/` 是 bed_home 根下唯一内置的临时边界，不进 S3。确属本机私有的状态用 bed 目录顶层 `*.local` 后缀排除在打包之外。

meta 对 bed 内代码**不可见**（bwrap 只 bind `data/`，root 整体被 tmpfs 遮蔽）——沙箱代码不能篡改 hostel 的记账。`last_persisted_at` 落盘使 dirty 追踪跨进程重启仍正确。

## 诚实边界

- **边界同步 ≠ 实时共享 FS**：两个 pod 不能同时 live 读写同一个 bed；要那个语义就得回共享 FS 路线（并放弃 overlay 演进）。对"一 conv 一 bed、之后可能换 pod 恢复"的模型，边界同步正好且简单。
- **崩溃丢 last-sync 之后的改动**：窗口靠生命周期 trigger + 周期兜底压小，非零。要零丢失只能实时 FS，另一套复杂度。

## 实现状态

已实现（`internal/store/` + `bed.Manager` 生命周期钩子）：

- `Store` 接口 + 独立 `noop` / `s3` / `pack` / `tar` 实现；`router.go` 只负责配置选择、按 bed 识别提交点和既有 CAS → pack 单向切换，S3 client 由三种远端布局复用
- 异步 initialization：`POST /v1/beds` 快速返回 phase/readiness；`Ensure` 加入同一 singleflight 并等待。Stage-in 失败保留原因且拒绝发布 resident——静默空启动等于数据丢失
- 原子 Stage-in：快照恢复到 sibling staging 目录，完整后才替换 stale luggage；下载失败保留可用现场；**persist 失败中止 Evict**（毁掉唯一副本比留着 bed 重试更糟）、`POST /v1/beds/:id/checkpoint`
- Store 同步循环：合并 lifecycle/pressure trigger，自主串行、周期兜底与失败退避；只传静默 dirty bed，并以 snapshot activity watermark 提交同步水位
- **Bed 生命周期**（§四）：`BeginOperation` 统一 Exec/文件/浏览器/checkpoint 活跃度；`phase`、`readiness`、派生的 `activity: active|idle` 与 generation/expiry 正交；`Evict` 不杀 active operation，evicting 期间新 operation 取消驱逐；`Purge`（`DELETE ?purge=true`）终结身份
- capabilities / healthz 报 `persistence: noop|auto|s3|pack|tar`
- **luggage**：evict 留现场 + `LastActiveAt` 盖章、Stage-in 按 generation 判新鲜（warm start / 旁路重拉后替换）、`--luggage-high/low-bytes` 水位 GC（stale 优先 → LRU，rename-under-lock 防与初始化竞态）、`GET /v1/beds` 报容量与全部本机 bed；generation 存 S3 object user metadata（`Stat`=HEAD 免下载）
- **双活冲突探测**（§三.5）：`Persist` 写前 HEAD 比对 generation，远端更新则 `store.ErrConflict` 拒绝覆盖（first-writer-wins；evict 路径因 persist 失败自然中止，bed 留在本机继续服务）
- **cas 后端**（§三.3，`internal/store/cas.go`，desync 库）：catar+CDC 流式切块上传（上代 index 做免传清单）、index 提交点带 generation/bytes metadata、块序列相同时零 chunk 上传但推进 index generation、提交后按"LIST − index 引用"做 per-bed GC、restore 经 `UnTarIndex` 并发拉块（块 ID 对解压数据复核，桶内损坏在 restore 报错而不是落进 workspace；desync `LocalFS` 为 `os.Root` 背书，自带 symlink 逃逸防护）；全流程在内存 objAPI fake 上有单测（roundtrip/增量/GC/no-op/冲突/purge）
- **pack 后端**（§三.3，`internal/store/pack.go`）：32 MiB 目标 pack、immutable manifest、`head.json` 原子提交、上代 chunk 免传、pack/manifest/chunk 三级摘要校验、两 pack LRU restore；内存 objAPI fake 覆盖布局/roundtrip/增量/no-op/冲突/purge。是 auto 的新 bed 默认布局，也是高文件数既有 CAS bed 的单向目标；未实现在线 prune/compaction
- **tar 后端**（§三.3，`internal/store/tar.go`）：每次生成完整 tar.gz 并原子覆盖单对象；Restore 使用 `os.Root` 限制路径与 symlink 逃逸；内存 objAPI fake 覆盖布局/roundtrip/全量覆盖/冲突/purge/恶意路径。可显式选择，auto 会识别并保持已有 tar bed

与设计的一处偏差：checkpoint **暂不硬静默**（不暂停接单，调用方自选空闲点打快照）。真实 S3 通路未在本地 CI 验证（无 MinIO）；生命周期逻辑、cas 全编排（经内存 objAPI fake）有单测覆盖。
