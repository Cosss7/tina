/**
 * M0.3 的验收：一条命令起全栈之后，杀掉节点，控制面要在 3 秒内显示离线。
 *
 * 这个测试用真的控制面二进制和真的节点进程，不 mock 任何东西。它依赖
 * `make build` 的产物，所以 `make test` 会先构建。
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const BIN = path.join(ROOT, '.build', process.platform === 'win32' ? 'control-plane.exe' : 'control-plane');
const NODE_MAIN = path.join(ROOT, 'packages', 'node-runtime', 'src', 'main.ts');

interface NodeView {
  id: string;
  name: string;
  online: boolean;
  projects: string[];
}
interface ProjectView {
  id: string;
  name: string;
  homeNodeId: string;
  online: boolean;
}
interface State {
  nodes: NodeView[];
  projects: ProjectView[];
}

test('节点掉线后控制面三秒内标记离线', async (t) => {
  if (!existsSync(BIN)) throw new Error(`${BIN} 不存在，先跑 make build`);

  const port = await freePort();
  const control = spawn(BIN, ['-addr', `127.0.0.1:${port}`], { stdio: ['ignore', 'pipe', 'pipe'] });
  const controlLog: string[] = [];
  control.stdout.on('data', (chunk: Buffer) => controlLog.push(chunk.toString('utf8')));
  control.stderr.on('data', (chunk: Buffer) => controlLog.push(chunk.toString('utf8')));
  t.after(() => {
    control.kill();
  });

  await waitFor(async () => (await health(port)) !== undefined, 10_000, '控制面起来');

  const node = spawn(process.execPath, [NODE_MAIN], {
    env: {
      ...process.env,
      TINA_CONTROL_PLANE_URL: `ws://127.0.0.1:${port}/ws/node`,
      TINA_NODE_ID: 'e2e-node',
      TINA_NODE_NAME: '端到端节点',
      TINA_HEARTBEAT_MS: '200',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const nodeLog: string[] = [];
  node.stdout.on('data', (chunk: Buffer) => nodeLog.push(chunk.toString('utf8')));
  node.stderr.on('data', (chunk: Buffer) => nodeLog.push(chunk.toString('utf8')));
  t.after(() => {
    node.kill();
  });

  const online = await waitFor(
    async () => {
      const state = await readState(port);
      return state?.nodes.some((entry) => entry.id === 'e2e-node' && entry.online) ? state : undefined;
    },
    10_000,
    `节点上线\n控制面: ${controlLog.join('')}\n节点: ${nodeLog.join('')}`,
  );

  assert.deepEqual(
    online.projects.map((project) => project.name).sort(),
    ['网站改版', '移动端'].sort(),
    'Web 上应当看到两个项目的看板',
  );
  assert.ok(
    online.projects.every((project) => project.homeNodeId === 'e2e-node' && project.online),
    '两个项目都该挂在注册上来的节点名下',
  );

  const killedAt = Date.now();
  node.kill();
  const offline = await waitFor(
    async () => {
      const state = await readState(port);
      if (state === undefined) return undefined;
      return state.nodes.every((entry) => !entry.online) ? state : undefined;
    },
    3_000,
    '节点离线',
  );

  const elapsed = Date.now() - killedAt;
  assert.ok(elapsed <= 3_000, `掉线检测用了 ${elapsed}ms，超过 3 秒`);
  assert.ok(
    offline.projects.every((project) => !project.online),
    '项目也应跟着显示离线',
  );
});

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return port;
}

async function health(port: number): Promise<unknown | undefined> {
  try {
    const response = await fetch(`http://127.0.0.1:${port}/health`);
    return response.ok ? await response.json() : undefined;
  } catch {
    return undefined;
  }
}

async function readState(port: number): Promise<State | undefined> {
  try {
    const response = await fetch(`http://127.0.0.1:${port}/api/state`);
    if (!response.ok) return undefined;
    return (await response.json()) as State;
  } catch {
    return undefined;
  }
}

async function waitFor<T>(probe: () => Promise<T | undefined>, timeoutMs: number, label: string): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = await probe();
    if (value !== undefined) return value;
    if (Date.now() > deadline) throw new Error(`等不到：${label}`);
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}
