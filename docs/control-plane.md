# 控制面项目服务

> P4 · 2026-11-09 · 项目业务、持久派发与执行事实接收已接通。
> 修改项目 API、数据库、审批、报告或队列时读取本文；总体边界见 [架构](architecture.md)。

## 当前可用范围

只启动 Go 控制面就能创建项目、创建需求卡、查看看板与历史报告、处理已存在的计划审批和终审。Router/Worker 配置与执行位置绑定由控制面保存，节点离线不影响这些项目操作。

请求拆卡或派活会创建 Run 和待执行记录，返回 HTTP 202。已绑定且在线的 Go tinad 自动接受派发，控制面接收计划/报告并维护审批与终审。没有可用执行位置时保持 queued。原 TS 服务的隔离测试仍作为 H0 行为基线，不再承载新项目 API。

## 启动与数据路径

首次准备依赖：`pnpm install`，以及 `go -C services/control-plane mod download`。随后运行：

```sh
make verify
make up
# 或构建后自定义地址与库位置
node apps/cli/src/main.ts up --addr 127.0.0.1:7801 --db .tina/control-plane.db
```

默认使用仓库下 `.tina/control-plane.db`，CLI 输出实际路径。可设置 `TINA_CONTROL_DB`，显式 `--db` 优先。直接启动二进制时使用 `-db`，相对路径以进程工作目录为准。`:memory:` 只用于临时体验或测试，退出即丢失数据。CLI 不再自动启动 H0 TS 节点；`--no-node` 保留为兼容参数。

控制面首次打开空库时默认初始化“网站改版”和“移动端”两个演示项目，初始化与节点注册无关。直接启动二进制可用 `-seed-demo=false` 关闭初始化，随后通过 Web 的“＋项目”或项目 API 创建项目。

`TINA_DB` 仍是 H0 节点的旧数据库配置，不作为新控制面库位置。新服务拒绝自动使用已有 H0 或无法识别的库，防止向旧库添加新结构。旧文件保留，导入工具在 P6 实现；当前不自动迁移历史数据。

`make demo` 现在演示无节点建卡与排队；可用 `TINA_CONTROL_DB=.tina/demo.db make demo` 保存记录。演示脚本自行启动并停止控制面，端口已占用时拒绝运行。它不生成虚构的模型结果。

## 数据与事务

实现位于 `services/control-plane/internal/project`，使用 `database/sql` 和固定版本 `modernc.org/sqlite v1.39.1`，无需 cgo。SQLite 使用 WAL、外键与 busy timeout。连接池上限为 1，让当前单进程服务中的业务事务顺序执行；不在事务内等待引擎或网络。

数据库以 `application_id=TINA` 和 `user_version=1` 标明归属及当前结构。表为 `project/agent/task/run/report/approval/event/execution_request`。关联键、检索字段、Run/审批/队列状态保存在 SQL 列，完整业务对象保存在各行的 JSON body；二者在同一条写入中更新。唯一索引限制同一卡片的活跃 Run、同一 Run 的报告、待批计划和项目事件序号。

每个项目默认有 Router 和 Worker 两个 Agent 配置，初始未绑定节点。它们是项目成员配置，不是已启动的进程。可修改名称、引擎、工作目录和节点绑定；未发送的 queued 请求允许补充绑定，已经派发或运行时需要先对账。P3 在实际派发时校验授权与可用性，并把选定节点和执行实例关联到 Run；P2 不把空节点身份伪装成有效的跨机器 dispatch。

建卡、排队、审批创建子卡、报告落库与终审均在事务中追加产品事件；任何一步失败都会回滚业务变更和事件。事件序号按项目从 1 连续追加。看板使用一致的只读事务，不在 GET 请求中偷偷重算并写入状态。

待执行记录保存稳定 commandId、Run/Agent 关联与完整提示词；Run 同时保存 sessionId/activityId。派发前冻结完整 dispatch 并标为 dispatched，发送失败不自动重排。回传按稳定消息 ID 和实例序号去重，业务结果、执行事实与产品事件同事务提交之后才 ack。重启保留原记录与 ID；已派发/运行的尝试先对账，未知结果保持待核查，不自动重跑。P5 增加取消和异常处置入口。

