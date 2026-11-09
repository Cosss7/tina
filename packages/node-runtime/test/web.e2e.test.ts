/**
 * 前端的验收：控制面里嵌进去的必须是构建产物，而不是那个手写的占位页。
 *
 * 只看两件事：`/` 返回的 HTML 挂了打包出来的 JS，而且那个 JS 真的拿得到。
 * 这条测试挡住的是"make build 忘了跑 web 构建"这种低级错误。
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

test('控制面服务的是构建出来的前端，静态资源可取', async (t) => {
  if (!existsSync(BIN)) throw new Error(`${BIN} 不存在，先跑 make build`);
  const port = await freePort();
  const control = spawn(BIN, ['-addr', `127.0.0.1:${port}`, '-db', ':memory:'], { stdio: ['ignore', 'ignore', 'pipe'] });
  control.on('error', (error) => { throw error; });
  t.after(() => control.kill());

  const index = await waitForHtml(port);
  assert.match(index, /<div id="root">/, 'React 挂载点还在');
  assert.match(index, /<script[^>]+src="\/assets\/[^"]+\.js"/, '页面应当引用打包出来的 JS');

  const asset = /src="(\/assets\/[^"]+\.js)"/.exec(index)?.[1];
  assert.ok(asset, '找不到 JS 资源路径');
  const response = await fetch(`http://127.0.0.1:${port}${asset}`);
  assert.equal(response.status, 200, 'JS 资源应当 200');
  const body = await response.text();
  assert.ok(body.length > 1000, 'JS 内容不像是个打包产物');
  assert.match(response.headers.get('content-type') ?? '', /javascript/);

  const state = await fetch(`http://127.0.0.1:${port}/api/state`);
  assert.equal(state.status, 200);
  const payload = (await state.json()) as { version: string };
  assert.equal(payload.version, '0.1.0');
});

async function waitForHtml(port: number): Promise<string> {
  const deadline = Date.now() + 10_000;
  for (;;) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/`);
      if (response.ok) return await response.text();
    } catch {
      // 还没起来
    }
    if (Date.now() > deadline) throw new Error('控制面没起来');
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return port;
}
