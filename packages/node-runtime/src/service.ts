/**
 * 节点上的闭环：建卡 → Router 拆卡 → 批计划 → 派 worker → 收报告 → 评审 → 终审。
 *
 * 每一步都写事件、都重算状态，所以数据库里的状态与 `docs/h0.md` 第 7 节对得上，
 * 而不是靠某处偷偷改了一笔。
 */

import type { Approval, Run, Store, Task } from './db.ts';
import { parsePlan, parseReport, refreshStatus, type Plan, type WorkerReport } from './domain.ts';
import type { TurnResult } from './engine-run.ts';
import { buildPlanPrompt, buildReportPrompt } from './prompts.ts';

export type TurnRunner = (prompt: string) => Promise<TurnResult>;

export class ServiceError extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(message);
    this.name = 'ServiceError';
    this.code = code;
  }
}

export interface ServiceOptions {
  store: Store;
  nodeId: string;
  /** 怎么跑一轮会话。测试里换成假实现，正式运行走 fake engine 子进程。 */
  runTurn: TurnRunner;
  /** worker 的 agent id，H0 只有一个。 */
  workerAgentId?: string;
}

export interface CardView {
  id: string;
  title: string;
  description: string;
  status: string;
  kind: string;
  assigneeId: string | null;
  planApproved: boolean;
  runs: Array<{ id: string; state: string; startedAt: string | null; endedAt: string | null; error: string | null }>;
  latestReport: {
    state: string;
    summary: string;
    decisions: unknown[];
    artifacts: unknown[];
    blockers: unknown[];
    generated: boolean;
  } | null;
  updatedAt: string;
}

export interface Board {
  projectId: string;
  cards: CardView[];
  approvals: Array<{ id: string; kind: string; state: string; taskId: string | null; payload: Record<string, unknown>; requestedAt: string }>;
}

export class Service {
  readonly #store: Store;
  readonly #nodeId: string;
  readonly #runTurn: TurnRunner;
  readonly #workerAgentId: string;
  readonly #inflight = new Map<string, Promise<void>>();

  constructor(options: ServiceOptions) {
    this.#store = options.store;
    this.#nodeId = options.nodeId;
    this.#runTurn = options.runTurn;
    this.#workerAgentId = options.workerAgentId ?? 'worker-1';
  }

  // ── 看板 ─────────────────────────────────────────────────────────────────

