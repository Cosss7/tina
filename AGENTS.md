# AGENTS.md

Tina 是一个以任务看板为中心的人机协作平台：人和智能体在同一个看板上推进同一批任务。修改控制面、tinad、共享协议或引擎接入前读 [docs/architecture.md](docs/architecture.md)；文档工作遵循 [docs/AGENTS.md](docs/AGENTS.md)。

## 当前阶段

**H0 已完成**，产出 `0.1.0`：单机跑通"建卡 → Router 拆卡 → 派 worker → 收报告 → 人终审"，真 dsh 手工验收通过。结果与遗留问题见 `docs/h0-report.md`。

**下一阶段是 H1 = `1.0.0`**。先按 [docs/next-implementation-plan.md](docs/next-implementation-plan.md) 从 P1 开始迁移：控制面维护项目权威数据，Go `tinad` 管理执行，普通适配器用 Go 内部模块。适配、取消与恢复行为见 [docs/engine-adapter-contract.md](docs/engine-adapter-contract.md)。

“家节点”和“先装回 Effect”属于旧方案；新实现不引入 Effect。H0 代码尚未完成架构迁移，不能将目标文档视为当前功能已实现。

- 动架构或产品方向之前读 `docs/vision.md`。
- 打 tag、改版本号、写发布说明之前读 `docs/versioning.md`。
- `docs/h0.md` 保持原样作为已交付版本的规格，改它等于改历史，除非要修错。

## 代码布局

```
apps/web                 React 前端（Web 与桌面共用）
apps/desktop             Electron 外壳（H0 不做）
apps/cli                 tina 命令（up / attach）
services/control-plane   Go 控制面
services/tinad           Go 执行服务与普通适配器（待按计划建立）
packages/protocol        共享执行协议（P1 建立，线格式与生成工具按实施定义）
packages/node-runtime    H0 TS 节点（业务与执行职责待迁移）
packages/engine-port     H0 引擎接口与 TCP 传输
packages/engine-dsh      H0 进程外 TS 适配器（内部插件尚未实现）
packages/ui-kit          组件库
```

## Commands

```sh
tina up         # 一条命令起全栈
make verify     # 构建、类型检查和离线测试，进 CI
```
