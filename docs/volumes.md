# Volume：数据、访问与持久化

Bed 拥有命名 Volume，Pillow 通过 Mount 选择路径和读写权限。Volume 回答数据来自哪里、
由谁管理、能保留多久；Mount 回答执行环境怎样访问它。卷身份由 Bed Name 与卷名确定，
改变 Pillow 挂载路径不改变持久身份。

Filesystem 准备本地数据、目录句柄及 Pillow 视图；Store 负责远端同步、恢复和显式传输。
Bed Manager 安排先恢复后运行、先停止写入后回收的顺序，见 [bed.md](bed.md)。

## 文件需求与访问

FS 的稳定需求是保护 Bed 私有数据并复用公共区域；Hostel/OS 核心资源仍按规则受控访问，
不能因不属于某个 Bed 就自动开放。程序仍以普通 rootfs 路径工作。
下面两条路线是落实同一需求的不同机制：从共享环境中隐藏其他 Bed，或构造不引入其他
Bed 的独立视图。它们不改变私有与公共的归属。

### 两种文件视图思路

| | 1. 从共享环境中排除私有资源 | 2. 向独立视图中引入公共资源 |
|---|---|---|
| 起点 | Carrier 原有文件系统，公共资源尽量开放 | Bed 自己的文件视图与私有数据 |
| 核心动作 | 禁止访问其他 Bed 数据和必要的管理资源 | 引入程序、库、系统配置及显式卷，声明访问方式 |
| 公共资源 | 默认复用，重点维护禁止访问的边界 | 按内部目录规则或 Volume/Mount 引入 |
| 主要难点 | 广泛操作权限不能绕过私有保护，受限宿主也须有可靠执行机制 | 路径完整性、映射与底层权限要分别兑现 |

路线 1 更接近共享 Linux 上的多用户使用，但目前没有覆盖不同宿主条件的完整实现方案。
独立 UID 可保护私有目录，却不能自动写入 root 所有的公共文件；授予绕过 Unix 权限的能力，
又会使单靠 UID 的私有保护失效。mount namespace 可以遮挡资源，但并非所有环境都允许；
路径重定向也不能代替可靠的访问控制。

**当前采用路线 2。** 以 Bed 数据根组织文件视图，按规则引入 Carrier 的公共程序、库和运行
依赖，再组合 Pillow 的显式挂载。bwrap 提供实际挂载视图；不可用时按能力使用 PRoot、
pathshim 或 Carrier 视图，并分别报告路径体验与访问保护的缺口，不能把降级解释为完整隔离。

公共目录的引入不递归修改来源的 owner、mode 或 ACL。启动时全树授权会使成本随公共软件
数量增长，在 OverlayFS 上还可能触发 copy-up 和额外 I/O；这适用于所有公共目录，
`/usr/local` 只是典型实例。挂载或映射为可写只表示视图允许写入，实际访问仍由 Bed 身份
与来源权限决定。镜像或目录提供方负责公共资源的权限，Hostel 不承诺任意系统目录可写，
也不自动修复已有文件的权限或跨 UID 安装、替换和卸载行为。

公共目录的路径与只读/可写属性由 `bedfs.CommonDirectories` 统一声明，mount
和 Landlock 消费同一份规则。不存在唯一的软件根路径；公共目录可以有多个，也不限于
软件安装用途。这些目录不随 Bed 回收，新增路径不应在各机制中增加按路径特判的分支。
`ProcessDirectories` 额外列出进程设施，它们不作为普通文件 API 数据导出。
私有根及 Volume 的来源、归属、只读要求继续由 BedFS/Mount 表达。

