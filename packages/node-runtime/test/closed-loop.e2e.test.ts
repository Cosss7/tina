/**
 * 闭环的端到端：真的拉起 fake engine 子进程，不是 stub。
 *
 * 这条测试盯的是"场景文件与提示词的约定还在不在"——提示词里的 `[TINA:PLAN]`
 * 标记、场景里的规则匹配、报告 JSON 的形状，任何一处漂了它都会红。
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadConfig } from '../src/config.ts';
import { Store } from '../src/db.ts';
import { runTurn } from '../src/engine-run.ts';
import { Service } from '../src/service.ts';

test('真 fake engine 跑完整闭环，计划与报告都能解析', async () => {
  const config = loadConfig({});
  const store = new Store();
  const service = new Service({
    store,
    nodeId: 'node-e2e',
    runTurn: (prompt) =>
      runTurn({ engineBin: config.engineBin, scenario: config.scenario, cwd: config.workDir, prompt, timeoutMs: 20_000 }),
  });

  const project = 'proj-website';
  const demand = service.createDemand({ projectId: project, title: '网站改版', description: '把首页和接口一起改', actor: 'human' });

  const approval = await service.requestPlan({ projectId: project, taskId: demand.id, actor: 'human' });
  const payload = approval.payload as { summary?: string; cards?: Array<{ title: string; acceptance: string[] }> };
  assert.match(payload.summary ?? '', /首页和接口/);
  assert.deepEqual(payload.cards?.map((card) => card.title), ['首页改版', '接口联调']);

  const { tasks } = service.decidePlan({ approvalId: approval.id, decision: 'approved', actor: 'human' });
  assert.equal(tasks.length, 2);

  const first = service.dispatch({ taskId: tasks[0]!.id, actor: 'human' });
  await service.waitForRun(first.run.id);
  assert.equal(store.getRun(first.run.id)?.state, 'completed');
  assert.equal(store.getReportByRun(first.run.id)?.state, 'done');

  const second = service.dispatch({ taskId: tasks[1]!.id, actor: 'human' });
  await service.waitForRun(second.run.id);
  assert.equal(store.getRun(second.run.id)?.state, 'completed');
  const secondReport = store.getReportByRun(second.run.id);
  assert.equal(secondReport?.state, 'blocked');
  assert.deepEqual(secondReport?.blockers, ['订单接口需要新的 API key，现有凭据权限不够']);

  const approved = service.finalReview({ taskId: tasks[0]!.id, decision: 'approved', actor: 'human' });
  assert.equal(approved.status, 'done');

  const board = service.board(project);
  assert.equal(board.cards.filter((card) => card.status === 'review').length, 1, '第二张卡还在评审中');
  assert.equal(board.cards.filter((card) => card.status === 'done').length, 2, '需求卡和第一张任务卡完成');
  store.close();
});
