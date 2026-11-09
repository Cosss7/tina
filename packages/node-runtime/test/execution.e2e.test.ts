/** Two independent Go tinad processes with separate journals and an HTTP client. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { once } from 'node:events';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const EXE = process.platform === 'win32' ? '.exe' : '';
interface Run { id: string; state: string; startedAt: string | null; needsReconciliation: boolean }
interface Card { id: string; title: string; kind: string; status: string; runs: Run[]; latestReport: { state: string; summary: string } | null }
interface Board { cards: Card[]; approvals: Array<{ id: string }> }
interface Agent { id: string; name: string; role: string; projectId: string; engineId: string }

test('两个 Go tinad 完成计划、依赖派发、报告和终审，控制面重启不重跑', async (t) => {
  mkdirSync(path.join(ROOT, '.build'), { recursive: true });
  const directory = mkdtempSync(path.join(ROOT, '.build', 'execution-e2e-'));
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const children: Array<{ process: ChildProcess; log: string; error: Error | null }> = [];
  const stop = async (child: ChildProcess): Promise<void> => {
    if (child.exitCode !== null || child.signalCode !== null || child.pid === undefined) return;
    const exited = once(child, 'exit'); child.kill(); await exited;
  };
  t.after(async () => {
    for (const child of [...children].reverse()) await stop(child.process);
    assert.ok(path.resolve(directory).startsWith(path.resolve(ROOT, '.build') + path.sep));
    rmSync(directory, { recursive: true, force: true });
  });
  const boot = (name: string, args: string[]) => {
    const process = spawn(path.join(ROOT, '.build', name + EXE), args, { cwd: ROOT, stdio: ['ignore', 'ignore', 'pipe'] });
    const child = { process, log: '', error: null as Error | null }; children.push(child);
    process.on('error', (error) => { child.error = error; });
    process.stderr!.on('data', (chunk: Buffer) => { child.log += chunk.toString('utf8'); });
    return process;
  };
  const waitFor = async <T>(probe: () => Promise<T | undefined>, label: string): Promise<T> => {
    const deadline = Date.now() + 12_000;
    for (;;) {
      for (const child of children) if (child.error) throw child.error;
      try { const value = await probe(); if (value !== undefined) return value; } catch { /* waiting for startup */ }
      if (Date.now() > deadline) throw new Error(`${label}\n${children.map((c) => c.log).join('\n')}`);
      await new Promise((resolve) => setTimeout(resolve, 40));
    }
  };
  const get = async <T>(url: string): Promise<T> => {
    const response = await fetch(base + url); assert.ok(response.ok, await response.clone().text()); return await response.json() as T;
  };
  const send = async <T>(method: string, url: string, body: unknown): Promise<T> => {
    const response = await fetch(base + url, { method, headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    assert.ok(response.ok, `${url}: ${await response.clone().text()}`); return await response.json() as T;
  };
  const cpArgs = ['-addr', `127.0.0.1:${port}`, '-db', path.join(directory, 'control.db')];
  const control = boot('control-plane', cpArgs);
  await waitFor(async () => (await fetch(base + '/health')).ok ? true : undefined, 'control plane health');
  const projectId = 'proj-website';
  const agents = await get<Agent[]>(`/api/projects/${projectId}/agents`);
  for (const agent of agents) {
    const nodeId = 'execution-' + agent.role;
    const token = randomBytes(32).toString('hex');
    await send('POST', '/api/nodes/enroll', { nodeId, name: nodeId, token });
    await send('PUT', `/api/projects/${projectId}/agents/${agent.id}`, { name: agent.name, engineId: 'fake', nodeId, cwd: ROOT });
    const configPath = path.join(directory, nodeId + '.json');
    writeFileSync(configPath, JSON.stringify({ nodeId, name: nodeId, url: `ws://127.0.0.1:${port}/ws/tinad`, token, database: path.join(directory, nodeId + '.db'), maxConcurrent: 2, roots: [ROOT], agents: [{ id: agent.id, projectId, role: agent.role, engineId: 'fake' }] }), { mode: 0o600 });
    boot('tinad', ['-config', configPath]);
  }
  await waitFor(async () => {
    const state = await get<{ nodes: Array<{ ready: boolean }> }>('/api/state');
    return state.nodes.filter((n) => n.ready).length === 2 ? true : undefined;
  }, 'both tinad registrations');
  const demand = await send<Card>('POST', '/api/cards', { projectId, title: '网站改版' });
  await send('POST', '/api/plans', { projectId, taskId: demand.id });
  const board = () => get<Board>(`/api/board?project=${projectId}`);
  const planned = await waitFor(async () => { const b = await board(); return b.approvals.length === 1 ? b : undefined; }, 'router plan');
  assert.equal(planned.cards[0]?.runs[0]?.state, 'completed');
  assert.ok(planned.cards[0]?.runs[0]?.startedAt, 'receipt alone must not substitute for a start event');
  const approved = await send<{ tasks: Card[] }>('POST', `/api/plans/${planned.approvals[0]!.id}/decide`, { projectId, decision: 'approved' });
  const [first, second] = approved.tasks;
  assert.ok(first && second);
  const premature = await fetch(`${base}/api/tasks/${second.id}/dispatch`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ projectId }) });
  assert.equal(premature.status, 409, 'forward dispatch must respect approved dependencies');
  await send('POST', `/api/tasks/${first.id}/dispatch`, { projectId });
  await waitFor(async () => { const b = await board(); return b.cards.find((c) => c.id === first.id)?.status === 'review' ? b : undefined; }, 'first worker report');
  await send('POST', `/api/tasks/${first.id}/review`, { projectId, decision: 'approved' });
  await send('POST', `/api/tasks/${second.id}/dispatch`, { projectId });
  const finished = await waitFor(async () => { const b = await board(); return b.cards.find((c) => c.id === second.id)?.status === 'review' ? b : undefined; }, 'second worker report');
  assert.equal(finished.cards.find((c) => c.id === second.id)?.latestReport?.state, 'blocked');
  const before = finished.cards.flatMap((c) => c.runs.map((r) => r.id));
  await stop(control);
  boot('control-plane', cpArgs);
  const recovered = await waitFor(async () => { const b = await board(); return b.cards.length === 3 ? b : undefined; }, 'control-plane restart');
  assert.deepEqual(recovered.cards.flatMap((c) => c.runs.map((r) => r.id)), before);
  assert.equal(recovered.cards.find((c) => c.id === first.id)?.status, 'done');
  await waitFor(async () => {
    const state = await get<{ nodes: Array<{ ready: boolean }> }>('/api/state'); return state.nodes.filter((n) => n.ready).length === 2 ? true : undefined;
  }, 'tinad reconnect');
});

async function freePort(): Promise<number> {
  const server = createServer(); await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address(); const port = typeof address === 'object' && address !== null ? address.port : 0;
  await new Promise<void>((resolve) => server.close(() => resolve())); return port;
}
