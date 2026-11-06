/**
 * 节点运行时的入口：建库、注册到控制面、把转发的请求落到 Service 上。
 *
 * 会话执行走 fake engine 子进程（M0.4 用假的跑闭环，真 dsh 留给手工验收）。
 */

import { loadConfig } from './config.ts';
import { NodeConnection } from './connection.ts';
import { Store } from './db.ts';
import { runTurn } from './engine-run.ts';
import { createRpcHandler } from './rpc.ts';
import { Service } from './service.ts';

const config = loadConfig();
const store = new Store(config.dbPath);
const service = new Service({
  store,
  nodeId: config.nodeId,
  runTurn: (prompt) =>
    runTurn({
      engineBin: config.engineBin,
      scenario: config.scenario,
      cwd: config.workDir,
      prompt,
      timeoutMs: 180_000,
    }),
});

const connection = new NodeConnection(config, {
  onOpen: () => {
    console.log(`已连上控制面 ${config.controlPlaneUrl}，托管 ${config.projects.length} 个项目`);
  },
  onClose: (reason) => {
    console.log(`与控制面断开（${reason}），重试中`);
  },
  onError: (error) => {
    console.error(`连接出错：${error.message}`);
  },
  onRequest: createRpcHandler(service),
});

connection.start();

let stopping = false;
function shutdown(): void {
  if (stopping) return;
  stopping = true;
  connection.stop();
  store.close();
  console.log('已停止');
  process.exit(0);
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
