# Tina

以任务看板为中心的人机协作平台。Go 控制面保存项目与审批，Go tinad 在本机或服务器承载 Router/Worker。架构图见 [docs/architecture.md](docs/architecture.md)。

## 本地运行

准备 Node.js 24+、pnpm 12、Go 1.24+ 和 make；Windows 使用 MSYS2 Bash，并让 Go、Node.js 可从 PATH 找到。首次下载依赖需要联网：

```sh
pnpm install
go -C services/control-plane mod download
go -C services/tinad mod download
make up
```

浏览器打开 <http://127.0.0.1:7801>。默认 fake 引擎可体验建卡、Router 拆卡、批计划、派 Worker、报告和终审。它输出固定演示内容；真实 dsh 需要下面的本机配置。第二张演示任务依赖第一张，先终审第一张才能派发第二张。

```sh
make demo      # 自动演示完整闭环；数据保留在输出的 .build/demo-* 目录
make verify    # 构建、类型检查和离线测试，不调用模型
```

日常启动默认使用 `.tina/control-plane.db` 保存项目、`.tina/tinad.json` 保存本机私有授权、`.tina/tinad.db` 保存执行命令和待回传事实；Ctrl+C 停止，下次启动保留数据。清理 `.build` 不会删除默认项目库。

```sh
# 构建后自定义启动；每个独立环境使用独立数据库和节点配置
node apps/cli/src/main.ts up --addr 127.0.0.1:7801 --db .tina/work.db --work-dir C:/code/my-project
# 只启动控制面；项目管理与已有审批仍可用，模型请求等待执行节点
node apps/cli/src/main.ts up --no-node
```

CLI 创建的本机配置默认 `autoProjects: true`，自动把未绑定 Agent 配置到当前节点，并将新项目的具体授权同步到本机清单。它不会迁移已绑定其他节点的 Agent。手工维护授权时设为 false；独立 tinad 不自行扩大授权。roots 约束请求工作目录，工具仍以当前操作系统用户权限运行。

## 使用真实 dsh

将 dsh 可用的 SDK profile、模型认证与原生持久目录准备在本机。示例为 MSYS2 Bash，`TINA_DSH_BIN` 指向实际安装的 `lib/bin.js`：

```sh
TINA_ENGINE=dsh \
TINA_DSH_BIN='C:/path/to/node_modules/@deepseek-ai/dsh/lib/bin.js' \
TINA_DSH_HOME='C:/path/to/dsh-sdk-home' \
make up
```

默认 profile 为 sdk-minimal，provider 为 deepseek-official，model 为 deepseek-flash，分别可由 `TINA_DSH_PROFILE`、`TINA_DSH_PROVIDER`、`TINA_DSH_MODEL` 覆盖。这些启动选项会保存到本机 tinad 配置；不要把 token 或模型密钥提交到 Git。也可直接编辑 `dsh.command` 参数数组，接入其他本机启动器。

SDK profile 必须能读取自己的认证配置。原生 SDK 的凭据插件和会话压缩配置随版本变化；配置建议与已验收版本见 [docs/tinad.md](docs/tinad.md)。当前 dsh 取消明确采用进程级降级，恢复能力由原生方法检查收窄。Worker 仍处于结构化报告联调阶段，本次迁移不等于已经交付自动修改仓库和制品上传。

## 导入已有 H0 文件库

先停止旧执行服务及其工具进程，再显式导入一个新的控制面库。下面的确认参数由执行操作的人负责；工具不会停止任意旧进程，也不自动导入个人历史数据。

```sh
.build/control-plane -db .tina/imported.db -import-h0 /path/to/h0.db -h0-stopped
node apps/cli/src/main.ts up --db .tina/imported.db
```

Windows 二进制带 `.exe`。导入前生成包含已提交 WAL 的快照，输出备份路径；来源保持只读。重复导入同一快照无副作用，项目冲突回滚。ID、依赖、报告、审批、人工覆盖和原事件顺序保留；旧活跃 Run 记录为迁移中断并提示核查外部效果，绝不自动重跑。历史 Agent ID 保留，新执行前在任务详情选择本项目的 Worker。

已有 H0 只有内存库时没有文件可导入。回退使用旧版本和 H0 快照；新控制面库不能交给旧节点写入。更多 API、远程节点配置及故障处理见 [控制面服务](docs/control-plane.md)、[tinad](docs/tinad.md) 和 [迁移验收报告](docs/architecture-migration-report.md)。
