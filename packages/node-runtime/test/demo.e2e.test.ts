/** P2 演示验收：真实控制面，无节点，需求及待执行请求落在控制面库中。 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { DatabaseSync } from 'node:sqlite';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url));

test('P2 demo 无节点建卡，Run 与执行请求持久排队', async (t) => {
  const port = await freePort();
  mkdirSync(path.join(ROOT, '.build'), { recursive: true });
  const directory = mkdtempSync(path.join(ROOT, '.build', 'tina-p2-demo-'));
  const dbPath = path.join(directory, 'control.db');
  t.after(() => {
    assert.ok(path.resolve(directory).startsWith(path.resolve(ROOT, '.build') + path.sep));
    rmSync(directory, { recursive: true, force: true });
  });
  const run = spawnSync('bash', ['demo.sh'], {
    cwd: ROOT,
    env: { ...process.env, TINA_PORT: String(port), TINA_CONTROL_DB: dbPath },
    encoding: 'utf8', timeout: 30_000,
  });
  assert.equal(run.status, 0, `${run.stdout}\n${run.stderr}`);
  const db = new DatabaseSync(dbPath);
  try {
    const task = JSON.parse(String(db.prepare('select body from task').get()?.body)) as { kind: string; title: string };
    assert.equal(task.kind, 'demand');
    assert.equal(task.title, '网站改版');
    const request = db.prepare('select state, body from execution_request').get();
    assert.equal(request?.state, 'queued');
    const stored = JSON.parse(String(request?.body)) as { runId: string; commandId: string; prompt: string };
    const execution = JSON.parse(String(db.prepare('select body from run where id=?').get(stored.runId)?.body)) as { commandId: string; state: string; startedAt: string | null };
    assert.equal(execution.commandId, stored.commandId);
    assert.equal(execution.state, 'queued');
    assert.equal(execution.startedAt, null);
    assert.match(stored.prompt, /\[TINA:PLAN\]/);
    const events = db.prepare('select seq,body from event where project_id=? order by seq').all('proj-website');
    assert.deepEqual(events.map((e) => Number(e.seq)), [1, 2, 3]);
    assert.equal(db.prepare('select count(*) as n from report').get()?.n, 0);
  } finally { db.close(); }
});

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return port;
}
