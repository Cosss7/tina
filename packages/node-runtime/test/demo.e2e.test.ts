/**
 * `demo.sh` 的验收：跑完之后，库里每条状态都要对得上 `docs/h0.md` 第 7 节。
 *
 * 这个测试不 mock 任何东西——真的控制面二进制、真的节点进程、真的 fake engine
 * 子进程，最后打开节点库逐条查。
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { DatabaseSync } from 'node:sqlite';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url));

test('demo.sh 跑通，库里的状态与第 7 节逐条对上', async (t) => {
  const port = await freePort();
  const dbPath = path.join(ROOT, '.build', `demo-${process.pid}.db`);
  mkdirSync(path.dirname(dbPath), { recursive: true });
  rmSync(dbPath, { force: true });
  t.after(() => rmSync(dbPath, { force: true }));

  const run = spawnSync('bash', ['demo.sh'], {
    cwd: ROOT,
    env: { ...process.env, TINA_PORT: String(port), TINA_DB: dbPath },
    encoding: 'utf8',
    timeout: 120_000,
  });
  assert.equal(run.status, 0, `demo.sh 退出码 ${String(run.status)}\n${run.stdout}\n${run.stderr}`);

  const db = new DatabaseSync(dbPath);
  try {
    const tasks = db.prepare('select title, status, kind, plan_approved, manual_override from task order by created_at').all();
    const byTitle = new Map(tasks.map((row) => [String(row.title), row]));

    assert.equal(tasks.length, 3, '一张需求卡加两张任务卡');
    assert.equal(byTitle.get('网站改版')?.kind, 'demand');
    assert.equal(byTitle.get('首页改版')?.kind, 'task');
    assert.equal(byTitle.get('接口联调')?.kind, 'task');

    // 待办：没有活跃 Run，且计划已批
    assert.equal(byTitle.get('网站改版')?.status, 'done', '需求卡拆完即完成');
    // 已完成：人终审通过
    assert.equal(byTitle.get('首页改版')?.status, 'done', '终审通过的卡进已完成');
    // 评审中：有 completed 的 Run 且报告已落库，等人终审
    assert.equal(byTitle.get('接口联调')?.status, 'review', '有报告的卡等人终审');

    const runs = db.prepare('select state, error from run order by rowid').all();
    assert.equal(runs.length, 2);
    assert.ok(runs.every((row) => row.state === 'completed'), '两个 Run 都是 completed');
    assert.ok(runs.every((row) => row.error === null));

    const reports = db.prepare('select state, summary, generated from report order by rowid').all();
    assert.equal(reports.length, 2);
    assert.equal(reports[0]?.state, 'done');
    assert.equal(reports[1]?.state, 'blocked');
    assert.ok(reports.every((row) => row.generated === 0), '都是 worker 自己交的，不是兜底的');

    const approvals = db.prepare('select kind, state, decided_by from approval order by requested_at').all();
    assert.deepEqual(
      approvals.map((row) => `${String(row.kind)}:${String(row.state)}`),
      ['plan:approved', 'final:approved'],
    );

    // 事件只追加，seq 从 1 连续
    const events = db.prepare('select seq, type from event order by seq').all();
    assert.deepEqual(events.map((row) => Number(row.seq)), events.map((_, index) => index + 1));
    const types = events.map((row) => String(row.type));
    assert.ok(types.includes('card.created'));
    assert.ok(types.includes('plan.requested'));
    assert.ok(types.includes('plan.approved'));
    assert.ok(types.includes('run.completed'));
    assert.ok(types.includes('review.approved'));
  } finally {
    db.close();
  }
});

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return port;
}
