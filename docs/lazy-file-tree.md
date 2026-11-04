# 按需加载文件树：VFS / go-fuse 探索

状态：**待做（探索方案，未实现）**。记录日期：2026-11-04。

本文保存按需加载资源文件的目标、源码调查与候选设计，不代表当前 Hostel 已支持此能力。
已完成 go-fuse 源码阅读；尚未实现原型、执行真实 FUSE 挂载测试或测量启动收益。
本地写入与 worker 部署方式是建议方案，实施前仍需确认。
现有 Volume、文件视图和持久化契约见 [volumes.md](volumes.md)，生命周期见 [bed.md](bed.md)。

## 目标与使用体验

资源在启动前完整下载、解包并送入 sandbox，会把内容传输成本放在首次执行之前。
目标是先声明目录与文件元数据，让 agent 通过普通文件 I/O 看到完整目录；内容在首次使用时获取。
目录可见不表示内容已经在本地，缓存命中后重复读取不再访问来源。

```text
/workspace/
├── skills/
│   └── skill1/
│       ├── SKILL.md        → contentRef A
│       └── scripts/run.py  → contentRef B
└── files/
    └── report.csv          → contentRef C
```

这里的路径是目标用户视图，不是已经支持的新 Volume 配置格式。
同一视图须同时覆盖 Hostel 文件 API、command、Session 和 Pillow 主进程，包含 cat、Python open 等普通文件操作。
只在 HTTP 文件接口中增加下载逻辑不能满足该目标。

| 操作 | 建议语义 |
|---|---|
| ls、find、stat | 从 manifest 返回目录项、类型、大小和权限，不下载正文 |
| 首次读取内容 | 获取固定版本内容、校验、发布到缓存，再返回字节 |
| 重复读取 | 使用内容缓存；内核缓存可进一步减少 FUSE 请求 |
| 新建或 O_TRUNC 完整覆盖 | 直接建立本地内容，不下载旧文件 |
| 局部写入或 append | 第一版先加载原文件，再建立 Bed 独立的修改副本 |
| 删除 | 记录 Bed 内删除状态，遮住来源文件 |
| rename | 更新目录项，同时保持已打开 handle 与覆盖目标的文件语义 |
| 获取失败 | 返回真实 I/O 错误；不得用空文件或 EOF 冒充成功 |

启动成本预计从正文传输转向索引构建和挂载，但实际收益取决于目录规模、访问比例和首次读取延迟，尚无性能数据。

## 文件树、内容缓存与修改状态

三个部分具有不同 owner 和生命周期：

- **文件树声明（manifest）**：路径、文件类型、大小、权限及内容版本。一个目录属于某个固定版本的树。
- **内容缓存**：已经获取的不可变字节，可以淘汰并重新下载。在授权边界允许时可跨 Bed 复用。
- **Bed 修改状态**：新增、覆盖、删除和 rename 等变化，必须按 Bed 隔离，并有独立的保留与回收策略。

path → URL 只是输入示意。URL 是获取地址，不是稳定内容身份；缓存键应绑定资源版本或内容摘要。
预签名 URL 过期或轮换时，需要上层解析器提供新的获取地址，不能因此改变文件版本。
上层拥有资源授权、业务身份和来源解析；Hostel 消费通用声明，不解释 skill、知识库等业务类型。
URL、请求凭据不应进入普通状态响应和日志。

建议第一版将远端来源视为不可变内容，修改只写 Bed 本地，不回写 URL。
远端写回需要另行定义目标、授权、版本冲突和提交语义，不属于当前原型范围。
写入策略尚待确认，本文不将其视为已经定稿的公开 API。

skill URL 若指向 zip，目录名和 URL 本身不足以描述包内文件。可选两种粒度：

1. 发布资源时生成文件级 manifest，按文件获取内容；目录枚举无需读取整包。
2. 按 skill 包加载，首次访问包内文件时下载整包；实现较小，但须明确加载单位是包。

第一版不实现远端压缩包的随机访问索引。manifest 的生成方与交付粒度需要在接入上层资源系统前确定。

## go-fuse 源码结论

