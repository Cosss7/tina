/** Imports the real H0 schema, including a committed WAL not yet checkpointed. */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Store } from '../src/db.ts';
const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const BIN = path.join(ROOT, '.build', process.platform === 'win32' ? 'control-plane.exe' : 'control-plane');

test('H0 文件库导入保留 ID、人工覆盖、关联与事件，拒绝冲突并可重复执行', () => {
  mkdirSync(path.join(ROOT, '.build'), { recursive: true });
  const directory = mkdtempSync(path.join(ROOT, '.build', 'migration-'));
  const source = path.join(directory, 'legacy.db'), destination = path.join(directory, 'control.db');
  const store = new Store(source);
  try {
    const task = store.createTask({ projectId: 'old-project', title: '保留人工状态', assigneeId: 'worker-1', planApproved: true });
    store.updateTask(task, { manualOverride: true, status: 'done' });
    const dependent = store.createTask({ projectId: 'old-project', title: '中断的任务', deps: [task.id], planApproved: true });
    const completed = store.createRun({ taskId: task.id, agentId: 'worker-1', nodeId: 'old-node', originatorId: 'human:old' });
    store.setRunState(completed.id, 'completed');
    const report = store.createReport({ runId: completed.id, state: 'done', summary: '真实历史报告', decisions: ['保留'], artifacts: [{ path: 'hello.txt' }] });
    const interrupted = store.createRun({ taskId: dependent.id, agentId: 'worker-1', nodeId: 'old-node', originatorId: 'human:old' });
    store.setRunState(interrupted.id, 'running');
    const approval = store.createApproval({ projectId: 'old-project', taskId: task.id, kind: 'final', payload: { note: '人工决定' } });
    store.decideApproval(approval.id, 'approved', 'human:old');
    const event = store.appendEvent({ projectId: 'old-project', type: 'old.fact', actor: 'human:old', payload: { taskId: task.id } });
    const importDB = (target: string, stopped = true) => spawnSync(BIN, ['-db', target, '-import-h0', source, '-backup-dir', path.join(directory, 'snapshots'), ...(stopped ? ['-h0-stopped'] : [])], { encoding: 'utf8' });
    assert.notEqual(importDB(destination, false).status, 0);
    const first = importDB(destination); assert.equal(first.status, 0, first.stderr);
    const summary = JSON.parse(first.stdout) as { backup: string; fingerprint: string; interruptedRuns: number; generatedReports: number; alreadyImported: boolean };
    assert.equal(summary.interruptedRuns, 1); assert.equal(summary.generatedReports, 1); assert.equal(summary.alreadyImported, false);
    const backup = new DatabaseSync(summary.backup, { readOnly: true });
    assert.equal(backup.prepare('select state from run where id=?').get(interrupted.id)?.state, 'running'); backup.close();
    const db = new DatabaseSync(destination);
    const read = (table: string, id: string): any => JSON.parse(String(db.prepare(`select body from ${table} where id=?`).get(id)?.body));
    try {
      const migrated = read('task', task.id); assert.equal(migrated.manualOverride, true); assert.equal(migrated.status, 'done'); assert.equal(migrated.assigneeId, 'worker-1');
      assert.deepEqual(read('task', dependent.id).deps, [task.id]);
      assert.deepEqual(read('report', report.id), report);
      assert.equal(read('run', completed.id).agentId, completed.agentId);
      assert.equal(read('run', interrupted.id).state, 'failed'); assert.match(read('run', interrupted.id).error, /external effects remain unknown/);
      assert.equal(read('approval', approval.id).decidedBy, 'human:old');
      assert.deepEqual(read('event', event.id), event);
      assert.deepEqual(db.prepare('select seq from event order by seq').all().map((row) => row.seq), [1, 2]);
      assert.equal(db.prepare('select count(*) as n from execution_request').get()?.n, 0, '历史活跃任务绝不自动重跑');
    } finally { db.close(); }
    const duplicate = importDB(destination); assert.equal(duplicate.status, 0, duplicate.stderr);
    const repeated = JSON.parse(duplicate.stdout); assert.equal(repeated.alreadyImported, true); assert.equal(repeated.backup, summary.backup);
    store.appendEvent({ projectId: 'old-project', type: 'changed', actor: 'human' });
    assert.notEqual(importDB(destination).status, 0, '变更过的来源与既有项目冲突，不能覆盖');
    const after = new DatabaseSync(destination); assert.equal(after.prepare('select count(*) as n from event').get()?.n, 2); after.close();
    assert.equal(store.getRun(interrupted.id)?.state, 'running', '来源未改写');
    assert.equal(store.getTask(task.id)?.manualOverride, true);
  } finally {
    store.close(); assert.ok(directory.startsWith(path.join(ROOT, '.build') + path.sep)); rmSync(directory, { recursive: true, force: true });
  }
});
