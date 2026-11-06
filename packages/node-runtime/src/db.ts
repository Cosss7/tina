/**
 * 节点侧的 SQLite。表结构照 `docs/h0.md` 第 6 节，一行不差。
 *
 * 用 Node 内置的 node:sqlite，不引原生依赖——`make verify` 要能离线跑，
 * 多一个需要编译的东西就多一个坏掉的理由。
 */

import { DatabaseSync } from 'node:sqlite';
import { randomUUID } from 'node:crypto';

export const SCHEMA = `
create table if not exists task (
  id              text primary key,
  project_id      text not null,
  title           text not null,
  description     text not null default '',
  status          text not null,
  kind            text not null default 'task',
  assignee_kind   text not null default 'agent',
  assignee_id     text,
  deps_json       text not null default '[]',
  manual_override integer not null default 0,
  plan_approved   integer not null default 0,
  created_at      text not null,
  updated_at      text not null
);

create table if not exists run (
  id            text primary key,
  task_id       text not null,
  agent_id      text not null,
  node_id       text not null,
  originator_id text not null,
  state         text not null,
  started_at    text,
  ended_at      text,
  error         text
);

create table if not exists report (
  id              text primary key,
  run_id          text not null,
  state           text not null,
  summary         text not null default '',
  decisions_json  text not null default '[]',
  artifacts_json  text not null default '[]',
  blockers_json   text not null default '[]',
  usage_json      text not null default '{}',
  generated       integer not null default 0
);

create table if not exists approval (
  id            text primary key,
  project_id    text not null,
  task_id       text,
  kind          text not null,
  state         text not null,
  payload_json  text not null default '{}',
  requested_at  text not null,
  decided_at    text,
  decided_by    text
);

create table if not exists event (
  id           text primary key,
  project_id   text not null,
  seq          integer not null,
  type         text not null,
  actor        text not null,
  payload_json text not null default '{}',
  created_at   text not null
);

create index if not exists task_project on task (project_id);
create index if not exists run_task on run (task_id);
create index if not exists report_run on report (run_id);
create index if not exists approval_project on approval (project_id);
create index if not exists approval_task on approval (task_id);
create index if not exists event_project_seq on event (project_id, seq);
`;

export type TaskStatus = 'todo' | 'doing' | 'review' | 'done';
export type TaskKind = 'demand' | 'task';
export type RunState = 'queued' | 'dispatched' | 'running' | 'completed' | 'failed' | 'cancelled';
export type ApprovalKind = 'plan' | 'final';
export type ApprovalState = 'pending' | 'approved' | 'rejected';

export interface Task {
  id: string;
  projectId: string;
  title: string;
  description: string;
  status: TaskStatus;
  kind: TaskKind;
  assigneeKind: string;
  assigneeId: string | null;
  deps: string[];
  manualOverride: boolean;
  planApproved: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface Run {
  id: string;
  taskId: string;
  agentId: string;
  nodeId: string;
  originatorId: string;
  state: RunState;
  startedAt: string | null;
  endedAt: string | null;
  error: string | null;
}

export interface Report {
  id: string;
  runId: string;
  state: string;
  summary: string;
  decisions: unknown[];
  artifacts: unknown[];
  blockers: unknown[];
  usage: Record<string, unknown>;
  generated: boolean;
}

export interface Approval {
  id: string;
  projectId: string;
  taskId: string | null;
  kind: ApprovalKind;
  state: ApprovalState;
  payload: Record<string, unknown>;
  requestedAt: string;
  decidedAt: string | null;
  decidedBy: string | null;
}

export interface ProjectEvent {
  id: string;
  projectId: string;
  seq: number;
  type: string;
  actor: string;
  payload: Record<string, unknown>;
  createdAt: string;
}

const now = (): string => new Date().toISOString();
const newId = (prefix: string): string => `${prefix}_${randomUUID()}`;

export class Store {
  readonly #db: DatabaseSync;

  constructor(filename = ':memory:') {
    this.#db = new DatabaseSync(filename);
    this.#db.exec('pragma journal_mode = WAL');
    this.#db.exec('pragma foreign_keys = ON');
    this.#db.exec(SCHEMA);
  }