节点先在控制面主机通过 `POST /api/nodes/enroll` 登记 `{nodeId,name,token}`，同 ID 的不同 token 不可覆盖。控制面只保存 token 的 SHA-256 摘要。tinad 在 `/ws/tinad` 握手时携带 Authorization 和 X-Tina-Node，随后注册的 Agent 必须与控制面已有项目、角色、引擎及节点绑定一致。注册不创建项目，也不改变绑定。Web 状态中 ready 表示已通过执行接线的节点，旧 H0 心跳的 online 不代表可以派发。

## 业务规则

- Router 输出由控制面解析和校验，不能直接改项目。计划必须有任务、key、标题与验收点；重复 key、缺失依赖和依赖环都被拒绝。批准时先分配全部子卡 ID，因此向后引用的依赖也会保留。
- 需求卡计划批准后完成。Worker 任务必须来自已批计划；依赖任务经人终审完成后才能派发，同一卡片不能同时有两个活跃 Run。
- queued、dispatched、running 都是活跃业务尝试；queued 不填写 startedAt，也不产生 run.started 事件。界面同时显示等待执行。
- 有终态报告的任务进入评审中；blocked 报告仍待人判断，不能仅因 blocked 自动判为执行失败。无有效报告的确定性执行失败生成醒目标记的兜底报告。
- 人终审针对最近一次已结束尝试的报告，运行中不能终审旧报告。状态推导保留已有 manualOverride 数据。模型输出不能指定权威报告 ID、Run ID 或 generated 标记。

`RecordOutcome` 是内部业务入口，没有对应的公共 HTTP 接口。执行接线先校验节点、命令、Run、会话、活动、实例和终态证据，再在去重事务中更新业务。等待超时或断连不能制造失败或取消终态。已有有效 Worker 报告不因引擎随后失败被兜底文本覆盖；原生用量单独保存在 engineUsage 中。当前仍沿用 H0 的单账号 `human:local` 操作者，完整多用户权限不在本次迁移范围内。

## HTTP 入口与验证

| 入口 | 行为 |
|---|---|
| `GET /api/state` | 控制面的项目目录与独立的节点可用性；不再返回 homeNodeId 或“项目在线” |
| `POST /api/projects` | `{name}` 创建项目及默认 Router/Worker 配置 |
| `PUT /api/projects/{id}/memory` | `{text}` 更新 Router 显式记忆，派发 Router 时与项目事实一起提供 |
| `POST /api/nodes/enroll` | 本机管理入口，预登记节点身份及 token |
| `GET /api/projects/{id}/agents` | 读取项目 Agent 配置 |
| `PUT /api/projects/{id}/agents/{agent}` | `{name,engineId,nodeId,cwd,model?,options?}` 修改已有 Agent 的配置/绑定 |
| `GET /api/board?project=...` | 持久看板、历史 Run/报告与待批计划 |
| `POST /api/cards` | `{projectId,title,description?}` 创建需求卡 |
| `POST /api/plans` | `{projectId,taskId}` 排队 Router Run，202 返回 `{run,task}` |
| `POST /api/plans/{id}/decide` | `{projectId,decision}` 批准或拒绝待批计划 |
| `POST /api/tasks/{id}/dispatch` | `{projectId}` 校验依赖并排队 Worker Run，返回 202 |
| `POST /api/tasks/{id}/review` | `{projectId,decision,note?}` 人终审 |
| `GET /api/events?project=...` | 读取项目产品事件 |
| `GET /api/queue?project=...` | 查看尚未发送的执行请求 |

decision 为 approved/rejected。所有变更均核对项目与目标记录的关联；错误决策、跨项目操作、重复活跃请求与重复审批会被拒绝。请求体限制 1 MiB。旧 `/ws/node` 当前仅保留在线状态观测；收到的项目声明不能创建或重新归属项目，也没有业务 RPC 转发。

`make verify` 验证 Go 业务与 API、P1 契约、真实进程重启、无节点排队演示及保留的 H0 隔离场景。重点覆盖事务回滚、依赖校验、重复请求竞争、恢复后的待批计划/报告与跨项目隔离。真实引擎与完整跨机器闭环不属于本阶段通过的验收。
