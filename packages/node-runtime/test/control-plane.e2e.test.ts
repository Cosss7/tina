/** P2 acceptance across real process restarts, with no Agent process. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const BIN = path.join(ROOT, '.build', process.platform === 'win32' ? 'control-plane.exe' : 'control-plane');

test('控制面进程重启后项目、卡片与队列仍在，无节点也可管理', async (t) => {
  mkdirSync(path.join(ROOT, '.build'), { recursive: true });
  const directory = mkdtempSync(path.join(ROOT, '.build', 'tina-control-p2-'));
  const filename = path.join(directory, 'project data.db');
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const start = async () => {
    const process = spawn(BIN, ['-addr', `127.0.0.1:${port}`, '-db', filename, '-seed-demo=false'], {
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    let failure: Error | null = null;
    let log = '';
    process.on('error', (error) => {
      failure = error;
    });
    process.stderr.on('data', (chunk: Buffer) => {
      log += chunk.toString('utf8');
    });
    const stop = async () => {
      if (process.exitCode !== null || process.signalCode !== null || failure !== null) return;
      const exited = once(process, 'exit');
      process.kill();
      await exited;
    };
    t.after(stop);
    const deadline = Date.now() + 10_000;
    for (;;) {
      if (failure) throw failure;
      if (process.exitCode !== null) throw new Error(`Control plane exited: ${log}`);
      try {
        if ((await fetch(`${base}/health`)).ok) break;
      } catch {
        /* starting */
      }
      if (Date.now() > deadline) throw new Error(`Control plane did not start: ${log}`);
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    return { stop };
  };
  const post = async (endpoint: string, value: unknown) => {
    const response = await fetch(`${base}${endpoint}`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(value),
    });
    assert.ok(response.ok, `${endpoint}: ${await response.clone().text()}`);
    return (await response.json()) as Record<string, unknown>;
  };
  const first = await start();
  t.after(async () => {
    await first.stop();
  });
  const project = await post('/api/projects', { name: '持久项目' });
  const card = await post('/api/cards', { projectId: project.id, title: '重启保留的需求' });
  const requested = await post('/api/plans', { projectId: project.id, taskId: card.id });
  const before = await (await fetch(`${base}/api/queue?project=${String(project.id)}`)).json();
  await first.stop();
  const second = await start();
  const state = (await (await fetch(`${base}/api/state`)).json()) as {
    projects: Array<{ id: string }>;
    nodes: unknown[];
  };
  assert.equal(state.projects[0]?.id, project.id);
  assert.deepEqual(state.nodes, []);
  const board = (await (await fetch(`${base}/api/board?project=${String(project.id)}`)).json()) as {
    cards: Array<{ id: string; runs: Array<{ state: string; startedAt: string | null }> }>;
  };
  assert.equal(board.cards[0]?.id, card.id);
  assert.equal(board.cards[0]?.runs[0]?.state, 'queued');
  assert.equal(board.cards[0]?.runs[0]?.startedAt, null);
  assert.deepEqual(await (await fetch(`${base}/api/queue?project=${String(project.id)}`)).json(), before);
  const duplicate = await fetch(`${base}/api/plans`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ projectId: project.id, taskId: card.id }),
  });
  assert.equal(duplicate.status, 409);
  assert.ok(requested.run);
  await second.stop();
  assert.ok(path.resolve(directory).startsWith(path.resolve(ROOT, '.build') + path.sep));
  rmSync(directory, { recursive: true, force: true });
});

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return port;
}