| 内容 | 访问目标 | 当前机制与边界 |
|---|---|---|
| Bed 私有数据 | 本 Bed 操作，其他 Bed 不得读写 | 独立身份及私有目录权限；可用时叠加路径隔离 |
| 公共可写 | 在提供方授权范围内共同使用软件 | 可写映射与实际 Unix 权限共同决定；不进行全树权限准备 |
| 公共只读 | 读取/执行运行依赖，不修改 | 文件 API 不隐式暴露；mount/Landlock 限制进程，UID-only 依赖实际 Unix 权限 |
| 管理私有 | 不隐式授权管理数据和凭据 | 配置的 beds-root 及受保护目录；能遮蔽/排除时使用对应机制，UID-only 不能隐藏 world-readable 凭据 |
| 外部来源 | 遵守 Mount 与提供方授权 | 不修改源权限，不改变来源 owner |

Landlock 不再宽泛授权 Carrier 的 `/run`、`/tmp`、`/var/tmp`：父目录授权会同时开放其下的
凭据或相邻 Bed 数据。临时数据属于 Bed 数据根；若路径辅助程序无法在这个授权范围工作，
实际组合探测应报告路径体验缺口，不重新扩大共享父目录来掩盖问题。

## 来源与挂载

每个 Volume 只能选择一种来源：

| 来源 | 数据与生命周期 |
|---|---|
| `managed: {persistence: none}` | Hostel 分配临时目录，Bed 回收时删除 |
| `managed: {persistence: sync}` | Hostel 管理本地工作副本，自动持久化并恢复完整卷 |
| `host_path: {path: ...}` | 引用 daemon 可见的已有目录，外部负责持久化与回收 |

HostPath 可以是 Pod 已挂入的 PVC；Hostel 不创建源目录、不改变源属主、不删除源数据。
来源必须是已有目录，不能覆盖 beds-root，不同源不得重叠或通过 symlink 互为别名。
调用方负责来源授权。managed 卷在 Bed 之间独立，同一 Bed 的多个 Pillow 可以共享同一卷。

```yaml
volumes:
  - name: workspace
    managed: {persistence: sync}
  - name: exchange
    managed: {persistence: none}
pillows:
  - name: agent
    workdir: /workspace
    mounts:
      - {volume: workspace, path: /workspace}
      - {volume: exchange, path: /run/service, read_only: true}
  - name: service
    mounts:
      - {volume: exchange, path: /run/service}
```

示例的只读要求须由文件 API 和进程视图同时兑现；无法兑现时初始化失败。
挂载路径必须绝对、非根，不能重复或覆盖运行时保留路径；同一视图对同一卷只声明一次挂载。
允许在私有工作区内嵌套共享卷：最长目标路径优先，进程先挂父、后挂子，List/Search 每次
下降重新选择逻辑来源。managed 父卷可准备挂载占位；借用父目录中的占位必须已存在，
Hostel 不为此修改外部目录。Pillow 之间不继承挂载，文件 API 与执行共用所选 Pillow 的视图。

全新 Bed 省略卷声明时展开默认 workspace 卷，其 persistence 由 daemon 默认同步配置解析并固定。
Pillow 列表必须显式声明，Hostel 不推断任何挂载；每个 Pillow
只获得自己声明的 mounts。workdir 只决定 cwd 和相对路径基准，不决定持久化范围。

## 本地数据与生命周期

`--beds-root` / `HOSTEL_BEDS_ROOT` 指定所有 Bed 本地数据的父目录，默认 `/workspace`。
它属于 Carrier 路径空间，与 Pillow 视角的 `/workspace` 分别解释。

```text
<beds-root>/
├── .identities/                 # 本地身份和执行声明，不进入快照
├── bed1/
│   ├── meta.json                # generation 与 sync 卷契约
│   ├── rootfs/                  # 临时根数据，例如 /tmp
│   └── volumes/
│       ├── workspace/           # sync
│       └── exchange/            # none
└── bed2/
    ├── meta.json
    ├── rootfs/
    └── volumes/
        └── workspace/           # 与 bed1 同名卷独立
```

Rootfs 为未命中挂载的数据提供本地根，不参与自动持久化。Pillow 主进程重启、Executor
替换以及保留目录的 daemon 恢复都保留本地卷数据；正常 evict 删除整个本地 Bed 目录，
再次创建只恢复 sync 卷内容，none 卷和 Rootfs 重新建立。HostPath 生命周期始终由外部管理。

