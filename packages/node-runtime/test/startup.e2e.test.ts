import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const ROOT = fileURLToPath(new URL('../../..', import.meta.url));

test('新项目授权热更新保留活动实例，统一启动优雅停止并保留原项目', async () => {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  assert.ok(address && typeof address === 'object');
  const port = address.port;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  mkdirSync(path.join(ROOT, '.build'), { recursive: true });
  const directory = mkdtempSync(path.join(ROOT, '.build', 'startup-'));
  const configPath = path.join(directory, 'tinad.json');
  const db = path.join(directory, 'control.db');
  const base = `http://127.0.0.1:${port}`;
  let log = '';
  let failure: Error | null = null;
  const start = () => {
    const child = spawn(
      process.execPath,
      [
        'apps/cli/src/main.ts',
        'up',
        '--addr',
        `127.0.0.1:${port}`,
        '--db',
        db,
        '--node-config',
        configPath,
        '--engine',
        'fake',
      ],
      { cwd: ROOT, stdio: ['ignore', 'pipe', 'pipe'] },
    );
    child.on('error', (error) => {
      failure = error;
    });
    child.stdout.on('data', (data: Buffer) => {
      log += data.toString();
    });
    child.stderr.on('data', (data: Buffer) => {
      log += data.toString();
    });
    return child;
  };
  let child = start();
  const stop = async () => {
    if (child.exitCode !== null || child.signalCode !== null) return;
    const done = once(child, 'exit');
    try {
      const config = JSON.parse(readFileSync(configPath, 'utf8'));
      config.stopping = true;
      writeFileSync(configPath, JSON.stringify(config));
    } catch {
      child.kill();
    }
    const timer = setTimeout(() => child.kill(), 10_000);
    await done;
    clearTimeout(timer);
  };
  const request = async (url: string, body?: unknown, method = 'POST'): Promise<any> => {
    const response = await fetch(
      base + url,
      body === undefined ? {} : { method, headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) },
    );
    assert.ok(response.ok, await response.clone().text());
    return await response.json();
  };
  const wait = async <T>(probe: () => Promise<T | undefined>): Promise<T> => {
    const end = Date.now() + 12_000;
    for (;;) {
      if (failure) throw failure;
      if (child.exitCode !== null) throw new Error(log);
      const value = await probe();
      if (value !== undefined) return value;
      if (Date.now() > end) throw new Error(log);
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  };
  try {
    await wait(async () => {
      try {
        return (await request('/api/state')).nodes.some((node: any) => node.ready) ? true : undefined;
      } catch {
        return undefined;
      }
    });
    const projectId = 'proj-website';
    const agents = await request('/api/projects/' + projectId + '/agents');
    const router = agents.find((agent: any) => agent.role === 'router');
    await request(
      '/api/projects/' + projectId + '/agents/' + router.id,
      { ...router, options: { delayMs: -1 } },
      'PUT',
    );
    const demand = await request('/api/cards', { projectId, title: '授权重载期间保持运行' });
    const queued = await request('/api/plans', { projectId, taskId: demand.id });
    const before = await wait(async () => {
      const detail = await request(`/api/runs/${queued.run.id}?project=${projectId}`);
      return detail.run.state === 'running' ? detail.run : undefined;
    });
    const created = await request('/api/projects', { name: '启动后新建项目' });
    await wait(async () =>
      (await request('/api/state')).nodes.some(
        (node: any) => node.ready && node.agents.some((agent: any) => agent.projectId === created.id),
      )
        ? true
        : undefined,
    );
    const after = await request(`/api/runs/${queued.run.id}?project=${projectId}`);
    assert.equal(after.run.state, 'running');
    assert.equal(after.run.instanceId, before.instanceId);
    await request(`/api/runs/${queued.run.id}/cancel`, { projectId, reason: '热更新完成' });
    await wait(async () =>
      (await request(`/api/runs/${queued.run.id}?project=${projectId}`)).run.state === 'cancelled' ? true : undefined,
    );
    await stop();
    assert.equal(child.exitCode, 0, log);
    child = start();
    await wait(async () => {
      try {
        return (await request('/api/state')).nodes.some((node: any) => node.ready) ? true : undefined;
      } catch {
        return undefined;
      }
    });
    const restored = await request(`/api/runs/${queued.run.id}?project=${projectId}`);
    assert.equal(restored.run.state, 'cancelled');
    assert.equal(restored.run.instanceId, before.instanceId);
    assert.ok((await request('/api/state')).projects.some((project: any) => project.id === created.id));
  } finally {
    await stop();
    assert.ok(directory.startsWith(path.join(ROOT, '.build') + path.sep));
    rmSync(directory, { recursive: true, force: true });
  }
});
