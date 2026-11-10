/** The public startup entry drives the Go Router/Worker loop without a model. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { DatabaseSync } from 'node:sqlite';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
test('一条命令完成 Go Router、依赖派发、报告与人工终审', async () => {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  assert.ok(address && typeof address === 'object');
  const port = address.port;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  mkdirSync(path.join(ROOT, '.build'), { recursive: true });
  const directory = mkdtempSync(path.join(ROOT, '.build', 'demo-verify-'));
  const filename = path.join(directory, 'control.db');
  try {
    const result = spawnSync(process.execPath, ['apps/cli/src/demo.ts'], {
      cwd: ROOT,
      env: { ...process.env, TINA_PORT: String(port), TINA_CONTROL_DB: filename },
      encoding: 'utf8',
      timeout: 35_000,
    });
    assert.equal(result.status, 0, result.stdout + '\n' + result.stderr);
    const db = new DatabaseSync(filename);
    try {
      const cards = db
        .prepare('select body from task')
        .all()
        .map((row) => JSON.parse(String(row.body)));
      assert.deepEqual(cards.map((card) => card.status).sort(), ['done', 'done', 'review']);
      assert.equal(db.prepare("select count(*) as n from run where state='completed'").get()?.n, 3);
      const reports = db
        .prepare(
          "select report.body from report join run on run.id=report.run_id where json_extract(run.body,'$.purpose')='report'",
        )
        .all()
        .map((row) => JSON.parse(String(row.body)));
      assert.equal(reports.length, 2);
      assert.ok(reports.every((report) => !report.generated));
      assert.deepEqual(reports.map((report) => report.state).sort(), ['blocked', 'done']);
      const events = db.prepare('select seq from event where project_id=? order by seq').all('proj-website');
      assert.deepEqual(
        events.map((event) => Number(event.seq)),
        Array.from({ length: events.length }, (_, index) => index + 1),
      );
    } finally {
      db.close();
    }
  } finally {
    assert.ok(directory.startsWith(path.join(ROOT, '.build') + path.sep));
    rmSync(directory, { recursive: true, force: true });
  }
});
