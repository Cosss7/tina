import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Store } from '../src/db.ts';
import { Service, ServiceError, type TurnRunner } from '../src/service.ts';
import { parsePlan, parseReport } from '../src/domain.ts';

const PLAN = JSON.stringify({
  summary: '两件事',
  cards: [
    { key: 'c1', title: '首页改版', description: '改首屏', acceptance: ['不换行'], deps: [] },
    { key: 'c2', title: '接口联调', description: '接三个接口', acceptance: ['有重试'], deps: ['c1'] },
  ],
});
const REPORT = JSON.stringify({
  state: 'done',
  summary: '做完了',
  decisions: ['先改结构'],
  artifacts: [{ path: 'web/index.html' }],
  blockers: [],
  usage: { inputTokens: 10 },
});

function setup(turn?: TurnRunner) {
  const store = new Store();
  let prompts: string[] = [];
  const runner: TurnRunner = turn ?? (async (prompt) => {
    prompts.push(prompt);
    return {
      events: [],
      text: prompt.includes('TINA:PLAN') ? PLAN : REPORT,
      usage: {},
      engine: 'stub',
    };
  });
  const service = new Service({ store, nodeId: 'node-test', runTurn: runner });
  return { store, service, prompts: () => prompts };
}

test('闭环：建卡 → 拆卡 → 批计划 → 派活 → 报告 → 评审 → 终审', async () => {
  const { store, service } = setup();
  const project = 'proj-1';

  const demand = service.createDemand({ projectId: project, title: '网站改版', actor: 'human' });
  assert.equal(demand.status, 'todo');
  assert.equal(demand.kind, 'demand');

  const approval = await service.requestPlan({ projectId: project, taskId: demand.id, actor: 'human' });
  assert.equal(approval.kind, 'plan');
  assert.equal(approval.state, 'pending');
  assert.equal(store.listTasks(project).length, 1, '批计划之前不创建任务卡');

  const decided = service.decidePlan({ approvalId: approval.id, decision: 'approved', actor: 'human' });
  assert.equal(decided.tasks.length, 2);
  assert.equal(store.getTask(demand.id)?.status, 'done', '需求卡拆完就算完成');
  assert.equal(store.listTasks(project).filter((task) => task.kind === 'task').length, 2);
  assert.ok(decided.tasks.every((task) => task.planApproved && task.status === 'todo'));

  const target = decided.tasks[0]!;
  const dispatched = await service.dispatch({ taskId: target.id, actor: 'human' });
  assert.equal(dispatched.run.state, 'completed');
  assert.equal(dispatched.task.status, 'review', '有报告等人终审');

  const reviewed = service.finalReview({ taskId: target.id, decision: 'approved', actor: 'human' });
  assert.equal(reviewed.status, 'done');
});

test('报告被解析后落库，字段一个不少', async () => {
  const { store, service } = setup();
  service.createDemand({ projectId: 'p', title: '需求', actor: 'human' });
  const approval = await service.requestPlan({ projectId: 'p', taskId: store.listTasks('p')[0]!.id, actor: 'human' });
  const { tasks } = service.decidePlan({ approvalId: approval.id, decision: 'approved', actor: 'human' });

  const { run } = await service.dispatch({ taskId: tasks[0]!.id, actor: 'human' });
  const report = store.getReportByRun(run.id);
  assert.ok(report);
  assert.equal(report.state, 'done');
  assert.equal(report.summary, '做完了');
  assert.deepEqual(report.decisions, ['先改结构']);
  assert.deepEqual(report.artifacts, [{ path: 'web/index.html' }]);
  assert.equal(report.generated, false);
});

test('blocked 的报告也进评审中，不自动失败', async () => {
  const blocked = JSON.stringify({ state: 'blocked', summary: '缺凭据', decisions: [], artifacts: [], blockers: ['要 key'], usage: {} });
  const { store, service } = setup(async (prompt) => ({
    events: [],
    text: prompt.includes('TINA:PLAN') ? PLAN : blocked,
    usage: {},
    engine: 'stub',
  }));
  service.createDemand({ projectId: 'p', title: '需求', actor: 'human' });
  const approval = await service.requestPlan({ projectId: 'p', taskId: store.listTasks('p')[0]!.id, actor: 'human' });
  const { tasks } = service.decidePlan({ approvalId: approval.id, decision: 'approved', actor: 'human' });

  const { run, task } = await service.dispatch({ taskId: tasks[0]!.id, actor: 'human' });
  assert.equal(run.state, 'completed');
  assert.equal(task.status, 'review');
  assert.equal(store.getReportByRun(run.id)?.state, 'blocked');
});

