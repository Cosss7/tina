# AGENTS.md

Tina 是一个以任务看板为中心的人机协作平台：人和智能体在同一个看板上推进同一批任务。

## 当前阶段

**H0 已完成**，产出 `0.1.0`：单机跑通"建卡 → Router 拆卡 → 派 worker → 收报告 → 人终审"，真 dsh 手工验收通过。结果与遗留问题见 `docs/h0-report.md`。

**下一阶段是 H1 = `1.0.0`**，还没开工。动它之前先看 `docs/h0-report.md` 第四节的建议：
把 Effect 装回节点运行时，同时用插件补上会话级取消——这两件事是同一个问题的两面。

- 动架构或产品方向之前读 `docs/vision.md`。
- 打 tag、改版本号、写发布说明之前读 `docs/versioning.md`。
- `docs/h0.md` 保持原样作为已交付版本的规格，改它等于改历史，除非要修错。

## 文档

文档分两层：**决策记在 `.agents/notes/`，`docs/` 只放当前共识的投影。** 规则与归档方式见 `docs/README.md`。

| 文档 | 回答什么 | 什么时候读 |
|---|---|---|
| `docs/README.md` | 文档规则：什么放哪、过时怎么归档 | 新增、移动、归档文档之前 |
| `docs/vision.md` | 产品最终形态：故事、架构、技术选型 | 改动涉及架构或产品方向时。它是定稿，改它要先讨论 |
| `docs/h0.md` | 这一版写什么、怎么算完成、故意简化了什么 | 实现 H0 之前，以及每次不确定"这个要不要做"时 |
| `docs/versioning.md` | 版本号、tag、发布流程 | 打 tag、改版本号、写发布说明时 |
| `docs/h0-report.md` | H0 的验收结果与遗留问题 | 想知道"这一版到底验出了什么"时 |
| `.agents/notes/proposed/2026-11-05-design-log.md` | D1–D63 的结论、被推翻的结论、判断集 J1–J7 | 想查某条设计为什么是这样，或准备推翻某条时 |
| `.agents/notes/proposed/2026-11-06-m0.1-bridge-spike.md` | 真 dsh 实测：协议能做什么、中断是什么语义、事件长什么样 | 写引擎端口、dsh bridge，或怀疑"dsh 能不能……"时 |

## 代码布局

目标布局如下（已落地 `apps/cli`、`apps/web`、`services/control-plane`、`packages/engine-port`、`packages/engine-fake`、`packages/node-runtime`；`packages/engine-dsh` 也已落地）：

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
- 改一条决策：先写进 `.agents/notes/proposed/`，代码落地后移到 `implemented/`，再改 `docs/` 里受影响的投影。
- TypeScript 没有构建步骤：Node 直接跑 `.ts`，所以 import 要带 `.ts` 后缀，且不能用 enum / 参数属性（`erasableSyntaxOnly` 会拦）。
- 不把本机的绝对路径写进仓库。

## 验证

验证入口是 `make verify`：构建前端 + 构建控制面 + 类型检查（`tsc -p`）+ 测试（`go test` 与 `node --test`，含真控制面真节点真 fake engine 的端到端）。先 `pnpm install` 一次。

本地起全栈用 `make up`（或 `node apps/cli/src/main.ts up`），打开 `http://127.0.0.1:7801`；想把闭环走一遍用 `make demo`。两条硬性要求：

- `make verify` 必须离线可跑：不访问网络、不调用真实模型，引擎一律用 fake。
- 真实模型的验证只出现在手动 demo 里，不进 CI。
