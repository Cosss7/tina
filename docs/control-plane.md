# 控制面项目服务

修改项目 API、数据存储、业务规则、审批、报告或派发决策时读取本文。总体边界见 [架构入口](architecture.md)，跨机器消息与故障语义见 [执行协议](execution-protocol.md)。

## 职责与业务记录

控制面保存项目业务的权威记录，执行确定性的规则与状态推导。模型请求进入执行队列，交给已绑定的 tinad；控制面不启动引擎。执行位置离线时，项目读写、历史查询和已有审批仍可使用，需要该位置的请求保持 queued。

| 记录 | 含义 |
|---|---|
| Project / Agent | 项目及成员身份、角色、配置、执行位置绑定与 Router 显式记忆 |
| Task | 持续追踪的需求或任务，包含依赖、指派和人工状态覆盖 |
| Run | 一次业务尝试，记录发起人、执行关联与状态；同一 Task 可经历多次 Run |
| Report | 某次 Run 的业务报告，保留模型内容、用量与生成来源；历史报告不随重试覆盖 |
| Approval / Event | 人工审批记录与按项目排序的产品事件 |

Router 返回计划建议，控制面校验后创建待批计划；人批准后才创建子任务。Worker 返回报告，控制面保存并推导评审状态；终审由人通过控制面处理。

## 数据与事务

项目数据库使用 SQLite，启用 WAL、外键与 busy timeout。连接池上限为 1，使单进程服务中的业务事务顺序执行；事务内不等待引擎或网络。

数据库以 application_id=TINA 和 user_version=1 标明归属及结构。项目表为 project/agent/task/run/report/approval/event/execution_request，辅助表为 node_identity/execution_fact/cancel_request/migration。关联键、检索字段及状态保存在 SQL 列，完整业务对象保存在 JSON body，两者在同一条写入中更新。

建卡、排队、审批创建子卡、报告落库与终审都在事务中追加产品事件，失败时一起回滚。项目事件序号从 1 连续追加。唯一索引约束同一卡片的活跃 Run、同一 Run 的报告和待批计划。看板使用一致的读事务，查询不重算并写入业务状态。

控制面数据库与 tinad 执行日志分别标识归属，不能交换使用。导入旧库时先生成包含已提交 WAL 的快照，再从快照只读导入；目标事务负责冲突检查和重复导入识别。源 ID、引用关系、人工覆盖和事件顺序保留，旧活跃尝试记录中断，不自动重跑。启动、数据库路径和导入操作见 [使用说明](../README.md)。

## 节点身份与执行绑定

