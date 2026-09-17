# 错误契约

Hostel 的通用错误契约位于 `internal/error.go`。领域层决定失败事实，API 层将事实转换为所属协议；领域错误不携带 HTTP 状态码、OpenSandbox 错误码或响应 JSON。

## 分类与原因

`hostel.ErrorKind` 表达稳定类别：ErrInvalidArgument、ErrNotFound、ErrConflict、ErrUnavailable、ErrPreparationFailed。使用 `hostel.WrapError(kind, op, cause)` 添加操作上下文，通过 `errors.Is` 判断类别、通过 `errors.As` 读取结构。底层 cause 保留，文件不存在等已有标准库错误仍能通过 `errors.Is` 识别；取消和截止时间复用 `context.Canceled` / `context.DeadlineExceeded`。

Op 使用固定操作名，例如 `session directory`，不拼入 command、env、凭据、调用方内容或宿主路径。`Error()` 保留内部错误链；API 使用 `Message()` 或自身的固定消息，不将底层 OS/IPC 错误直接暴露。错误分类禁止依赖字符串比较。

新领域错误及触及错误语义的改动遵循此契约。已有领域 sentinel 可保留其精确含义，按所属用例逐步接入；不要批量替换成泛化错误而丢失调用方需要的区别。类别表示失败种类，不替 API 决定状态码；例如查询不存在的文件和执行前 cwd 不存在属于不同操作，协议映射可不同。

## 执行结果

程序执行后返回非零 exit code 是正常的进程终态，不能当成参数校验错误。Execution 将终止原因与进程事实分开保存。

Session 的 env/cwd 准备失败使用 ErrPreparationFailed，保留根因；命令没有执行时 `ExecutionResult.Process` 为 nil，错误通过 `Err` 传递，终态原因为 `preparation_failed`。原生 API 返回 `process: null` 和安全错误说明；Execd 根据自身事件规范投影为 error/125。Shell 仍可复用，不能因此宣称 Executor 丢失。实际 transport/进程丢失继续使用 `lost / executor_lost`。

同步拒绝在产生执行流前返回；已经生成 Execution 的异步失败必须进入其唯一终态。底层函数只包装和返回错误，拥有生命周期的入口负责一次带 bed/execution 上下文的终态记录，避免逐层重复日志。