  board(projectId: string): Board {
    const cards = this.#store.listTasks(projectId).map((task) => {
      const { task: fresh } = refreshStatus(this.#store, task);
      const runs = this.#store.listRuns(fresh.id);
      const latestRun = runs.at(-1);
      const report = latestRun === undefined ? undefined : this.#store.getReportByRun(latestRun.id);
      return {
        id: fresh.id,
        title: fresh.title,
        description: fresh.description,
        status: fresh.status,
        kind: fresh.kind,
        assigneeId: fresh.assigneeId,
        planApproved: fresh.planApproved,
        runs: runs.map((run) => ({ id: run.id, state: run.state, startedAt: run.startedAt, endedAt: run.endedAt, error: run.error })),
        latestReport:
          report === undefined
            ? null
            : {
                state: report.state,
                summary: report.summary,
                decisions: report.decisions,
                artifacts: report.artifacts,
                blockers: report.blockers,
                generated: report.generated,
              },
        updatedAt: fresh.updatedAt,
      } satisfies CardView;
    });
    const approvals = this.#store.listApprovals(projectId, 'pending').map((approval) => ({
      id: approval.id,
      kind: approval.kind,
      state: approval.state,
      taskId: approval.taskId,
      payload: approval.payload,
      requestedAt: approval.requestedAt,
    }));
    return { projectId, cards, approvals };
  }

  // ── 建卡 ─────────────────────────────────────────────────────────────────

  createDemand(input: { projectId: string; title: string; description?: string; actor: string }): Task {
    const title = input.title.trim();
    if (title === '') throw new ServiceError('invalid_argument', 'Title is required');
    const task = this.#store.createTask({
      projectId: input.projectId,
      title,
      description: input.description ?? '',
      kind: 'demand',
    });
    this.#store.appendEvent({
      projectId: input.projectId,
      type: 'card.created',
      actor: input.actor,
      payload: { taskId: task.id, title: task.title },
    });
    return task;
  }

  // ── 拆卡 ─────────────────────────────────────────────────────────────────

  async requestPlan(input: { projectId: string; taskId: string; actor: string }): Promise<Approval> {
    const task = this.#requireTask(input.taskId);
    if (task.projectId !== input.projectId) throw new ServiceError('not_found', 'Card does not belong to this project');
    if (task.planApproved) throw new ServiceError('conflict', 'Card plan is already approved');

    const open = this.#store.listApprovals(input.projectId, 'pending').filter((approval) => approval.kind === 'plan' && approval.taskId === task.id);
    if (open.length > 0) throw new ServiceError('conflict', 'Card already has a pending plan');

    const result = await this.#runTurn(buildPlanPrompt({ title: task.title, description: task.description }));
    let plan: Plan;
    try {
      plan = parsePlan(result.text);
    } catch (error) {
      throw new ServiceError('plan_unparsable', error instanceof Error ? error.message : String(error));
    }

    const approval = this.#store.createApproval({
      projectId: input.projectId,
      taskId: task.id,
      kind: 'plan',
      payload: { summary: plan.summary, cards: plan.cards, task: { id: task.id, title: task.title } },
    });
    this.#store.appendEvent({
      projectId: input.projectId,
      type: 'plan.requested',
      actor: 'router',
      payload: { approvalId: approval.id, taskId: task.id, cards: plan.cards.length },
    });
    return approval;
  }

  // ── 批计划 ───────────────────────────────────────────────────────────────

  decidePlan(input: { approvalId: string; decision: 'approved' | 'rejected'; actor: string }): { approval: Approval; tasks: Task[] } {
    const approval = this.#store.getApproval(input.approvalId);
    if (approval === undefined) throw new ServiceError('not_found', 'Approval not found');
    if (approval.kind !== 'plan') throw new ServiceError('invalid_argument', 'Approval is not for a plan');
    if (approval.state !== 'pending') throw new ServiceError('conflict', 'Approval has already been decided');

    const decided = this.#store.decideApproval(approval.id, input.decision, input.actor);
    this.#store.appendEvent({
      projectId: approval.projectId,
      type: input.decision === 'approved' ? 'plan.approved' : 'plan.rejected',
      actor: input.actor,
      payload: { approvalId: approval.id, taskId: approval.taskId },
    });
    if (input.decision === 'rejected') return { approval: decided, tasks: [] };

    const cards = Array.isArray(approval.payload.cards) ? (approval.payload.cards as Plan['cards']) : [];
    const created: Task[] = [];
    const keys = new Map<string, string>();
    for (const card of cards) {
      const description = [card.description, '', '验收标准：', ...card.acceptance.map((item) => `- ${item}`)].join('\n').trim();
      const deps = card.deps.map((key) => keys.get(key)).filter((id): id is string => id !== undefined);
      const task = this.#store.createTask({
        projectId: approval.projectId,
        title: card.title,
        description,
        kind: 'task',
        assigneeId: this.#workerAgentId,
        planApproved: true,
        deps,
      });
      keys.set(card.key, task.id);
      created.push(task);
      this.#store.appendEvent({
        projectId: approval.projectId,
        type: 'card.created',
        actor: 'router',
        payload: { taskId: task.id, title: task.title, fromApproval: approval.id },
      });
    }

    if (approval.taskId !== null) {
      const demand = this.#store.getTask(approval.taskId);
      if (demand !== undefined) {
        const approved = this.#store.updateTask(demand, { planApproved: true });
        refreshStatus(this.#store, approved);
      }
    }
    return { approval: decided, tasks: created };
  }

  // ── 派活 ─────────────────────────────────────────────────────────────────

  /**
   * 派活。**立刻返回**：建好 Run、把卡片推进"进行中"，真正的执行在后台跑。
   * 一轮真实会话可能要几十秒，让 HTTP 请求等着它只会等出超时。
   */
  dispatch(input: { taskId: string; actor: string }): { run: Run; task: Task } {
    const task = this.#requireTask(input.taskId);
    if (task.kind === 'demand') throw new ServiceError('invalid_argument', 'Split the request into tasks before dispatching');
    if (!task.planApproved) throw new ServiceError('conflict', 'Plan is not approved');

    const active = this.#store.listRuns(task.id).filter((run) => ['queued', 'dispatched', 'running'].includes(run.state));
    if (active.length > 0) throw new ServiceError('conflict', 'Card already has an active run');

    const run = this.#store.createRun({ taskId: task.id, agentId: this.#workerAgentId, nodeId: this.#nodeId, originatorId: input.actor });
    this.#store.appendEvent({ projectId: task.projectId, type: 'run.queued', actor: input.actor, payload: { runId: run.id, taskId: task.id } });
    this.#store.setRunState(run.id, 'dispatched');
    this.#store.setRunState(run.id, 'running');
    this.#store.appendEvent({ projectId: task.projectId, type: 'run.started', actor: this.#workerAgentId, payload: { runId: run.id, taskId: task.id } });
    const { task: running } = refreshStatus(this.#store, this.#requireTask(task.id));

    this.#inflight.set(run.id, this.#execute(run, task));
    const stored = this.#store.getRun(run.id);
    if (stored === undefined) throw new ServiceError('internal', 'Run not found');
    return { run: stored, task: running };
  }

  /** 等一个 Run 跑完（测试和 demo 用，产品侧靠轮询看板）。 */
  waitForRun(runId: string): Promise<void> {
    return this.#inflight.get(runId) ?? Promise.resolve();
  }

  async #execute(run: Run, task: Task): Promise<void> {
    try {
      const result = await this.#runTurn(buildReportPrompt({ title: task.title, description: task.description }));
      const report = parseReport(result.text);
      this.#store.createReport({
        runId: run.id,
        state: report.state,
        summary: report.summary,
        decisions: report.decisions,
        artifacts: report.artifacts,
        blockers: report.blockers,
        usage: { ...report.usage, engineUsage: result.usage },
      });
      this.#store.setRunState(run.id, 'completed');
      this.#store.appendEvent({ projectId: task.projectId, type: 'run.completed', actor: this.#workerAgentId, payload: { runId: run.id, state: report.state } });
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.#store.createReport({ runId: run.id, state: 'failed', summary: `Run failed: ${message}`, generated: true });
      this.#store.setRunState(run.id, 'failed', { error: message });
      this.#store.appendEvent({ projectId: task.projectId, type: 'run.failed', actor: this.#workerAgentId, payload: { runId: run.id, error: message } });
    } finally {
      refreshStatus(this.#store, this.#requireTask(task.id));
    }
  }

  // ── 人终审 ───────────────────────────────────────────────────────────────

  finalReview(input: { taskId: string; decision: 'approved' | 'rejected'; actor: string; note?: string }): Task {
    const task = this.#requireTask(input.taskId);
    const latest = this.#store.latestReport(task.id);
    if (latest === undefined) throw new ServiceError('conflict', 'No report available for review');

    const existing = this.#store.finalApproval(task.id);
    if (existing?.state === 'approved') throw new ServiceError('conflict', 'Card has already passed final review');

    const approval = this.#store.createApproval({
      projectId: task.projectId,
      taskId: task.id,
      kind: 'final',
      payload: { reportId: latest.id, summary: latest.summary, note: input.note ?? '' },
    });
    this.#store.decideApproval(approval.id, input.decision, input.actor);
    this.#store.appendEvent({
      projectId: task.projectId,
      type: input.decision === 'approved' ? 'review.approved' : 'review.rejected',
      actor: input.actor,
      payload: { taskId: task.id, approvalId: approval.id },
    });
    const { task: refreshed } = refreshStatus(this.#store, this.#requireTask(task.id));
    return refreshed;
  }

  #requireTask(taskId: string): Task {
    const task = this.#store.getTask(taskId);
    if (task === undefined) throw new ServiceError('not_found', `Card not found: ${taskId}`);
    return task;
  }
}
