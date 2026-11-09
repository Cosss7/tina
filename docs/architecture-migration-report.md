# 架构迁移验收报告

> 2026-11-10 · P1–P6 完成。对应 [实施计划](next-implementation-plan.md)，不构成 H1 `1.0.0` 发布。H0 规格及验收报告保持原样。

## 已完成的切换

控制面是项目、任务、审批、Run、报告、事件和 Router 显式记忆的权威写入方。Go tinad 只负责本机授权、引擎会话、执行命令、进程监管与可靠回传；Router 和 Worker 使用同一套机制，可在不同执行位置运行。

生产入口已经切换为 Go 控制面与 Go tinad。Web/CLI 保持 TypeScript；旧 TS 节点启动、业务 RPC、未认证的旧心跳入口和 TS dsh 适配器已移除。仍有价值的 H0 纯业务、fake 场景与协议测试保留。

| 阶段 | 产出 | 提交 |
|---|---|---|
| P1 | Go 执行消息、适配契约、注册表与 fake | `d445a9f` |
| P2 | 持久项目服务、审批、报告、事件与队列 | `9cf008b` |
| P3 | Go tinad、原生 dsh、进程监管与执行日志 | `853b90d` |
| P4 | 认证派发、去重确认与双 tinad 闭环 | `d8daaaf` |
| P5 | 取消、原生恢复、异常对账与清理证据 | `0098704` |
| P6 | 界面、整栈启动、迁移、版本兼容与整体验收 | 本报告所在提交 |

## 验收结果

| 编号 | 证据与结果 |
|---|---|
| A1 | 控制面独立进程可创建项目/卡片、读取历史报告并处理审批；无执行节点时请求保持 queued。 |
| A2 | 控制面重启保留原项目、任务、Run/命令 ID、审批与队列；已发尝试先对账。 |
| A3/A4 | 自动测试使用两个 Go tinad；另在 Windows Router 与 WSL Linux Worker 间完成建卡、计划、批准、依赖派发、报告和终审。两端日志独立，不共享项目库。 |
| A5 | 断连结果保留并按稳定 ID 补传，控制面落库后确认；重复命令不产生第二次执行，节点重启未知尝试不自动重跑。 |
| A6 | queued 撤回、初始化取消、回合取消、迟到取消、完成竞争、原生崩溃、工具清理与未知停止均覆盖。真实 dsh 工具任务取消确认主进程及工作负载退出。 |
| A7 | 用真实 H0 Store 创建含未 checkpoint WAL 的来源；验证 ID、关联、人工覆盖、报告、审批和原事件顺序，快照完整，重复无副作用，冲突回滚，来源不改写，旧活跃尝试不重跑。 |
| A8 | Windows 离线 make verify 通过：全部 Go 包与 36 条 Node 测试。真实 dsh 在显式开启的独立验收中通过，不进入离线检查。 |
| A9 | 注册表拒绝虚报能力，fake 多会话隔离；授权热更新不替换正在运行的实例，执行日志独占锁阻止双进程共用账本。 |
| A10 | dsh 0.2.2-rc.1 关闭进程后恢复原生身份，新实例准确回忆上一轮随机校验信息；缺少方法的 0.2.0-rc.2 明确拒绝，不新建替代会话。 |

完整进程测试见 `packages/node-runtime/test/execution.e2e.test.ts`；整栈启动、热更新、旧库导入分别见 `demo.e2e.test.ts`、`startup.e2e.test.ts`、`migration.e2e.test.ts`。Go 侧的事务、协议、会话、取消及进程测试紧邻各实现包。

## 真实 dsh 与跨环境验证

测试环境为 Windows 与 WSL Arch Linux，Go 1.27.3、Node.js 26.13.0。跨环境验收使用独立 Linux 内核及原生 /tmp 执行库，属于 Windows/WSL 跨环境验证；没有宣称已经在两台物理主机上重复验收。

真实 dsh 使用 sdk-minimal、deepseek-official、deepseek-flash，以及独立原生历史目录。0.2.2-rc.1 通过结构化报告、进程释放后原生历史恢复，以及正在执行 PowerShell 前台等待任务时的取消；独立查询工具 PID 验证取消前存活、取消后退出，Job Object 最终证明 root=exited、workload=exited。0.2.0-rc.2 通过结构化报告、恢复能力收窄及模型活动的进程级取消；不把该旧版视作已支持原生恢复。

验收目录不提交凭据。原生 profile 认证与压缩格式需要按版本配置，见 [tinad](tinad.md)。真实模型测试入口 `TestRealDSHAcceptance` 默认跳过，只有显式设置配置文件变量才运行：

```sh
# JSON 为本机 dsh.Options：command 参数数组、profile、provider、model、home；不包含模型密钥。
TINA_DSH_ACCEPTANCE_CONFIG=/absolute/path/to/dsh-options.json \
TINA_DSH_ACCEPTANCE_TOOLS=1 \
go -C services/tinad test ./internal/engine/dsh -run TestRealDSHAcceptance -v -count=1
```

复测旧版缺少恢复方法的行为时，加 `TINA_DSH_ACCEPTANCE_EXPECT_NO_RESUME=1`，不启用工具标志。测试会调用真实模型并写入原生历史；只有操作者主动开启时才执行。

跨 Windows/WSL 的 fake 闭环可重复执行：

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go -C services/tinad build -o ../../.build/tinad-linux .
node scripts/accept-cross-os.mjs
```

该脚本要求 WSL 能通过 loopback 访问 Windows 控制面（本次使用的网络配置已通过），默认发行版 arch，可用 TINA_WSL_DISTRO 覆盖。配置与 token 仅用于隔离的临时节点，测试后删除；结果保留在输出的 .build/cross-os-* 目录。

此外，Linux 原生进程树测试、tinad 全包测试和控制面全包测试通过；两项服务在 WSL 中的 go test -race ./... 均通过。Windows 的 Go vet 也通过；没有把 Windows 未具备 cgo 工具链的环境表述为通过 Windows race 检查。CI 先下载依赖，再执行离线验证和 Linux race 检查。

Chrome 无头浏览器实际点击完成建卡、Router 拆卡、批准、派 Worker、查看报告与终审，未出现 JavaScript 异常。界面在控制面断开时显示上次记录并禁用变更操作，执行节点离线与项目数据可访问性分别展示。

## 当前使用与边界

本地使用入口为 [README](../README.md)：make up 启动持久控制面和本机 tinad，默认 fake。新项目授权热更新、取消、历史报告、Worker 指派、原生恢复与重启保留数据均已接通。导入个人 H0 数据仍需操作者先停止旧执行、显式选择源库和目标库；本次只对隔离的验收来源执行迁移，没有自动迁移个人数据。

当前沿用单账号 human:local，完整多用户权限另定；roots 限制工作目录选择，不是工具沙箱。dsh 采用明确的 process 取消降级；会话内取消、交互答复、完整 Router 记忆策略、真实仓库交付与制品文件上传、桌面和移动端不属于本次迁移完成范围。未知副作用需要核查，系统不会提供伪造停止证明来解锁重跑。
