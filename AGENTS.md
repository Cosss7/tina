# AGENTS.md

Tina 是一个以任务看板为中心的人机协作平台：人和智能体在同一个看板上推进同一批任务。修改控制面、tinad、共享协议或引擎接入前读 [docs/architecture.md](docs/architecture.md)；文档工作遵循 [docs/AGENTS.md](docs/AGENTS.md)。

## 当前阶段

**H0 已完成**，产出 `0.1.0`：单机跑通"建卡 → Router 拆卡 → 派 worker → 收报告 → 人终审"，真 dsh 手工验收通过。结果与遗留问题见 `docs/h0-report.md`。

**下一阶段是 H1 = `1.0.0`**。按 [docs/next-implementation-plan.md](docs/next-implementation-plan.md) 迁移：P1–P5 已完成协议、控制面业务、Go tinad 闭环与取消，下一步 P6 收尾启动、界面和数据迁移验收。修改项目业务前读 [docs/control-plane.md](docs/control-plane.md)，修改执行接入前读 [docs/tinad.md](docs/tinad.md) 与 [适配契约](docs/engine-adapter-contract.md)。消息规则见 [执行协议](docs/execution-protocol.md)。

“家节点”和“先装回 Effect”属于旧方案；新实现不引入 Effect。Go tinad 已能承载闭环；P6 将恢复 CLI 的一条命令全栈启动。区分已实现阶段与后续规划。

- 动架构或产品方向之前读 `docs/vision.md`。
- 打 tag、改版本号、写发布说明之前读 `docs/versioning.md`。
- `docs/h0.md` 保持原样作为已交付版本的规格，改它等于改历史，除非要修错。

## 代码布局

```
apps/web                 React 前端（Web 与桌面共用）
apps/desktop             Electron 外壳（H0 不做）
apps/cli                 tina 命令（up / attach）
services/control-plane   Go 控制面、SQLite 项目库、业务与持久执行队列
services/tinad           Go 执行服务、fake/dsh 适配器、进程监管与回传日志
packages/protocol        共享 Go 执行消息、JSON v1 编解码和校验
packages/node-runtime    H0 TS 行为基线（生产项目 API 已迁到 Go）
packages/engine-port     H0 引擎接口与 TCP 传输
packages/engine-dsh      H0 进程外 TS 适配器（内部插件尚未实现）
packages/ui-kit          组件库
```

## Commands

```sh
tina up         # 启动持久化控制面，tinad 当前独立配置启动
make verify     # 构建、类型检查和离线测试，进 CI
```
