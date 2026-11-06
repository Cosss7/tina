/**
 * 假引擎的可执行入口：从环境变量拿端口、令牌、场景文件，拨回节点，然后等死。
 *
 * 环境变量：
 *   TINA_ENGINE_PORT      节点监听的端口
 *   TINA_ENGINE_TOKEN     一次性令牌
 *   TINA_ENGINE_SCENARIO  场景文件路径
 */

import { serveEngine } from '@tina/engine-port';
import { createFakeBackend } from './fake.ts';
import { loadScenarioFile } from './scenario.ts';

const port = Number(process.env.TINA_ENGINE_PORT);
const token = process.env.TINA_ENGINE_TOKEN;
const scenarioPath = process.env.TINA_ENGINE_SCENARIO;

if (!Number.isInteger(port) || port <= 0) throw new Error('TINA_ENGINE_PORT is required');
if (token === undefined || token === '') throw new Error('TINA_ENGINE_TOKEN is required');
if (scenarioPath === undefined || scenarioPath === '') throw new Error('TINA_ENGINE_SCENARIO is required');

const scenario = loadScenarioFile(scenarioPath);
const intervalMs = process.env.TINA_ENGINE_INTERVAL_MS === undefined ? 0 : Number(process.env.TINA_ENGINE_INTERVAL_MS);

const served = await serveEngine({
  port,
  token,
  engine: scenario.engine,
  ...(scenario.engineVersion === undefined ? {} : { engineVersion: scenario.engineVersion }),
  createBackend: createFakeBackend(scenario, { intervalMs }),
});

await served.closed;