调查版本为 [go-fuse d54f65d](https://github.com/hanwen/go-fuse/tree/d54f65d20662f285443aed4b7020056f842f8bb0)，
commit 日期 2026-10-31。调查时 Hostel 的间接依赖是 v2.2.0，而该源码已包含 v2.12 内容；
新接口的可用性须在选择依赖版本时核对，不能直接按现有依赖使用。

推荐使用 fs/ 高层 inode API；fuse/pathfs 和 fuse/nodefs 已被仓库标记为 deprecated。

| 能力 | 接口或实现 | Hostel 仍需承担的部分 |
|---|---|---|
| 目录树 | Lookup、Readdir、Getattr | manifest 与元数据语义 |
| 预建小规模文件树 | OnAdd、NewPersistentInode、AddChild | 树规模、身份与生命周期 |
| 按需内容 | Open、FileReader.Read | 内容获取、重试、完整性和缓存 |
| 本地文件 handle | NewLoopbackFileFromOS | backing file 选择与修改隔离 |
| 内核缓存 | EntryTimeout、AttrTimeout、NotifyEntry、NotifyContent | 何时失效与版本一致性 |
| 生命周期 | fs.Mount、WaitMount、Unmount | 准入、停止、下载取消和清理失败重试 |

PersistentInode 只表示节点不随内核遗忘而从内存树消失，不是数据落盘，也不对应 Volume persistence。
参考 [fs API](https://github.com/hanwen/go-fuse/blob/d54f65d20662f285443aed4b7020056f842f8bb0/fs/api.go)。

### 可借鉴的实现

[ZipFS 示例](https://github.com/hanwen/go-fuse/blob/d54f65d20662f285443aed4b7020056f842f8bb0/fs/zipfs_example_test.go)
先读取本地 zip 索引并建立目录树，Getattr 直接返回文件大小，首次 Open 才解压单个文件。
它证明了元数据与内容可以分开提供，但使用整文件内存缓存，没有 HTTP、缓存容量或淘汰机制。

[LoopbackFile](https://github.com/hanwen/go-fuse/blob/d54f65d20662f285443aed4b7020056f842f8bb0/fs/files.go)
可接管本地 os.File，提供 Read、Write、Setattr、锁、Fsync 与关闭。
自定义 lazy handle 在内容落地后可委托给它，避免重新实现本地文件操作。

[newunionfs](https://github.com/hanwen/go-fuse/blob/d54f65d20662f285443aed4b7020056f842f8bb0/newunionfs/unionfs.go)
展示了本地只读层与可写层合并、copy-up、删除标记和合并枚举。
它依赖本地目录，不提供远端内容 backend，且没有完整实现 Rename/Mkdir 等操作，不能直接作为本需求的成品。

rclone mount 的 VFS 缓存也是参考，但任意 path → contentRef 的 manifest、每个 Bed 的本地修改与远端写回边界
仍需单独核对。首选评估 go-fuse fs/ 承接文件系统接口，具体内容模型由 Hostel 管理。

### 加载时机与性能取舍

| 方式 | 收益 | 代价 |
|---|---|---|
| 首次 Open 下载整文件 | 实现简单，立即获得本地 handle | 只打开不读取也会下载 |
| 首次 Read 下载整文件 | 保持真正按读取触发，避免分块复杂度 | 需要自定义延迟初始化 handle；首次小读也会取完整文件 |
| 按 Read 的 offset/length 获取块 | 减少大文件局部读取流量 | 需要 Range、块缓存、并发合并与校验设计 |

建议原型采用首次 Read 下载整文件。对同一内容的并发请求合并获取，使用临时文件完成校验后原子发布。
请求取消与共享下载任务的生命周期需分别管理；一个 reader 取消不能随意破坏其他 reader。
下载并发、超时、缓存容量和正在使用的缓存文件保留规则由内容层明确控制。

FilePassthroughFder 可让支持的内核直接访问 backing file，但 backing FD 在 Open/Create 时注册。
严格延迟到 Read 才下载时，不能在首次 Open 就依赖 passthrough。内核与权限支持还需验证，原型不以它为前提。
写入时须维护 inode、已打开 handle 和内核缓存的一致性，不能仅替换目录中的路径就认为 copy-up 已完成。

## 接入 Hostel 的候选边界

Filesystem 拥有按需文件树及 Volume 的资源与状态，Bed Manager 协调准备和释放顺序。
文件 API 与 Pillow 进程访问同一个挂载视图；Executor 替换不应销毁文件树或 Bed 修改。

当前 BedFS 绑定已存在的本地源目录，Mount 不允许路径重叠。
不能直接在已有 /workspace 挂载下面再叠加 /workspace/skills 挂载来实现示例布局。
需要在单个 Volume 内组织该文件树，或选择互不重叠的挂载布局；具体 Volume source/schema 尚未设计。
当前普通目录持久化与按需来源的组合也须经过单独设计，不能只增加一个 URL 字段。

### 文件系统 worker

go-fuse [同进程死锁说明](https://github.com/hanwen/go-fuse/blob/d54f65d20662f285443aed4b7020056f842f8bb0/fs/api.go#L171)
列出了服务进程访问自身挂载点时的风险，包括 fork/exec 期间 chdir、FD 重排、执行挂载点上的程序和 mmap。
部分场景有 workaround，GOMAXPROCS=1 尤其需要验证。

Hostel 会启动命令与 Pillow 主进程，因此建议优先验证独立 worker，而不是直接在 daemon 中服务 FUSE：

```text
Hostel daemon
├── filesystem worker
│   ├── manifest 与每个 Bed 的修改状态
│   ├── 内容获取与缓存
│   └── FUSE mounts
└── Executor
    └── agent / shell / Python → 访问挂载视图
```

worker 可以是同一二进制的子进程，不要求拆成独立产品服务。
一 Carrier 一个 worker 还是更细粒度进程，需根据故障影响范围和成本验证后决定。
缓存目录、修改数据目录、worker 程序及其依赖放在挂载视图之外，避免递归访问自身。

### 挂载、身份与回收

Linux 需要验证 /dev/fuse、fusermount 或直接挂载权限，以及实际 securityContext 下的可用性。
不能因运行用户是 root 就认为一定可挂载。daemon 与 BedUser 不同，须验证 allow_other、
权限检查及目录归属，防止增加跨 Bed 访问面。

fs.Mount 内部等待 WaitMount 后才返回，可作为视图发布前的条件。
这只证明挂载可服务，不证明所有远端正文已经可下载；状态应区分挂载就绪、缓存与来源故障。
回收应停止使用方、关闭句柄、处理本地写入、取消并等待内容任务，再卸载并清理 backing data。
Unmount 没有 context 参数，会重试并等待服务循环退出；失败时仍须保留具体挂载的清理责任。
worker 丢失后的挂载状态、恢复方式和已打开文件的失败语义待验证。

Flush 可因 dup 被多次调用，Release 的返回错误会被内核忽略；需要反馈给应用的写入错误不能留到 Release。
Fsync 首先定义为本地修改落盘；如果未来增加远端提交，其成功边界必须另行定义。

Hostel 仍独立运行并报告能力；FUSE 不可用不应自动阻断其他可用能力。
按需文件树请求失败、或改用显式 eager materialization 的策略尚待定义，不能返回空目录冒充已兑现。

## 与持久化的关系

内容来源、读缓存和 Bed 修改不能全部作为普通本地目录一并 checkpoint。
若 Store 遍历合成视图读取每个文件，未访问内容会被全量下载，抵消懒加载收益。

建议持久化固定来源引用、用户修改与删除标记，读缓存仅作为可重建数据。
该方式要求来源可长期重新获取；若来源有保留期限，需选择归档来源或完整物化等明确策略。
这属于新的持久化契约，尚未由当前 whole-volume snapshot 实现。
原型先使用临时文件树验证运行语义，再接持久化；不得把原型的本地缓存当作恢复保证。

## 待做与验收

以下项目全部未完成，复用项目现有测试入口与真实 Linux 环境验证，不以 mock 或编译通过代替挂载证据。

- [ ] 确认写入策略、文件级/包级内容粒度、manifest 生成方与固定版本规则。
- [ ] 在目标 Carrier 权限下探测 FUSE，验证 BedUser 和 Pillow 视图可访问挂载。
- [ ] 实现固定 manifest 与首次 Read 整文件加载原型，覆盖文件 API 和进程 I/O。
- [ ] 用内容请求计数证明 ls/stat/Open 不取正文、首次 Read 获取、重复读取命中缓存。
- [ ] 验证并发读取合并下载、取消隔离、来源失败、URL 刷新及摘要不匹配。
- [ ] 验证新建、O_TRUNC、append、局部写、rename、删除、打开后删除与跨 Bed 修改隔离。
- [ ] 在挂载目录启动 Python、脚本和二进制，覆盖 local/supervisor、GOMAXPROCS=1 与并发压力。
- [ ] 验证 Executor 替换、worker 退出、Bed 回收、打开文件时卸载与清理重试。
- [ ] 测量启动、首次读、缓存读的耗时及下载字节、缓存占用，比较 eager 方案。
- [ ] 设计并验证持久化，证明 checkpoint/evict 不触发未访问来源的全量下载。

第一版不包括分块下载、远端写回、运行中替换 manifest 和完整远端 zip 随机访问。
后续实现应分别交付功能与环境验证证据，更新本文状态后再将稳定契约并入 volumes.md。