每个项目有 Router 和 Worker 配置，身份与配置由控制面保存，注册不要求进程常驻。Agent 绑定指定引擎、tinad、工作目录及模型选项；tinad 的本机授权清单独立维护，见 [本机授权与配置](tinad.md#本机授权与配置)。

未发送的 queued 请求允许补充绑定。已派发、运行或仍待核查清理时禁止修改相关 Agent 的执行配置，保留原节点对账路径。实际派发前校验注册、绑定和容量，再冻结完整 dispatch 及具体执行位置。

节点通过控制面主机上的 POST /api/nodes/enroll 登记 nodeId/name/token，或由控制面启动参数 enroll-config 读取本机私有配置进行登记。同 ID 的不同 token 不覆盖原身份；控制面仅保存 token 的 SHA-256 摘要。

tinad 在 /ws/tinad 握手时携带 Authorization 与 X-Tina-Node。随后注册的 Agent 必须匹配已有项目、角色、引擎及节点绑定，注册不会创建项目或更改绑定。状态中的 ready 表示该节点已完成认证及执行注册。

## 业务规则

- 计划必须有任务、key、标题与验收点；重复 key、缺失依赖和依赖环被拒绝。批准时先分配全部子卡 ID，再建立依赖。
- 需求卡在计划批准后完成。Worker 任务必须来自已批计划；依赖任务经人终审完成后才能派发，同一卡片不能有两个活跃 Run。
- queued、dispatched、running 都是活跃尝试。命令回执不填写 startedAt；收到 activity.started 才记录实际开始。
- 终态 Run 的任务进入评审；blocked 报告保留给人判断。确定性失败且缺少有效报告时，由控制面生成带 generated 标记的兜底报告。
- 人终审针对最近一次已结束尝试的报告，不能在新尝试运行时终审旧报告。状态推导保留 manualOverride。
- 模型输出不能指定权威报告 ID、Run ID 或 generated 标记。已有有效报告不因后续引擎失败被兜底文本覆盖；原生用量单独保存为 engineUsage。

控制面使用 human:local 记录单账号操作来源。项目业务授权与机器所有者授予的执行权限分别校验。

## 派发、事实与取消

待执行记录保存稳定 commandId、Run/Agent 关联与提示词；Run 保存 sessionId/activityId。派发前持久保存完整 dispatch，回传时核验节点、命令、Run、会话、活动和实例。事实去重、业务结果与产品事件同事务提交后才 ack。消息字段及重放规则见 [执行协议](execution-protocol.md#3-消息目录)。

取消请求保存首次请求 ID、操作者、原因和时间，重复请求复用原操作。未发送的请求可以原子撤回；执行中的请求按照 [接受、终态与取消](execution-protocol.md#4-接受终态与取消) 结算。needsReconciliation 和 diagnostic 保存待核查状态及原因，重新对账只询问执行事实。

派活可携带 resumeFromRunId，要求来源为同一卡片上已结束、拥有真实句柄且清理已确认的尝试。新的 Run、命令及活动使用新 ID，沿用逻辑会话与原生句柄；所选 Agent/引擎必须匹配。句柄与恢复能力的约束见 [适配契约](engine-adapter-contract.md#3-身份与持久句柄)。

Router 的显式记忆在控制面保存，并与项目事实一起提供给 Router；项目上下文不依赖某个引擎进程是否仍存活。

## HTTP API

| 入口 | 行为 |
|---|---|
| `GET /api/state` | 项目目录及节点可用性 |
| `POST /api/projects` | `{name}` 创建项目及默认 Router/Worker 配置 |
| `PUT /api/projects/{id}/memory` | `{text}` 更新 Router 显式记忆 |
| `POST /api/nodes/enroll` | `{nodeId,name,token}` 在控制面主机登记节点身份 |
| `GET /api/projects/{id}/agents` | 读取项目 Agent 配置 |
| `PUT /api/projects/{id}/agents/{agent}` | `{name,engineId,nodeId,cwd,model?,options?}` 修改配置与绑定 |
| `GET /api/board?project=...` | 看板、历史 Run、最新报告与待批计划 |
| `GET /api/runs/{id}?project=...` | 指定历史尝试及其报告 |
| `PUT /api/tasks/{id}/assignee` | `{projectId,agentId}` 指派本项目 Worker；存在活跃尝试时拒绝 |
| `POST /api/cards` | `{projectId,title,description?}` 创建需求卡 |
| `POST /api/plans` | `{projectId,taskId}` 排队 Router Run，202 返回 `{run,task}` |
| `POST /api/plans/{id}/decide` | `{projectId,decision}` 批准或拒绝计划 |
| `POST /api/tasks/{id}/dispatch` | `{projectId,resumeFromRunId?}` 校验依赖并排队 Worker Run，返回 202 |
| `POST /api/runs/{id}/cancel` | `{projectId,reason?}` 记录取消请求 |
| `POST /api/runs/{id}/reconcile` | `{projectId}` 请求核查已派发尝试 |
| `POST /api/tasks/{id}/review` | `{projectId,decision,note?}` 人终审 |
| `GET /api/events?project=...` | 产品事件 |
| `GET /api/queue?project=...` | 尚未发送的执行请求 |

decision 为 approved/rejected。变更请求核对项目与目标记录的关联，拒绝跨项目操作和重复活跃请求。请求体上限为 1 MiB。业务结果只能通过经过校验的执行事实进入，不提供制造引擎结果的公共 HTTP 入口。
