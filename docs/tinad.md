# tinad 执行服务

> P6 · 2026-11-10 · Go 执行服务、真实 dsh、启动、授权热更新与跨环境验收已落地。
> 配置或修改 tinad 的接入、引擎和本机权限时读取本文；消息规则见 [执行协议](execution-protocol.md)。

tinad 只执行已授权的 Agent 请求，不持有项目业务库。Go 二进制由 `make build` 生成；`make up` 同时启动控制面与本机 tinad。独立部署使用 `.build/tinad -config <本机配置文件>`，Windows 文件名为 tinad.exe。

配置包含 nodeId、name、url（ws/wss 执行端点）、私有 token、database、maxConcurrent、roots，以及本机允许的 agents（id/projectId/role/engineId）。token 只出现在本机私有配置及握手 Authorization 头中，不进入注册消息或普通日志。根目录经过真实路径解析；请求中的工作目录必须处于授权根目录内，Agent、项目及引擎的绑定也必须匹配本机清单。

roots 限制工作目录选择，不限制工具进程以当前操作系统用户身份访问其他目录。独立部署前，在控制面登记相同身份并配置 Agent 绑定；本机清单必须明确列出允许的项目与 Agent。示例中的 token 需要换成自己生成的至少 32 字符随机值：

```json
{
  "nodeId": "server-1", "name": "Server Worker",
  "url": "ws://CONTROL_HOST:7801/ws/tinad",
  "token": "REPLACE_WITH_PRIVATE_RANDOM_TOKEN_32_CHARS_OR_LONGER",
  "database": "/var/lib/tina/tinad.db", "maxConcurrent": 2,
  "roots": ["/srv/work"],
  "agents": [{"id": "worker-proj-website", "projectId": "proj-website", "role": "worker", "engineId": "fake"}]
}
```

本机 roots 和 agents 的文件变更会热更新并重新注册，不取消已授权的活动；身份、连接或引擎启动配置变化需要重启。CLI 默认 autoProjects=true，仅本地启动器同步未绑定项目的具体授权；手工配置和独立 tinad 不自动授权新项目。

执行库有独占文件锁，同一份日志不能同时交给两个 tinad。退出时先停止本机活动、持久保存结果，在控制面连接仍可用时最多补传 3 秒；未获确认的数据留待重连。CLI 用私有配置的 stopping 标志触发同一关闭流程，强制结束后仍按未知结果对账。

执行数据库与控制面库独立，使用 TINJ application_id，只有 command 与 outbox 表。命令在创建或提交引擎之前持久登记；重复相同命令只回放已知回执，内容冲突被拒绝。回传消息保留稳定 ID，直到控制面持久确认后删除 outbox 记录。命令账本保留，因此清空已确认 outbox 不会失去幂等记录。

连接断开不取消活动。服务重启后，未取得终态的旧命令变为 unknown，并占用待核查容量；不会静默重放输入或创建替代会话。未知执行必须先对账，不能因超时自动重新派发。关闭引擎后保留真实历史，恢复必须使用引擎原生身份；数据库不存另一份原生转录。

## 引擎接入

- fake：可人工控制的会话实现和离线 scripted 模式。支持隔离会话、回合级中断和同一 Driver 内的原生句柄恢复；重启后不伪装成仍有内存历史。scripted 的计划/报告仅是演示模型输出，项目规则仍在控制面校验。
- dsh：Go 模块直接驱动 stdio JSON-RPC，独立引擎进程。配置 `dsh.command` 为可执行文件及独立参数数组（例如 node.exe、dsh 的 lib/bin.js），可设置 profile/provider/model/home。命令与持久目录来自本机配置，控制面的请求不能替换可执行文件或凭据。

dsh 在原生事件确认后发布真实句柄，恢复调用 session/resume 并校验原持久目录和工作目录；缺失历史明确失败。初始化时以缺少必要参数的只读请求检查恢复方法是否存在，不创建会话、不发送输入。已验收 0.2.0-rc.2 缺少该方法：句柄记录 resume=false，恢复明确拒绝；0.2.2-rc.1 支持原生恢复。公开 SDK 的活动级中断未接入，因此当前 dsh 明确声明 process 取消，保留历史但释放运行时，不发布 turn 取消能力。

sdk-minimal profile 默认不一定加载本地凭据服务。需要读取 DSH_HOME/.credentials.yaml 时，按对应 dsh 版本的官方文档在 profile 的 cordis.patch.yml 插入 `@deepseek-ai/dsh-credentials-local`；不要把抽象的 credentials 包当作提供者。也可通过原生支持的环境变量提供认证。该配置属于本机 dsh，不发到控制面。sdk-minimal 的会话压缩配置应与其持久目录匹配；避免与使用不同压缩格式的桌面历史混用。验收使用独立原生目录，未改用户桌面 profile。

公共进程监管在 Windows 上先挂起创建引擎，再加入带 kill-on-close 的 Job Object 后恢复执行，因此工具后代从启动起就在同一受管范围。停止后分别观察根进程和 Job 的活跃进程数。Unix 使用独立进程组并检查组退出；不把发出信号或等待超时当作停止证明。进程退出与活动终态分别处理，不能用清理失败覆盖已完成结果。

## 验证与阶段边界

离线 Go 测试使用真实子进程验证 stdio、原生持久恢复、进程级取消及父/子进程清理；使用真实 WebSocket 断连验证同一结果 ID 补传、命令不重放、本机授权和未知投递。真实模型与跨环境结果另记在 [迁移报告](architecture-migration-report.md)。

P3–P5 已接通派发、取消与异常对账。取消命令单独持久登记，只有对应活动的结果已持久保存后才确认停止操作。构造会话期间收到取消会阻止后续提交输入；停止不可证实时回传 unknown，不合成 cancelled。连接丢失不取消现有活动，未确认的执行与清理仍占用待核查容量。

fake 的 turn 取消可保留有限数量的空闲会话，后续 input/显式 resume 继续同一实例；迟到取消只查询原活动。配置变化或空闲实例被释放时，以原生句柄恢复并返回新的实例 ID。dsh 先尝试原生 shutdown，再用公共进程监管确认根进程及工具退出；该行为明确报告 process 降级。dsh 句柄同时核对原持久目录与工作目录，不能把换环境后的新建伪装为恢复。

可选交互接口仍按能力发布，不对未实现能力返回空成功。当前未接入 dsh 的提问/审批回复或会话内取消；后续按原生协议的实际支持补充，不影响这次已声明的 process 降级验收。