本地身份记录保存 Pillow、卷和宿主资源声明；远端快照只保存 sync 卷声明与内容，
排除 Rootfs、none 卷声明及内容、HostPath、环境配置和凭据。`meta.json` 位于进程 Rootfs 外；
private 视图遮蔽 Bed 父目录，再挂回自己的根与卷。其他机制的访问保证见 [isolation.md](isolation.md)。

## 路径与文件视图

BedFS 为每个 Pillow 持有视图和目录句柄，共享 Bed 根数据与卷来源。路径先在客户端空间
规范化，命中最具体的 Mount 则解析到卷来源；其余路径解析到 Rootfs。公共软件和系统依赖只进入进程视图，不隐式进入文件 API。返回路径使用对应逆转换。Volume 因此既可接入外部数据，也可在
私有目录中接入一块公共目录；路径改变不改变其归属和回收责任。

| 路径空间 | 作用 |
|---|---|
| Client | 文件 API 的 path 与执行 cwd；相对路径以所属 Pillow Workdir 为基准 |
| Carrier | daemon 使用的真实目录，如 `<beds-root>/bed1/volumes/workspace` |
| Executor | 进程实际看到的路径，由选定文件视图投影 |

默认 workspace 挂载下，`/workspace/a` 和 workdir 内相对路径 `a` 都指向
`volumes/workspace/a`；`/tmp/job` 未被挂载覆盖时指向 `rootfs/tmp/job`。
workdir 可以位于 Rootfs 或任意已声明挂载内，不必叫 `/workspace`。

文件 API 通过每个来源的 `os.Root` 进行 descriptor-relative 操作，拒绝 symlink 逃出来源。
文件 API 是 Bed 数据管理接口，由 daemon 在进程内调用 BedFS，不启动文件 worker，也不模拟 Bed 的 Unix 身份。
Handler 解析 Bed，获取覆盖整个请求的 operation，再使用默认 Pillow 的 BedFS；请求不创建或恢复缺失 Bed。
BedFS 只路由 Rootfs 与显式 Mount，独占 Carrier 的显式只读 fallback 是下面说明的授权例外。
API 遵守 Mount 只读约束，但 daemon 可能访问 Bed 进程因 Unix 权限无法访问的数据。
HostPath 声明同时授权 daemon 的文件 API 访问来源；不得把 Bed UID 的 DAC 限制当作该 API 的限制。
Hostel 管理的数据写入后维护 Bed owner；HostPath 保持提供方权限，不自动 chown 来源。
禁止删除或重命名挂载根及其父目录；跨挂载 rename 不支持，跨多个挂载的父目录导入导出也拒绝。
进程 cwd 属于结构化字段，先由 BedFS 解析再投影；命令文本保持原样，绝对路径字面量由
实际进程视图解释。Session 使用创建时绑定的视图，通过独立 shell 控制步骤更新 cwd。

### 进程视图与访问保证

| 视图 | 路径能力与限制 |
|---|---|
| mount / bwrap | 以 Bed Rootfs 建立私有根，接入运行依赖和卷；可兑现只读挂载 |
| PRoot | 通过 ptrace 提供根路径视图，改善路径体验，不提供安全隔离或只读保证 |
| pathshim | 提供工作目录、临时目录和受支持映射的局部路径视图 |
| carrier | 使用宿主进程视图，不能重定向任意绝对路径；结构化文件 API 仍使用 BedFS |

shared/confined 组合按 PRoot → pathshim → carrier 验证可用的进程视图；访问屏障由
Landlock 或 UID/DAC 等机制独立提供。当前 Landlock 未允许外部卷进入规则，carrier 也不
支持进程路径重定向；这些组合可保留读写卷的 API 映射并披露进程映射缺口。显式只读要求
只能由实际可用的机制满足，不能静默放宽。