  close(): void {
    this.#db.close();
  }

  // ── 任务卡 ───────────────────────────────────────────────────────────────

  createTask(input: {
    projectId: string;
    title: string;
    description?: string;
    kind?: TaskKind;
    assigneeId?: string | null;
    deps?: string[];
    /** 由已批的计划拆出来的卡，出生就是批过的。 */
    planApproved?: boolean;
  }): Task {
    const task: Task = {
      id: newId('task'),
      projectId: input.projectId,
      title: input.title,
      description: input.description ?? '',
      status: 'todo',
      kind: input.kind ?? 'task',
      assigneeKind: 'agent',
      assigneeId: input.assigneeId ?? null,
      deps: input.deps ?? [],
      manualOverride: false,
      planApproved: input.planApproved ?? false,
      createdAt: now(),
      updatedAt: now(),
    };
    this.#db
      .prepare(
        `insert into task (id, project_id, title, description, status, kind, assignee_kind, assignee_id,
                           deps_json, manual_override, plan_approved, created_at, updated_at)
         values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      )
      .run(
        task.id, task.projectId, task.title, task.description, task.status, task.kind,
        task.assigneeKind, task.assigneeId, JSON.stringify(task.deps),
        task.manualOverride ? 1 : 0, task.planApproved ? 1 : 0, task.createdAt, task.updatedAt,
      );
    return task;
  }

  getTask(id: string): Task | undefined {
    const row = this.#db.prepare('select * from task where id = ?').get(id);
    return row === undefined ? undefined : mapTask(row as unknown as RawTask);
  }

  listTasks(projectId: string): Task[] {
    const rows = this.#db.prepare('select * from task where project_id = ? order by created_at, id').all(projectId);
    return rows.map((row) => mapTask(row as unknown as RawTask));
  }

  updateTask(task: Task, patch: Partial<Pick<Task, 'status' | 'planApproved' | 'manualOverride' | 'assigneeId' | 'description'>>): Task {
    const next: Task = { ...task, ...patch, updatedAt: now() };
    this.#db
      .prepare('update task set status = ?, plan_approved = ?, manual_override = ?, assignee_id = ?, description = ?, updated_at = ? where id = ?')
      .run(
        next.status, next.planApproved ? 1 : 0, next.manualOverride ? 1 : 0,
        next.assigneeId, next.description, next.updatedAt, next.id,
      );
    return next;
  }

  // ── Run ─────────────────────────────────────────────────────────────────

  createRun(input: { taskId: string; agentId: string; nodeId: string; originatorId: string }): Run {
    const run: Run = {
      id: newId('run'),
      taskId: input.taskId,
      agentId: input.agentId,
      nodeId: input.nodeId,
      originatorId: input.originatorId,
      state: 'queued',
      startedAt: null,
      endedAt: null,
      error: null,
    };
    this.#db
      .prepare('insert into run (id, task_id, agent_id, node_id, originator_id, state, started_at, ended_at, error) values (?, ?, ?, ?, ?, ?, ?, ?, ?)')
      .run(run.id, run.taskId, run.agentId, run.nodeId, run.originatorId, run.state, null, null, null);
    return run;
  }

  setRunState(runId: string, state: RunState, options: { error?: string | null } = {}): void {
    const finished = state === 'completed' || state === 'failed' || state === 'cancelled';
    const startedAt = this.#db.prepare('select started_at from run where id = ?').get(runId) as unknown as { started_at: string | null } | undefined;
    const start = startedAt?.started_at ?? (state === 'running' ? now() : null);
    this.#db
      .prepare('update run set state = ?, started_at = ?, ended_at = ?, error = ? where id = ?')
      .run(state, start, finished ? now() : null, options.error ?? null, runId);
  }

  getRun(id: string): Run | undefined {
    const row = this.#db.prepare('select * from run where id = ?').get(id);
    return row === undefined ? undefined : mapRun(row as unknown as RawRun);
  }

  listRuns(taskId: string): Run[] {
    const rows = this.#db.prepare('select * from run where task_id = ? order by rowid').all(taskId);
    return rows.map((row) => mapRun(row as unknown as RawRun));
  }

  // ── 报告 ────────────────────────────────────────────────────────────────

  createReport(input: {
    runId: string;
    state: string;
    summary: string;
    decisions?: unknown[];
    artifacts?: unknown[];
    blockers?: unknown[];
    usage?: Record<string, unknown>;
    generated?: boolean;
  }): Report {
    const report: Report = {
      id: newId('rep'),
      runId: input.runId,
      state: input.state,
      summary: input.summary,
      decisions: input.decisions ?? [],
      artifacts: input.artifacts ?? [],
      blockers: input.blockers ?? [],
      usage: input.usage ?? {},
      generated: input.generated ?? false,
    };
    this.#db
      .prepare('insert into report (id, run_id, state, summary, decisions_json, artifacts_json, blockers_json, usage_json, generated) values (?, ?, ?, ?, ?, ?, ?, ?, ?)')
      .run(
        report.id, report.runId, report.state, report.summary,
        JSON.stringify(report.decisions), JSON.stringify(report.artifacts),
        JSON.stringify(report.blockers), JSON.stringify(report.usage),
        report.generated ? 1 : 0,
      );
    return report;
  }

  getReportByRun(runId: string): Report | undefined {
    const row = this.#db.prepare('select * from report where run_id = ?').get(runId);
    return row === undefined ? undefined : mapReport(row as unknown as RawReport);
  }

  /** 任务最近一次的报告，用于卡片详情和状态推导。 */
  latestReport(taskId: string): Report | undefined {
    const row = this.#db
      .prepare('select r.* from report r join run u on u.id = r.run_id where u.task_id = ? order by u.rowid desc limit 1')
      .get(taskId);
    return row === undefined ? undefined : mapReport(row as unknown as RawReport);
  }

  // ── 审批 ────────────────────────────────────────────────────────────────

  createApproval(input: {
    projectId: string;
    taskId: string | null;
    kind: ApprovalKind;
    payload: Record<string, unknown>;
  }): Approval {
    const approval: Approval = {
      id: newId('apr'),
      projectId: input.projectId,
      taskId: input.taskId,
      kind: input.kind,
      state: 'pending',
      payload: input.payload,
      requestedAt: now(),
      decidedAt: null,
      decidedBy: null,
    };
    this.#db
      .prepare('insert into approval (id, project_id, task_id, kind, state, payload_json, requested_at, decided_at, decided_by) values (?, ?, ?, ?, ?, ?, ?, ?, ?)')
      .run(approval.id, approval.projectId, approval.taskId, approval.kind, approval.state, JSON.stringify(approval.payload), approval.requestedAt, null, null);
    return approval;
  }

  getApproval(id: string): Approval | undefined {
    const row = this.#db.prepare('select * from approval where id = ?').get(id);
    return row === undefined ? undefined : mapApproval(row as unknown as RawApproval);
  }

  listApprovals(projectId: string, state?: ApprovalState): Approval[] {
    const rows = state === undefined
      ? this.#db.prepare('select * from approval where project_id = ? order by requested_at, id').all(projectId)
      : this.#db.prepare('select * from approval where project_id = ? and state = ? order by requested_at, id').all(projectId, state);
    return rows.map((row) => mapApproval(row as unknown as RawApproval));
  }

  /** 某个任务的最终审批（人终审）。 */
  finalApproval(taskId: string): Approval | undefined {
    const row = this.#db.prepare("select * from approval where task_id = ? and kind = 'final' order by requested_at desc limit 1").get(taskId);
    return row === undefined ? undefined : mapApproval(row as unknown as RawApproval);
  }

  decideApproval(id: string, state: 'approved' | 'rejected', decidedBy: string): Approval {
    this.#db.prepare('update approval set state = ?, decided_at = ?, decided_by = ? where id = ?').run(state, now(), decidedBy, id);
    const approval = this.getApproval(id);
    if (approval === undefined) throw new Error(`审批 ${id} 不见了`);
    return approval;
  }

  // ── 事件（只追加） ───────────────────────────────────────────────────────

  appendEvent(input: { projectId: string; type: string; actor: string; payload?: Record<string, unknown> }): ProjectEvent {
    const next = this.#db.prepare('select coalesce(max(seq), 0) + 1 as seq from event where project_id = ?').get(input.projectId) as unknown as { seq: number };
    const event: ProjectEvent = {
      id: newId('evt'),
      projectId: input.projectId,
      seq: next.seq,
      type: input.type,
      actor: input.actor,
      payload: input.payload ?? {},
      createdAt: now(),
    };
    this.#db
      .prepare('insert into event (id, project_id, seq, type, actor, payload_json, created_at) values (?, ?, ?, ?, ?, ?, ?)')
      .run(event.id, event.projectId, event.seq, event.type, event.actor, JSON.stringify(event.payload), event.createdAt);
    return event;
  }

  listEvents(projectId: string): ProjectEvent[] {
    const rows = this.#db.prepare('select * from event where project_id = ? order by seq').all(projectId);
    return rows.map((row) => {
      const raw = row as unknown as RawEvent;
      return {
        id: raw.id,
        projectId: raw.project_id,
        seq: raw.seq,
        type: raw.type,
        actor: raw.actor,
        payload: JSON.parse(raw.payload_json) as Record<string, unknown>,
        createdAt: raw.created_at,
      };
    });
  }
}

interface RawTask {
  id: string; project_id: string; title: string; description: string; status: string; kind: string;
  assignee_kind: string; assignee_id: string | null; deps_json: string; manual_override: number;
  plan_approved: number; created_at: string; updated_at: string;
}
interface RawRun {
  id: string; task_id: string; agent_id: string; node_id: string; originator_id: string;
  state: string; started_at: string | null; ended_at: string | null; error: string | null;
}
interface RawReport {
  id: string; run_id: string; state: string; summary: string; decisions_json: string;
  artifacts_json: string; blockers_json: string; usage_json: string; generated: number;
}
interface RawApproval {
  id: string; project_id: string; task_id: string | null; kind: string; state: string;
  payload_json: string; requested_at: string; decided_at: string | null; decided_by: string | null;
}
interface RawEvent {
  id: string; project_id: string; seq: number; type: string; actor: string; payload_json: string; created_at: string;
}

const mapTask = (raw: RawTask): Task => ({
  id: raw.id,
  projectId: raw.project_id,
  title: raw.title,
  description: raw.description,
  status: raw.status as TaskStatus,
  kind: raw.kind as TaskKind,
  assigneeKind: raw.assignee_kind,
  assigneeId: raw.assignee_id,
  deps: JSON.parse(raw.deps_json) as string[],
  manualOverride: raw.manual_override === 1,
  planApproved: raw.plan_approved === 1,
  createdAt: raw.created_at,
  updatedAt: raw.updated_at,
});

const mapRun = (raw: RawRun): Run => ({
  id: raw.id,
  taskId: raw.task_id,
  agentId: raw.agent_id,
  nodeId: raw.node_id,
  originatorId: raw.originator_id,
  state: raw.state as RunState,
  startedAt: raw.started_at,
  endedAt: raw.ended_at,
  error: raw.error,
});

const mapReport = (raw: RawReport): Report => ({
  id: raw.id,
  runId: raw.run_id,
  state: raw.state,
  summary: raw.summary,
  decisions: JSON.parse(raw.decisions_json) as unknown[],
  artifacts: JSON.parse(raw.artifacts_json) as unknown[],
  blockers: JSON.parse(raw.blockers_json) as unknown[],
  usage: JSON.parse(raw.usage_json) as Record<string, unknown>,
  generated: raw.generated === 1,
});

const mapApproval = (raw: RawApproval): Approval => ({
  id: raw.id,
  projectId: raw.project_id,
  taskId: raw.task_id,
  kind: raw.kind as ApprovalKind,
  state: raw.state as ApprovalState,
  payload: JSON.parse(raw.payload_json) as Record<string, unknown>,
  requestedAt: raw.requested_at,
  decidedAt: raw.decided_at,
  decidedBy: raw.decided_by,
});
