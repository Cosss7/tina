# AGENTS.md

Tina 是一个以任务看板为中心的人机协作平台：人和智能体在同一个看板上推进同一批任务。修改控制面、tinad、共享协议或引擎接入前读 [docs/architecture.md](docs/architecture.md)；文档工作遵循 [docs/AGENTS.md](docs/AGENTS.md)。

## 当前阶段

**H0 已完成**，产出 `0.1.0`：单机跑通"建卡 → Router 拆卡 → 派 worker → 收报告 → 人终审"，真 dsh 手工验收通过。结果与遗留问题见 `docs/h0-report.md`。

**H1 前置架构迁移 P1–P6 已实现**，阶段与验收见 [docs/next-implementation-plan.md](docs/next-implementation-plan.md) 和 [迁移报告](docs/architecture-migration-report.md)。这不表示 H1 `1.0.0` 已发布。修改项目业务前读 [docs/control-plane.md](docs/control-plane.md)，修改执行接入前读 [docs/tinad.md](docs/tinad.md) 与 [适配契约](docs/engine-adapter-contract.md)。消息规则见 [执行协议](docs/execution-protocol.md)。

控制面持有项目权威数据，Go tinad 承载 Router/Worker 执行，普通适配器为内部 Go 模块。当前不引入 Effect。启动、配置与旧库导入见 [README.md](README.md)。

## 代码布局

```
apps/web                 React 前端（Web 与桌面共用）
apps/desktop             Electron 外壳（预留）
apps/cli                 tina up 与完整闭环 demo
services/control-plane   Go 控制面、SQLite 项目库、业务与持久执行队列
services/tinad           Go 执行服务、fake/dsh 适配器、进程监管与回传日志
packages/protocol        共享 Go 执行消息、JSON v1 编解码和校验
packages/node-runtime    保留的 H0 纯业务基线与跨进程验收，旧生产入口已移除
packages/engine-port     H0 测试接口与 TCP 传输
packages/engine-fake     H0 测试场景
packages/ui-kit          组件库（预留）
```

## Commands

```sh
make up         # 构建并启动持久化控制面 + 本机 Go tinad（默认 fake）
make demo       # 完整的离线演示，数据隔离在 .build
make format     # Go 用 gofmt，前端、脚本与配置用 Prettier
make verify     # 格式检查、构建、类型检查和离线测试，进 CI
```

## 提交前检查

修改代码后、提交前必须执行 `make format`，并确保 `make format-check` 通过；格式化产生的改动必须一并提交。统一使用仓库锁定的 Prettier 与 Go 自带的 gofmt，具体规则见 `.prettierrc.json` 和 `.prettierignore`。

`make verify` 和 CI 必须通过格式检查；格式不符合要求时不得提交或合并。