bwrap 优先使用 daemon 已有权限准备文件视图，否则尝试无特权 user namespace。
rootful 视图模板归 Bed；要求私有 PID namespace 时，在每个 Executor 中为各 Pillow 派生
匹配 procfs 的最终视图，Executor 替换重建派生视图并保留数据与模板。准备失败保留清理责任。

这个根视图用于保护私有数据并接入共享系统程序和工具链；系统依赖通常只读，公共软件的可写区域按目录规则引入，实际写权限由来源提供。若覆盖重新暴露 Bed 父目录，必须再次遮蔽。非运行依赖目录内的程序通过
显式卷接入或放入 BedFS；不能自动挂回任意程序的宿主父目录。该视图不提供可任意修改系统
目录的完整镜像或 CoW rootfs。机制与降权顺序见 [isolation.md](isolation.md)。

`process_view.rootfs`、`mode` 和 `path_mappings` 披露实际根视图及读写／只读映射能力；
它们与文件隔离等级分别判断。启动验证须让 command、Session 和主进程实际写文件，再由
BedFS 读回，不能用程序存在或 `true` 成功代替。

pathshim 0.1.7 要求 source 和 cwd 在启动前已经存在并且可遍历。Hostel 负责准备私有目录、
managed Volume 和 workdir；HostPath 的访问权限由提供方准备。默认路径与显式 mounts 先按
覆盖关系合并，再交给 helper，避免重复 destination。启动 helper probe 显式检查 guest cwd，
真实 Bed 的组合执行探测继续在最终 Bed 身份下验证文件互通。参数或访问错误不会由 pathshim
自动创建目录、改用 `/` 或静默 passthrough；实例 probe 失败保留原始诊断并按工具策略处理。
若 Carrier BedHome 位于 `/tmp` 等 guest bind 下，pathshim 额外保留 BedHome 的同路径映射，
使局部视图外的私有 cwd 不被再次重定向；Session 后续切换 cwd 也使用同一视图。

### 文件 API 的候选读取

挂载在进程侧已兑现时，API 直接访问源目录。未兑现时，Reader 先读对应的 Rootfs 本地
候选，失败再读卷来源；两者均受各自目录句柄约束，不合并目录树。写入和 Transfer 始终
使用声明来源，API 始终检查只读要求。只读卷的来源若与公共软件树重叠，该 Pillow 的隐式软件写入口保守关闭，防止源路径别名绕过。这样受限视图仍可读取进程产物，同时报告能力缺口。

独占 Carrier 可显式启用 `--dorm-read-fallback-root`：shared 文件视图下，未命中卷的
绝对路径在 BedFS 不存在时，允许只读 API 尝试所配置进程根。相对路径不回退，mutation
仍只操作 BedFS。该策略默认关闭，会暴露所配置根，不能用于共享 Carrier。

### 临时目录

共享优先不等于所有路径都共享：软件和运行依赖有复用价值，临时文件通常是 Bed 的中间产物，
默认随 BedHome 管理，不进入持久化。Filesystem 负责准备、保护与路径体验；准备 `bedHome/tmp`
不要求独占 rootfs。Carrier 的公共 `/tmp` 仍属于 Carrier，不因路径同名就成为 Bed 的默认临时来源。

`/tmp` 默认属于 Bed Rootfs，由命令、Session 和主进程共享；Executor 替换保留内容，
本地回收时删除，目录带 sticky bit。显式挂载可替换其数据来源，权限和持久性按卷契约处理。
mount/PRoot 和 pathshim 的临时目录映射让进程与 API 指向同一数据；carrier 模式用宿主
路径设置 `TMPDIR`，无法重定向程序硬编码的 `/tmp`。Pillow 与单次请求 env 可覆盖 TMPDIR。

需要跨 Bed 共享临时文件时，使用既有 Volume/Mount 声明，可将同一 HostPath Volume 挂载到 `/tmp`，
不增加另一套临时目录配置；Hostel 不接管
提供方的权限与清理。显式挂载优先于默认映射，每个目标只发出一次；无法兑现的只读
声明不能被隐式可写默认项替代。缺少路径映射时，硬编码 `/tmp` 与遵守 TMPDIR 的
程序可能访问不同来源，实际进程视图和隔离状态须如实披露。

