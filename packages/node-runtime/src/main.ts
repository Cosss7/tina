/**
 * 节点运行时的入口。M0.3 只做一件事：注册到控制面并保持心跳。
 * 托管项目、调度、引擎进程在 M0.4 接上来。
 */

import { loadConfig } from './config.ts';
import { NodeConnection } from './connection.ts';

const config = loadConfig();
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
});

connection.start();

let stopping = false;
function shutdown(): void {
  if (stopping) return;
  stopping = true;
  connection.stop();
  console.log('已停止');
  process.exit(0);
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
