# 执行协议 v1

> P6 · 2026-11-10 · 已有 Go 消息、编解码器、网络接线与执行验收。
> 修改控制面与 tinad 的消息、重放、对账或适配调用前读取本文。
> 数据边界见 [架构](architecture.md)，操作语义见 [适配契约](engine-adapter-contract.md)。

本协议只传递执行请求和事实。P1–P6 已实现控制面业务、带节点凭据的 WebSocket 执行接线、tinad 持久回传、引擎监管、取消、恢复与异常对账，见 [控制面项目服务](control-plane.md) 与 [tinad](tinad.md)。没有已绑定的执行节点时请求保持 queued。

## 1. 数据边界

| 所有者 | 对象与最小关联 |
|---|---|
| 控制面 | Project、项目成员与 Agent 配置/绑定；Task 及依赖；Plan 与 Approval；Run 与派发命令；Report；产品 Event；Router 显式记忆 |
| tinad | 本机引擎和可用能力；Session、原生 Handle、执行 Instance、Activity；命令回执和待确认执行事实 |
| 引擎 | 原生上下文、转录与可恢复历史；tinad 使用其真实句柄 |

Task 可有多次 Run；一次 Run 对应一个业务尝试与一份最终报告。命令、活动、会话均有独立 ID。控制面保存业务对象及与执行的关联，tinad 不保存另一份权威 Task/Approval/Report，也不解析计划或报告来修改项目。

`Execution` 为所有执行命令、回执和事实提供六个必填关联：`projectId`、`runId`、`agentId`、`nodeId`、`sessionId`、`activityId`。授权来自经过认证的绑定和本机策略，不能把消息中的 ID 当作授权证明。注册报告已有身份及可用性，不创建项目，也不擅自修改成员角色。

`SessionHandle` 包含 `engineId`、`sessionId`、`nativeId`，可带非敏感 `metadata`。`instanceId` 标识本次运行时实例；恢复可沿用会话和原生句柄，必须产生新的执行实例。Run、进程 PID 和引擎品牌都不能充当持久会话身份。

## 2. 消息封装

共享实现位于 `packages/protocol`，使用独立 Go module，标准库 JSON，无 protobuf 生成步骤。线格式不含 Go 类型信息。每次编码产生一个 UTF-8 JSON 对象；网络连接与帧边界在 P3 接线时确定，编解码器不打开端口。

```json
{
  "version": 1,
  "id": "dispatch-worker-1",
  "type": "dispatch",
  "payload": {
    "commandId": "command-worker-1",
    "execution": {
      "projectId": "project-1", "runId": "run-worker-1",
      "agentId": "worker-1", "nodeId": "node-1",
      "sessionId": "session-worker-1", "activityId": "activity-worker-1"
    },
    "config": {"engineId": "fake", "cwd": "/work/tina"},
    "input": "执行已批准的任务并返回报告。"
  }
}
```

`id` 是这条消息的稳定回传/去重身份，补传保留原值；`commandId` 是操作的幂等身份。同一命令的回执、进度和结果使用各自的消息 ID，均关联原命令 ID。取消和交互回复使用新的命令 ID，并指向已有活动。

v1 拒绝未知版本、未知消息、未知字段、空身份、空/null payload 及尾随第二个 JSON 值；字段采用下述大小写。新增字段或消息时升级协议版本，避免旧实现静默忽略。`options` 和交互 `answer` 是显式开放的 JSON；普通字符串、整数、布尔值不携带引擎 SDK 对象。

## 3. 消息目录

方向中的 CP 指控制面，N 指 tinad。每条均包含第 2 节的封装，Go 字段的 JSON 名称和校验以 `packages/protocol/messages.go`、`types.go` 为准。