## 自动持久化

`managed.persistence: sync` 表达持久化需求，`HOSTEL_SYNC` 选择 daemon 实现及默认行为。
内部 StoreKind 和响应 `sync` 报告实际算法，不是创建 Bed 时选择同步范围的字段。
显式 sync 必须找到 durable Store；daemon 默认 noop 时仍可使用已配置的 S3。没有后端
则初始化失败。只有 none／HostPath 的 Bed 不访问远端，也不占用 durable pin。

Store 复用实例的 S3 bucket、prefix、endpoint、region 与凭据；凭据不进入 Bed 环境。
对象前缀按 Bed Name 分开用于归属与回收，不是共享 S3 凭据下的租户授权屏障。
实例提供 path-style 配置以适配所用 backend；连接配置由 Store 统一管理。

### 同步与恢复流程

```text
初始化 → 检查本地与远端 generation
  → 需要恢复：下载到旁路目录 → 验证卷契约和目录完整性 → 发布到 Bed 目录
  → 准备 Pillow 文件视图与进程 → Ready
本地工作 → operation / session / pressure 触发同步 → Store 合并、串行、周期兜底与退避
显式 checkpoint → 等待同步结果
最终回收 → 停止所有写入 → 同步成功 → 释放和删除本地数据
```

恢复时只依据已有 sync 卷契约补全或核对持久卷声明，不把未声明卷一律替换成默认 workspace。
none 卷按本次声明创建为空目录，可增加、删除或改名。
省略 volumes 时只从快照补全 sync 卷，临时卷及其 Pillow mounts 必须由创建方声明。
同一本地生命周期（包括运行中和保留本地身份的 daemon 恢复）仍要求完整卷声明一致。
恢复失败清理旁路目录、保留原本地现场；数据契约不匹配不能发布。持久化失败阻止删除唯一
本地副本。异常遗留目录称 luggage，初始化按 generation 判断新鲜度，过期时替换而不合并；
回收与 UID 保留见 [bed.md](bed.md)。正常关闭保留本地数据，但宿主丢失后的恢复依赖远端快照。

后台同步在发起时选择无 operation 的 dirty Bed，以 activity watermark 防止上传期间的新
活动被误标为已同步。Session 连接本身不永久阻止同步，真实流量会推进水位。
周期同步和显式 checkpoint 允许并发写入，不提供应用事务一致性；最终 evict 则关闭准入并
停止 Pillow、Session、Executor、Transfer 写入后再同步。

### 快照完整性与单写者

多个 sync 卷与 Bed 元数据使用同一 generation 和一个提交点。先上传完整数据，再发布
引用；任一上传失败都不能推进已同步版本。快照发布原子性不等于对并发写入的应用快照。
崩溃可能丢失上次同步之后的改动，缩短周期只能缩小窗口。

上层调度必须保证同名 Bed 的单写者归属。Store 写前检查远端 generation，发现更新则
拒绝覆盖；HEAD 与 PUT 之间仍有竞争窗口，因此这是冲突探测，不是分布式锁或原子 CAS。
失败的最终同步保留本地副本供处理，不能以自动重试替代上层归属裁决。

当前 Restore 恢复完整快照。相同 generation 的可用本地副本可复用，否则预计恢复字节为
完整快照大小；generation 差值不能用来估算增量。快照只恢复文件数据，运行配置由创建方
重新提供，网络、Executor 和 Amenity 状态由各领域重新准备。

### 格式与回收

