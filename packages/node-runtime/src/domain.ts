/**
 * 领域规则：卡片状态推导、Router 计划与 Worker 报告的解析。
 *
 * 状态规则就是 `docs/h0.md` 第 7 节那张表，一条一条对上；这里是它唯一的实现，
 * 别处不许再算一遍。
 */

import type { Store, Task, TaskStatus } from './db.ts';

const ACTIVE_RUN_STATES = ['queued', 'dispatched', 'running'];

/**
 * 从 Run 与审批推导卡片状态（D23）。人手动改过的一律以人为准。
 */
export function deriveStatus(store: Store, task: Task): TaskStatus {
  if (task.manualOverride) return task.status;
  if (task.kind === 'demand') {
    // 需求卡在计划批准的那一刻就算完成：它被拆成了子卡，使命结束。
    return task.planApproved ? 'done' : 'todo';
  }

  const final = store.finalApproval(task.id);
  if (final?.state === 'approved') return 'done';

  const runs = store.listRuns(task.id);
  if (runs.some((run) => ACTIVE_RUN_STATES.includes(run.state))) return 'doing';

  // 终端状态的 Run 只要有报告就进评审中——包括 failed 的兜底报告。
  // 不然一次失败会被推导成"待办"，人就再也看不见它了。
  const terminal = runs.filter(
    (run) => run.state === 'completed' || run.state === 'failed' || run.state === 'cancelled',
  );
  const hasReport = terminal.some((run) => store.getReportByRun(run.id) !== undefined);
  if (hasReport) return 'review';

  return 'todo';
}

/** 推导并落库，返回有没有变化。 */
export function refreshStatus(store: Store, task: Task): { task: Task; changed: boolean } {
  const next = deriveStatus(store, task);
  if (next === task.status) return { task, changed: false };
  return { task: store.updateTask(task, { status: next }), changed: true };
}

// ── Router 的计划 ──────────────────────────────────────────────────────────

export interface PlanCard {
  key: string;
  title: string;
  description: string;
  acceptance: string[];
  deps: string[];
}

export interface Plan {
  summary: string;
  cards: PlanCard[];
}

export function parsePlan(text: string): Plan {
  const raw = extractJson(text, 'Router plan');
  const summary = typeof raw.summary === 'string' ? raw.summary : '';
  if (!Array.isArray(raw.cards) || raw.cards.length === 0) throw new Error('Plan cards must be a non-empty array');
  const cards = raw.cards.map((item, index) => {
    if (typeof item !== 'object' || item === null) throw new Error(`cards[${index}] must be an object`);
    const card = item as Record<string, unknown>;
    if (typeof card.title !== 'string' || card.title === '') throw new Error(`cards[${index}].title is required`);
    const acceptance = Array.isArray(card.acceptance)
      ? card.acceptance.filter((entry): entry is string => typeof entry === 'string')
      : [];
    if (acceptance.length === 0) throw new Error(`cards[${index}] must include acceptance criteria`);
    const deps = Array.isArray(card.deps)
      ? card.deps.filter((entry): entry is string => typeof entry === 'string')
      : [];
    return {
      key: typeof card.key === 'string' && card.key !== '' ? card.key : `c${index + 1}`,
      title: card.title,
      description: typeof card.description === 'string' ? card.description : '',
      acceptance,
      deps,
    };
  });
  return { summary, cards };
}

export function toJson(value: unknown): string {
  return JSON.stringify(value);
}

// ── Worker 的报告 ──────────────────────────────────────────────────────────

export interface WorkerReport {
  state: 'done' | 'blocked' | 'failed';
  summary: string;
  decisions: string[];
  artifacts: Array<{ path: string; note?: string }>;
  blockers: string[];
  usage: Record<string, unknown>;
}

export function parseReport(text: string): WorkerReport {
  const raw = extractJson(text, 'Worker report');
  const state = raw.state;
  if (state !== 'done' && state !== 'blocked' && state !== 'failed') {
    throw new Error(`Report state must be done, blocked, or failed; got ${String(state)}`);
  }
  const summary = typeof raw.summary === 'string' ? raw.summary : '';
  if (summary === '') throw new Error('Report summary is required');
  const decisions = Array.isArray(raw.decisions)
    ? raw.decisions.filter((entry): entry is string => typeof entry === 'string')
    : [];
  const blockers = Array.isArray(raw.blockers)
    ? raw.blockers.filter((entry): entry is string => typeof entry === 'string')
    : [];
  const artifacts: Array<{ path: string; note?: string }> = [];
  if (Array.isArray(raw.artifacts)) {
    for (const entry of raw.artifacts) {
      if (typeof entry === 'string') artifacts.push({ path: entry });
      else if (typeof entry === 'object' && entry !== null) {
        const item = entry as Record<string, unknown>;
        if (typeof item.path !== 'string') continue;
        artifacts.push(typeof item.note === 'string' ? { path: item.path, note: item.note } : { path: item.path });
      }
    }
  }
  const usage = typeof raw.usage === 'object' && raw.usage !== null ? (raw.usage as Record<string, unknown>) : {};
  return { state, summary, decisions, artifacts, blockers, usage };
}

/**
 * 从模型输出里抠出第一个完整的 JSON 对象。
 *
 * 模型爱在前后加解释和 ```json 围栏，所以不能直接 JSON.parse 整段。
 */
function extractJson(text: string, label: string): Record<string, unknown> {
  const fenced = /```(?:json)?\s*\n([\s\S]*?)```/i.exec(text);
  const candidates = fenced?.[1] === undefined ? [text] : [fenced[1], text];
  for (const candidate of candidates) {
    const start = candidate.indexOf('{');
    if (start < 0) continue;
    const end = matchBrace(candidate, start);
    if (end < 0) continue;
    try {
      const value: unknown = JSON.parse(candidate.slice(start, end + 1));
      if (typeof value === 'object' && value !== null) return value as Record<string, unknown>;
    } catch {
      // 换下一个候选
    }
  }
  throw new Error(`No valid JSON object found in ${label}`);
}

function matchBrace(text: string, start: number): number {
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let index = start; index < text.length; index += 1) {
    const char = text[index];
    if (inString) {
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === '"') inString = false;
      continue;
    }
    if (char === '"') inString = true;
    else if (char === '{') depth += 1;
    else if (char === '}') {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}