| type | 方向 | payload |
|---|---|---|
| `register` | N → CP | `nodeId/name/version/maxConcurrent`，`engines[]` 的 `id/version?/capabilities`，`agents[]` 的 `id/projectId/role/engineId/configRef?`；role 为 router 或 worker。凭据不随注册广播 |
| `heartbeat` | N → CP | `nodeId`；连接时效由接收端记录，只改变可用性 |
| `dispatch` | CP → N | `commandId/execution/config/input`，可选 `resume`；config 包含 `engineId/cwd` 和可选 `model/systemPrompt/options`，由 tinad 校验本机授权 |
| `input` | CP → N | `commandId/execution/instanceId/text`；向明确的现存实例提交下一活动，不能重叠执行或暗中创建新实例 |
| `cancel` | CP → N | `commandId/execution/reason`，可选 `instanceId`；已知实例必须指定，尚未启动时可按已登记的活动撤销 |
| `respond` | CP → N | `commandId/execution/instanceId/requestId/answer`；回答引擎授权/提问，与项目计划审批分开 |
| `receipt` | N → CP | `commandId/execution/admission`，可选 `instanceId/failure/stop`；见第 4 节 |
| `event` | N → CP | `execution/instanceId/commandId/seq/occurredAt/kind` 和对应载荷；见下文 |
| `result` | N → CP | 同样的关联、序号与时间，`state`，可选 `output/failure/usage/handle/stop` |
| `ack` | CP → N | `nodeId/messageId`；表示已持久保存某条回传消息，不能代替引擎接受回执 |
| `reconcile.request` | CP → N | `nodeId/known[]`；每项含 `execution/instanceId?/lastSeq` |
| `reconcile.response` | N → CP | `nodeId/observations[]`；每项含上述游标、`state` 和可选 `result` |

事件 kind 与载荷：`session.opened` 可带真实 `handle`，无持久句柄时不编造；`activity.started` 无附加载荷；`message` 带 `text`；`tool.call/tool.result` 带 `tool`；`usage` 带非负 `inputTokens/outputTokens`；`interaction.request` 带 `interaction{requestId,kind,prompt}`；`stop` 带停止证据。每条只能带与 kind 对应的载荷。`interaction.kind` 为 `approval` 或 `question`。工具调用至少包含 `callId/name`，结果至少包含 `callId`，可带 `input/output/isError`。

每个执行实例的 event 与 result 共用从 1 开始、严格递增的 `seq`，跨活动不重置；重放保留原序号、消息 ID 和时间。接收端按节点、实例、序号去重并核验活动关联，不能把 `occurredAt` 当作跨机器排序依据。对账游标按活动记录最近事实，可存在序号间隔。需要重放且尚未 ack 的记录必须持久保留；这些持久性由 P3 的回传日志实现，P1 codec 只验证单条消息。

## 4. 接受、终态与取消

| admission | 含义 |
|---|---|
| `admitted` | tinad 已接纳待确认，尚无引擎接受证据 |
| `accepted` | 引擎按其协议确认接受，必须携带 `instanceId`；尚不证明活动开始或完成 |
| `rejected` | 明确未接受，必须给出 `failure{code,message}` |
| `unknown` | 接受与否无法证明，必须给出原因，等待对账 |

`activity.started` 才是开始事实，`result` 才是活动终态。每个活动最多一个终态。`state` 为 `completed/failed/cancelled`；failed 必须保留失败原因。`output` 是原始模型输出，可以是计划或报告的 JSON 文本；由控制面解析校验，执行完成不等于业务报告通过，也不等于卡片终审完成。

`StopEvidence` 分别表达 `mode`（none/turn/process）、`requested`、`activityStopped`、`sessionUsable`、`root` 和 `workload`，可带诊断 `detail`。root/workload 为 `live/unproven/exited/not_applicable`。`requested` 表示已向目标发出请求，不证明停止。`not_applicable` 只用于确实没有对应受管进程的情况，不能用于掩盖观测缺失。

取消终态必须证明活动已停止，且其工具工作负载已退出或确实不适用；process 降级还要求父进程退出或不适用，且不能宣称会话仍可用。turn 取消允许父进程继续存活。仅发信号、收到回执、等待超时或 WebSocket 断开，都不足以产生 cancelled。

原生活动完成先赢得终态时，迟到取消不改写 completed；随后清理错误用独立 `stop` 事件记录。completed 可同时携带未证实清理的信息，这不抹掉已经观测到的模型结果，也不允许未经核查直接复用仍占用的执行资源。

## 5. 重复、断连与恢复规则