| 实现 | 提交与增量单位 | 主要取舍 |
|---|---|---|
| CAS | desync 将归档流切成内容块，index 原子发布顺序与 generation | 未变块免传；对象较多，提交后回收未引用块 |
| Pack | 不可变 pack → snapshot manifest → head | 减少小对象请求；历史 pack 追加保留到 purge，尚无在线 compaction/prune |
| Tar | 完整 tar.gz 单对象原子替换 | 对象最少，无增量复用；每次完整上传下载 |
| Restic | repository snapshot ref 与 head generation | 内容去重；自动同步 repository 由 Hostel 管理，显式 Transfer repository 由调用方管理 |
| noop | 无远端读写 | 仅本地数据，evict 后从空数据开始 |

`auto` 发现 CAS／Pack／Tar 提交点，选择最高 generation，相同最高代存在多个格式则报错。
新 Bed 默认 Pack；已有格式保持，CAS 达到配置的文件数阈值可在下一代切至 Pack，旧对象
保留到 purge。显式格式读取时也识别这三种布局，并沿用 generation；purge 清理各布局。
Restic 使用独立 repository/head，不属于 auto 的三种格式发现。

CAS、Pack 的内容摘要在恢复时复核；Tar 在受限目录句柄下解包，拒绝绝对路径、逃逸链接、
重复条目及特殊文件，恢复普通权限和 mtime，不恢复 owner 或 setuid/setgid。
各格式只接收按卷名生成的相对根集合，复用同一过滤规则。实现锚点是
`internal/bed/store/sync`，调度与本地发布由 `internal/bed/store` 拥有。

## 显式文件传输

