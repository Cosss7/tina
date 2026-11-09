# AGENTS.md

Tina 是一个以任务看板为中心的人机协作平台：人和智能体在同一个看板上推进同一批任务。Read [docs/architecture.md](docs/architecture.md) before changing `packages/`; follow [docs/AGENTS.md](docs/AGENTS.md) for documentation.

## 当前阶段

**H0 已完成**，产出 `0.1.0`：单机跑通"建卡 → Router 拆卡 → 派 worker → 收报告 → 人终审"，真 dsh 手工验收通过。结果与遗留问题见 `docs/h0-report.md`。

**下一阶段是 H1 = `1.0.0`**，还没开工。动它之前先看 `docs/h0-report.md` 第四节的建议：
把 Effect 装回节点运行时，同时用插件补上会话级取消——这两件事是同一个问题的两面。

- 动架构或产品方向之前读 `docs/vision.md`。
- 打 tag、改版本号、写发布说明之前读 `docs/versioning.md`。
- `docs/h0.md` 保持原样作为已交付版本的规格，改它等于改历史，除非要修错。

## 代码布局

```
apps/web                 React 前端（Web 与桌面共用）
apps/desktop             Electron 外壳（H0 不做）
apps/cli                 tina 命令（up / attach）
services/control-plane   Go 控制面
packages/node-runtime    节点运行时（Effect）
packages/engine-port     引擎端口协议
packages/engine-dsh      dsh bridge 插件
packages/protocol        protobuf（buf 生成）
packages/ui-kit          组件库
```

## Commands

```sh
tina up         # 一条命令起全栈
make verify     # 32 条测试，离线，进 CI
```