| 情况 | 处理规则 |
|---|---|
| 同一 commandId、相同完整请求 | 返回已登记回执/事实，不提交第二次输入；命令记录须先于外部副作用持久建立 |
| 同一 commandId、不同请求 | 拒绝身份冲突，不覆盖先前执行；重试业务尝试使用新的 Run 和命令 |
| 引擎可能已接受但回执丢失 | 保留 unknown/待确认并对账；恢复传输不等于允许重新执行命令 |
| 旧 activity/instance 的取消迟到 | 只查询或处理原目标，不能取消新活动或新实例 |
| 控制面重启/tinad 离线 | 项目历史可用，运行记录等待对账；不推定引擎停止，不自动重跑 |
| 对账没有相关记录 | 返回 unknown，不能把“没有本地记录”当作未执行证明 |
| 对账返回终态 | 必须附可校验的原 result，关联、状态和序号与快照一致，再按正常结果路径去重落库 |
| 恢复历史缺失、工作环境不匹配或能力不支持 | 明确拒绝；新建会话需要独立决定，不能冒充原生恢复 |

对账的非终态为 `unknown/queued/running`；running 必须标明实例，已知序号也必须标明实例。对账响应应覆盖请求中的活动，额外回报本机未确认活动；CP 将缺少响应的条目保留为待对账，不能按“未出现”结算。禁止凭内存中的 unknown 状态承诺跨重启幂等。

## 6. 内部 Go 适配接口

实现入口为 `services/tinad/internal/engine`，使用内部模块调用，不增加自定义 TCP 适配层。

| 接口 | 操作 |
|---|---|
| `Driver` | `Definition`、`Probe`、`Create`；按引擎或协议族复用 |
| `Resumer`（可选） | `Resume`；注册时校验声明，运行时检查真实句柄及可用能力 |
| `Session` | `Info`、`Submit`、`Updates`、`Close`；每个实例维护隔离状态 |
| `Interrupter`（可选） | `Interrupt(ctx, activityID)`；调用者已选择准确实例，不能以旧活动取消新活动 |
| `Responder`（可选） | `Respond(ctx, requestID, answer)`；调用者先校验实例、活动和待答请求 |

`Capabilities` 目前包含 resume、interrupt（none/turn/process）、respond。静态声明 → Probe → 会话只能收窄；注册表拒绝缺少恢复入口的驱动，创建时拒绝缺少中断/交互入口的会话，并尝试释放无效运行时。恢复返回不同原生 ID 也会被拒绝。运行中补充输入尚未发布为能力；当前 fake 对忙碌会话明确拒绝。

Submit 的 context 约束本次调用等待，不承担整个活动生命周期；取消活动必须走显式 Interrupt。Close 释放运行时，不删除原生历史。恢复后的新实例不能沿用旧实例的调用或事件。

fake 可人工触发 Complete，支持多个隔离会话、命令去重、当前回合取消、Close 后 Resume。同一 Driver 内保存内存历史；新 Driver 不持有旧历史，必须拒绝恢复。它没有 OS 子进程，进程证据为 not_applicable，不能代替真实 dsh 的进程监管验收。命令去重也仅覆盖当前内存实例；跨重启日志在 P3 实现。

## 7. 完整业务路径与样例

可机器读取的 v1 消息见 [`flow.json`](../packages/protocol/testdata/flow.json)，包含一次注册、Router dispatch/result、Worker dispatch/result。计划和报告文本沿用 H0 的字段；这份样例不是新的业务 API。

| 顺序 | 控制面的业务工作 | 通过 tinad 的执行消息 |
|---|---|---|
| 1 | 创建项目、成员和需求卡，保存上下文 | 无建卡 RPC；register 只报告 Router/Worker 可用性 |
| 2 | 建立 Router Run 与幂等派发记录 | dispatch → receipt → activity.started → result（计划建议） |
| 3 | 校验计划与依赖，保存待批计划；人批准后创建子任务 | 无批准 RPC |
| 4 | 建立 Worker Run，检查授权与依赖 | dispatch → receipt → activity.started → result（报告文本） |
| 5 | 校验报告，去重落库，推导评审中；人终审 | 无终审 RPC；持久保存执行事实后 ack |
| 6 | 若执行中请求取消，保存取消请求 | cancel → receipt；获得停止证据才 result(cancelled)，未知则 reconcile |

P1 的自动验证包括消息双向编解码、非法字段与关联拒绝、未知投递和对账状态、停止证据校验、fake 多会话隔离、恢复拒绝、回执丢失后的重复命令、完成与取消竞争，以及 Router/Worker 经 JSON → 同一适配接口 → JSON 返回结果。真实业务事务和跨进程闭环在 P2–P4 验证。

运行 `make verify` 同时检查 H0 与新 Go 契约；单独运行可用 `go -C packages/protocol test ./...` 和 `go -C services/tinad test ./...`。