test('报告解析不了时 Run 失败，并补一份 generated 的兜底报告', async () => {
  const { store, service } = setup(async (prompt) => ({
    events: [],
    text: prompt.includes('TINA:PLAN') ? PLAN : '我不知道该说什么',
    usage: {},
    engine: 'stub',
  }));
  service.createDemand({ projectId: 'p', title: '需求', actor: 'human' });
  const approval = await service.requestPlan({ projectId: 'p', taskId: store.listTasks('p')[0]!.id, actor: 'human' });
  const { tasks } = service.decidePlan({ approvalId: approval.id, decision: 'approved', actor: 'human' });

  const { run } = await service.dispatch({ taskId: tasks[0]!.id, actor: 'human' });
  assert.equal(run.state, 'failed');
  assert.match(run.error ?? '', /找不到合法 JSON/);
  const report = store.getReportByRun(run.id);
  assert.equal(report?.generated, true);
  assert.equal(report?.state, 'failed');
});

test('计划没批不能派活；需求卡不能直接派活', async () => {
  const { store, service } = setup();
  const demand = service.createDemand({ projectId: 'p', title: '需求', actor: 'human' });
  await assert.rejects(service.dispatch({ taskId: demand.id, actor: 'human' }), (error: unknown) =>
    error instanceof ServiceError && /需求卡/.test(error.message));

  const approval = await service.requestPlan({ projectId: 'p', taskId: demand.id, actor: 'human' });
  const { tasks } = service.decidePlan({ approvalId: approval.id, decision: 'approved', actor: 'human' });
  const task = store.getTask(tasks[0]!.id)!;
  store.updateTask(task, { planApproved: false });
  await assert.rejects(service.dispatch({ taskId: task.id, actor: 'human' }), (error: unknown) =>
    error instanceof ServiceError && /计划还没批/.test(error.message));
});

test('拒绝计划不产生任务卡', async () => {
  const { store, service } = setup();
  const demand = service.createDemand({ projectId: 'p', title: '需求', actor: 'human' });
  const approval = await service.requestPlan({ projectId: 'p', taskId: demand.id, actor: 'human' });
  const result = service.decidePlan({ approvalId: approval.id, decision: 'rejected', actor: 'human' });
  assert.deepEqual(result.tasks, []);
  assert.equal(store.listTasks('p').length, 1);
  assert.equal(store.getTask(demand.id)?.status, 'todo');
});

test('每一步都留事件，且 seq 连续', async () => {
  const { store, service } = setup();
  const demand = service.createDemand({ projectId: 'p', title: '需求', actor: 'human' });
  const approval = await service.requestPlan({ projectId: 'p', taskId: demand.id, actor: 'human' });
  const { tasks } = service.decidePlan({ approvalId: approval.id, decision: 'approved', actor: 'human' });
  await service.dispatch({ taskId: tasks[0]!.id, actor: 'human' });

  const events = store.listEvents('p');
  assert.deepEqual(events.map((event) => event.seq), events.map((_, index) => index + 1));
  assert.deepEqual(
    events.map((event) => event.type),
    ['card.created', 'plan.requested', 'plan.approved', 'card.created', 'card.created', 'run.queued', 'run.started', 'run.completed'],
  );
});

test('Router 计划与 Worker 报告的解析能容忍围栏和前后废话', () => {
  const plan = parsePlan('先说说思路。\n```json\n{"summary":"x","cards":[{"title":"t","acceptance":["a"]}]}\n```\n以上。');
  assert.equal(plan.cards[0]!.title, 't');
  assert.equal(plan.cards[0]!.key, 'c1');

  const report = parseReport('好的，报告如下：{"state":"done","summary":"完事","decisions":[],"blockers":[]}');
  assert.equal(report.summary, '完事');

  assert.throws(() => parsePlan('没有 JSON'), /找不到合法 JSON/);
  assert.throws(() => parseReport('{"state":"weird","summary":"x"}'), /state 只能是/);
});
