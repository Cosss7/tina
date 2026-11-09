/**
 * 真 dsh 的引擎入口：和 fake engine 一样由节点拉起来，走同一套端口协议。
 *
 * 环境变量：
 *   TINA_ENGINE_PORT / TINA_ENGINE_TOKEN   节点给的
 *   TINA_DSH_BIN       dsh 的 lib/bin.js 绝对路径（必填，不写进仓库）
 *   变量名统一带 TINA_ 前缀：宿主环境里本来就有 DSH_PROFILE（比如 dsh web），
 *   不加前缀会被继承过来，子进程就 boot 错 profile 了。
 *   TINA_DSH_PROFILE   默认 sdk-minimal
 *   TINA_DSH_PROVIDER  默认 deepseek-official
 *   TINA_DSH_MODEL     默认 deepseek-flash
 *   TINA_DSH_REASONING 默认 off（H0 要的是稳定的 JSON，不是思考）
 *   TINA_DSH_MAX_TOKENS 默认 8192（定太小会全用在思考上，正文一个字都不剩）
 *   TINA_WORK_DIR 会话工作目录，默认当前目录
 */

import { serveEngine } from '@tina/engine-port';
import { DshBackend } from './backend.ts';

const port = Number(process.env.TINA_ENGINE_PORT);
const token = process.env.TINA_ENGINE_TOKEN;
const dshBin = process.env.TINA_DSH_BIN;

if (!Number.isInteger(port) || port <= 0) throw new Error('TINA_ENGINE_PORT must be a positive integer');
if (token === undefined || token === '') throw new Error('TINA_ENGINE_TOKEN is required');
if (dshBin === undefined || dshBin === '') throw new Error('TINA_DSH_BIN is required (path to dsh lib/bin.js)');

const served = await serveEngine({
  port,
  token,
  engine: 'dsh',
  engineVersion: process.env.TINA_DSH_PROFILE ?? 'sdk-minimal',
  createBackend: (emit) =>
    new DshBackend(
      {
        dshBin,
        profile: process.env.TINA_DSH_PROFILE ?? 'sdk-minimal',
        provider: process.env.TINA_DSH_PROVIDER ?? 'deepseek-official',
        model: process.env.TINA_DSH_MODEL ?? 'deepseek-flash',
        cwd: process.env.TINA_WORK_DIR ?? process.cwd(),
        maxTokens: process.env.TINA_DSH_MAX_TOKENS === undefined ? 8192 : Number(process.env.TINA_DSH_MAX_TOKENS),
        reasoningEffort: process.env.TINA_DSH_REASONING ?? 'off',
      },
      emit,
    ),
});

await served.closed;
