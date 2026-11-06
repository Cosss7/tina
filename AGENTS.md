# AGENTS.md

Tina 是一个以任务看板为中心的人机协作平台：人和智能体在同一个看板上推进同一批任务。

## 当前阶段

**H0**（开发里程碑 0），目标产物 `0.1.0`：单机跑通"建卡 → Router 拆卡 → 派 worker → 收报告 → 人终审"。

- 实现 H0 的任何部分之前，读 `docs/h0.md`，它是这一版的规格、验收标准和简化清单。
- 打 tag、改版本号、写发布说明之前，读 `docs/versioning.md`。

## 文档

文档分两层：**决策记在 `.agents/notes/`，`docs/` 只放当前共识的投影。** 规则与归档方式见 `docs/README.md`。

| 文档 | 回答什么 | 什么时候读 |
|---|---|---|
| `docs/README.md` | 文档规则：什么放哪、过时怎么归档 | 新增、移动、归档文档之前 |
| `docs/vision.md` | 产品最终形态：故事、架构、技术选型 | 改动涉及架构或产品方向时。它是定稿，改它要先讨论 |
| `docs/h0.md` | 这一版写什么、怎么算完成、故意简化了什么 | 实现 H0 之前，以及每次不确定"这个要不要做"时 |
| `docs/versioning.md` | 版本号、tag、发布流程 | 打 tag、改版本号、写发布说明时 |
| `.agents/notes/2026-11-05-design-log.md` | D1–D47 的结论、被推翻的结论、判断集 J1–J7 | 想查某条设计为什么是这样，或准备推翻某条时 |

## 代码布局

目标布局如下（H0 尚未开始写代码）：

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

## 约定

- 提交信息用中文，`<type>: <一句话>`，正文说明为什么。type 取 `feat` / `fix` / `docs` / `refactor` / `test` / `chore`。
- 改一条决策：先写进 `.agents/notes/` 的决策记录，再改 `docs/` 里受影响的投影。
- 不把本机的绝对路径写进仓库。

## 验证

验证命令在 `docs/h0.md` 里定义，当前仓库还没有可运行的代码。两条硬性要求：

- `make verify` 必须离线可跑：不访问网络、不调用真实模型，引擎一律用 fake。
- 真实模型的验证只出现在手动 demo 里，不进 CI。