客户端上传下载通过 HTTP 与 BedFS 交换字节；Transfer 则让 Hostel 直接在 Bed 与 S3
之间搬运数据。它由调用方触发，不发布 Bed 快照、不修改同步完成标记；none 卷也可传输。
自动持久化与 Transfer 共用连接配置，但后者独立选择 `copy` 或 `restic`。请求入口见
[protocol.md](protocol.md#文件与传输入口)。

Transfer 的 Bed 路径遵守所选 Pillow 的视图，S3 key 相对实例 prefix，调用方负责分配
对象路径及授权，并避开自动持久化前缀。请求不接受任意 bucket、endpoint 或临时凭据。
copy 的目录 key 以 `/` 结尾表示前缀；复制目录内容不添加源 basename，空目录不产生对象。
单文件目标是精确 key。key 拒绝空段、`.`、`..` 和反斜线，下载也检查远端对象名。

### 执行、重试与取消

Bed Ready 后才可注册传输，file operation 覆盖整个后台任务；创建请求断开不取消任务。
普通 eviction 在任务运行时拒绝，Purge 和 daemon 关闭取消并等待 I/O 退出后才释放 BedFS。
运行结果包含已完整发布的文件数与字节数，取消请求已接受不代表进程或 I/O 已退出。

操作 ID 在当前 Store Manager 实例及 Bed 内幂等，相同参数返回原操作，参数冲突拒绝。
记录有容量和保留期限，重启后不恢复；调用方用 instance_id 拒绝发往不同实例的延迟请求。
记录缺失不能证明旧任务成功，记录淘汰后复用 ID 会成为新操作。运行并发和 deadline 有界，
查询、重放与取消不创建 Bed、不额外保活。取消保留已完成文件，multipart 失败尝试 Abort。

### 数据保证

copy 只支持普通文件和目录，拒绝 symlink 与特殊文件；不携带完整 POSIX 元数据。
本地下载先写临时文件再原子发布，不覆盖模式原子创建，覆盖模式替换同名文件；S3 不覆盖
写入使用条件提交，不支持时失败。两种模式都保留目标端多余文件。
目录整体按文件逐一提交，失败可能留下部分结果；不自动回滚，也不冻结源端写入。
调用方须保证传输期间源文件稳定；边上传边截短或改写不在数据保证范围内，Hostel 不负责检测
或修复这种并发修改。
Transfer 只能选择单一数据来源，不能把多个挂载拼成一个导入导出目录。

Restic 通过 BedFS 导出到私有临时目录后上传，下载先恢复到私有目录再导入，避免外部进程
直接遍历可被替换的宿主路径。代价是额外本地复制与临时空间。repository 不存在才初始化，
权限与密码错误直接失败；上传返回完整 ref，下载必须明确 ref，不接受 latest 或缩写。
可指定 parent_ref；overwrite 控制下载文件替换，不控制 snapshot 追加。

Restic 保留普通权限、mtime、空目录与未逃逸的相对 symlink，拒绝特殊文件和越界链接。
取消先中断进程以释放 repository 锁，再按预算强制终止并等待；调用方负责显式 repository
的维护，Hostel 不自动 unlock/forget/prune。Restic 的二进制与密码由实例配置，凭据只交给
受管进程。自动快照与显式传输的 repository 身份和回收责任相互独立。

## 验证入口

[单机 E2E](../tests/e2e/README.md) 覆盖双 Bed、共享卷、只读挂载、临时数据、跨实例恢复、
改变挂载路径及 purge；Store 测试覆盖多卷失败时旧快照仍可恢复。运行证据记录具体环境
与格式，不能把文件 API 成功等同于进程路径或全部存储后端已验证。

### Pack Object 大小与 S3 上传分片

`HOSTEL_PACK_TARGET_BYTES` / `--pack-target-bytes` 默认 `33554432`（32 MiB），
控制 pack 聚合后的 Object 目标大小，优先级为 Store Options > CLI > env > 默认值。
这是持久化布局参数，与 S3 传输分片独立。

所有对象统一交给 AWS `transfermanager.UploadObject`。Hostel 不暴露 S3 分片大小或 multipart
切换阈值，直接采用库默认策略；当前依赖在低于 16 MiB 时使用普通 PUT，达到该阈值时使用
multipart，初始分片大小为 8 MiB。分片数量与大对象的分片调整也由 SDK 决定。
pack 持久化和文件上传共用该入口，已有快照布局和读取方式不变。
每个上传的资源预算保持最多 2 个并发 part，失败或取消后的清理预算为 30 秒。

S3 上传以源数据在传输期间保持稳定为前提。最终回收同步先关闭准入并停止受管写入者；
自动持久化交给 S3 的是已完成的 pack/chunk buffer 或临时归档，上传完成前不再修改或复用。
因此，这些路径不会一边上传同一个产物、一边继续改写它。后台同步期间业务文件仍可能变化，
其快照一致性边界见[自动持久化](#自动持久化)，不等于上传产物本身仍在变化。

上传保留输入的 seek 能力，并将长度传给 SDK。Hostel 不为源被并发改写增加自定义长度校验、
冻结或快照机制，也不承诺这种情况下必然失败；普通上传、multipart、重试及失败清理由库负责。
显式 Transfer 直接读取源文件时，由调用方满足同一稳定性前提。

S3 兼容性回归位于 `internal/bed/store/backend/s3_integration_test.go`。
设置 `HOSTEL_TEST_S3_ENDPOINT`、`HOSTEL_TEST_S3_BUCKET`、`HOSTEL_TEST_S3_REGION`，
并通过运行环境注入 `HOSTEL_TEST_S3_ACCESS_KEY` 和 `HOSTEL_TEST_S3_SECRET_KEY` 后，
正常的 `make test` / `repocli validate` 会执行该用例；未设置 endpoint 时跳过。
它只写随机 `diagnostics/hostel-multipart/` 前缀，校验分片边界、metadata、读回 SHA-256
和取消清理，并逐个删除测试对象。HTTP endpoint 会经本地可信 TLS bridge，确保覆盖
SDK 的 HTTPS aws-chunked 路径；这属于 S3 backend 集成验证，不代表 Bedbox 部署或业务 E2E。

测试也支持 `HOSTEL_TEST_S3_CREATE_BUCKET=1`：未指定 bucket 时创建并清理专属临时 bucket。
`HOSTEL_TEST_S3_PATH_STYLE=false` 可选择 virtual-hosted 访问。测试覆盖稳定源的普通上传与 multipart、
取消清理和不覆盖写入；不把并发改写源文件的行为作为上传契约验收条件。
