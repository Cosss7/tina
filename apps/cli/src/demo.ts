import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdirSync, mkdtempSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { saveConfig, type LocalConfig } from './local-node.ts';

const root = fileURLToPath(new URL('../../..', import.meta.url));
mkdirSync(path.join(root, '.build'), { recursive: true });
const directory = mkdtempSync(path.join(root, '.build', 'demo-'));
const configPath = path.join(directory, 'tinad.json');
const db = process.env.TINA_CONTROL_DB || path.join(directory, 'control-plane.db');
const port = process.env.TINA_PORT || '7801';
const base = `http://127.0.0.1:${port}`;
const projectId = process.env.TINA_PROJECT || 'proj-website';
let log = ''; let failure: Error | undefined;
const child = spawn(process.execPath, [path.join(root, 'apps/cli/src/main.ts'), 'up', '--addr', `127.0.0.1:${port}`, '--db', db, '--node-config', configPath, '--engine', 'fake'], { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] });
child.on('error', (error) => { failure = error; });
child.stdout.on('data', (data: Buffer) => { log += data.toString(); });
child.stderr.on('data', (data: Buffer) => { log += data.toString(); });
async function request(endpoint: string, body?: unknown): Promise<any> {
  const response = await fetch(base + endpoint, body === undefined ? {} : { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
  if (!response.ok) throw new Error(`${endpoint}: ${response.status} ${await response.text()}`);
  return await response.json();
}
async function wait<T>(probe: () => Promise<T | undefined>): Promise<T> {
  const deadline = Date.now() + 20_000;
  for (;;) {
    if (failure) throw failure;
    if (child.exitCode !== null) throw new Error(log);
    const value = await probe(); if (value !== undefined) return value;
    if (Date.now() > deadline) throw new Error('Demo timeout\n' + log);
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}
async function board(): Promise<any> { return await request('/api/board?project=' + encodeURIComponent(projectId)); }
try {
  await wait(async () => { try { return (await request('/api/state')).nodes.some((node: any) => node.ready) ? true : undefined; } catch { return undefined; } });
  const demand = await request('/api/cards', { projectId, title: '网站改版', description: '验证控制面、Router、Worker 和人终审闭环' });
  await request('/api/plans', { projectId, taskId: demand.id });
  const approval = await wait(async () => (await board()).approvals.find((item: any) => item.taskId === demand.id));
  const decision = await request(`/api/plans/${approval.id}/decide`, { projectId, decision: 'approved' });
  for (const task of decision.tasks) {
    await request(`/api/tasks/${task.id}/dispatch`, { projectId });
    const card = await wait(async () => (await board()).cards.find((item: any) => item.id === task.id && item.status === 'review'));
    if (card.latestReport?.state === 'done') await request(`/api/tasks/${task.id}/review`, { projectId, decision: 'approved' });
  }
  console.log(JSON.stringify(await board(), null, 2));
  console.log('完整闭环完成；项目数据库：' + db);
} catch (error) { console.error(String(error) + '\n' + log); process.exitCode = 1; }
finally {
  if (child.exitCode === null && child.signalCode === null) {
    try { const config = JSON.parse(readFileSync(configPath, 'utf8')) as LocalConfig; config.stopping = true; saveConfig(configPath, config); } catch { child.kill(); }
    const timeout = setTimeout(() => { child.kill(); }, 10_000);
    await once(child, 'exit'); clearTimeout(timeout);
  }
}
