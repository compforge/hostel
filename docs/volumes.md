# Volume：数据、访问与持久化

Bed 拥有命名 Volume，Pillow 通过 Mount 选择路径和读写权限。Volume 回答数据来自哪里、
由谁管理、能保留多久；Mount 回答执行环境怎样访问它。卷身份由 Bed Name 与卷名确定，
改变 Pillow 挂载路径不改变持久身份。

Filesystem 准备本地数据、目录句柄及 Pillow 视图；Store 负责远端同步、恢复和显式传输。
Bed Manager 安排先恢复后运行、先停止写入后回收的顺序，见 [bed.md](bed.md)。

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
挂载路径必须绝对、非根，不能与同一 Pillow 的其他挂载或运行时保留路径重叠；同一视图对
同一卷只声明一次挂载。Pillow 之间不继承挂载，文件 API 与执行共用所选 Pillow 的视图。

全新 Bed 省略卷声明时展开默认 workspace 卷，其 persistence 由 daemon 默认同步配置解析并固定。
省略 Pillow 列表时创建 main，并在存在 workspace 卷时挂载到 `/workspace`；显式 Pillow
只获得自己声明的 mounts。workdir 只决定 cwd 和相对路径基准，不决定持久化范围。

## 本地数据与生命周期

`--beds-root` / `HOSTEL_BEDS_ROOT` 指定所有 Bed 本地数据的父目录，默认 `/workspace`。
它属于 Carrier 路径空间，与 Pillow 视角的 `/workspace` 分别解释。

```text
<beds-root>/
├── .identities/                 # 本地身份和执行声明，不进入快照
├── bed1/
│   ├── meta.json                # generation 与 managed 卷契约
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

本地身份记录保存 Pillow、卷和宿主资源声明；远端快照保存 managed 卷契约与 sync 卷内容，
排除 Rootfs、none 卷内容、HostPath、环境配置和凭据。`meta.json` 位于进程 Rootfs 外；
private 视图遮蔽 Bed 父目录，再挂回自己的根与卷。其他机制的访问保证见 [isolation.md](isolation.md)。

## 路径与文件视图

BedFS 为每个 Pillow 持有视图和目录句柄，共享 Bed 根数据与卷来源。路径先在客户端空间
规范化，命中 Mount 则解析到卷来源，否则解析到 Rootfs。返回路径使用对应逆转换。

| 路径空间 | 作用 |
|---|---|
| Client | 文件 API 的 path 与执行 cwd；相对路径以所属 Pillow Workdir 为基准 |
| Carrier | daemon 使用的真实目录，如 `<beds-root>/bed1/volumes/workspace` |
| Executor | 进程实际看到的路径，由选定文件视图投影 |

默认 workspace 挂载下，`/workspace/a` 和 workdir 内相对路径 `a` 都指向
`volumes/workspace/a`；`/tmp/job` 未被挂载覆盖时指向 `rootfs/tmp/job`。
workdir 可以位于 Rootfs 或任意已声明挂载内，不必叫 `/workspace`。

文件 API 通过每个来源的 `os.Root` 进行 descriptor-relative 操作，拒绝 symlink 逃出来源。
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

私有根只接入需要的共享系统程序和工具链，系统依赖通常只读，`/usr/local` 保留 Carrier
级可写软件环境。若覆盖重新暴露 Bed 父目录，必须再次遮蔽。非运行依赖目录内的程序通过
显式卷接入或放入 BedFS；不能自动挂回任意程序的宿主父目录。该视图不提供可任意修改系统
目录的完整镜像或 CoW rootfs。机制与降权顺序见 [isolation.md](isolation.md)。

`process_view.rootfs`、`mode` 和 `path_mappings` 披露实际根视图及读写／只读映射能力；
它们与文件隔离等级分别判断。启动验证须让 command、Session 和主进程实际写文件，再由
BedFS 读回，不能用程序存在或 `true` 成功代替。

### 文件 API 的候选读取

挂载在进程侧已兑现时，API 直接访问源目录。未兑现时，Reader 先读对应的 Rootfs 本地
候选，失败再读卷来源；两者均受各自目录句柄约束，不合并目录树。写入和 Transfer 始终
使用声明来源，API 始终检查只读要求。这样受限视图仍可读取进程产物，同时报告能力缺口。

独占 Carrier 可显式启用 `--dorm-read-fallback-root`：shared 文件视图下，未命中卷的
绝对路径在 BedFS 不存在时，允许只读 API 尝试所配置进程根。相对路径不回退，mutation
仍只操作 BedFS。该策略默认关闭，会暴露所配置根，不能用于共享 Carrier。

### 临时目录

`/tmp` 默认属于 Bed Rootfs，由命令、Session 和主进程共享；Executor 替换保留内容，
本地回收时删除，目录带 sticky bit。显式挂载可替换其数据来源，权限和持久性按卷契约处理。
mount/PRoot 和 pathshim 的临时目录映射让进程与 API 指向同一数据；carrier 模式用宿主
路径设置 `TMPDIR`，无法重定向程序硬编码的 `/tmp`。Pillow 与单次请求 env 可覆盖 TMPDIR。

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

恢复时依据已有 managed 卷契约补全或核对声明，不把未声明卷一律替换成默认 workspace。
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
