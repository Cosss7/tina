/** 控制面项目 API 的薄客户端。模型请求返回待执行 Run。 */

export interface NodeView {
  id: string;
  name: string;
  version: string;
  online: boolean;
  lastSeenAt: string;
}

export interface ProjectView {
  id: string;
  name: string;
}

export interface StateView {
  version: string;
  nodes: NodeView[];
  projects: ProjectView[];
}

export interface RunView {
  id: string;
  state: string;
  startedAt: string | null;
  endedAt: string | null;
  error: string | null;
}

export interface ReportView {
  state: string;
  summary: string;
  decisions: unknown[];
  artifacts: unknown[];
  blockers: unknown[];
  generated: boolean;
}

export interface CardView {
  id: string;
  title: string;
  description: string;
  status: 'todo' | 'doing' | 'review' | 'done';
  kind: 'demand' | 'task';
  assigneeId: string | null;
  planApproved: boolean;
  runs: RunView[];
  latestReport: ReportView | null;
  updatedAt: string;
}

export interface PlanCard {
  key: string;
  title: string;
  description: string;
  acceptance: string[];
  deps: string[];
}

export interface ApprovalView {
  id: string;
  kind: 'plan' | 'final';
  state: string;
  taskId: string | null;
  payload: { summary?: string; cards?: PlanCard[]; task?: { id: string; title: string }; note?: string };
  requestedAt: string;
}

export interface BoardView {
  projectId: string;
  cards: CardView[];
  approvals: ApprovalView[];
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { 'content-type': 'application/json', ...(init?.headers ?? {}) },
    cache: 'no-store',
  });
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { error?: { message?: string } };
    throw new Error(body.error?.message ?? `Request failed (${response.status})`);
  }
  return (await response.json()) as T;
}

export const api = {
  state: () => request<StateView>('/api/state'),
  createProject: (name: string) => request<ProjectView>('/api/projects', { method: 'POST', body: JSON.stringify({ name }) }),
  board: (projectId: string) => request<BoardView>(`/api/board?project=${encodeURIComponent(projectId)}`),
  createCard: (projectId: string, title: string, description: string) =>
    request<CardView>('/api/cards', { method: 'POST', body: JSON.stringify({ projectId, title, description }) }),
  requestPlan: (projectId: string, taskId: string) =>
    request<{ run: RunView }>('/api/plans', { method: 'POST', body: JSON.stringify({ projectId, taskId }) }),
  decidePlan: (projectId: string, approvalId: string, decision: 'approved' | 'rejected') =>
    request<unknown>(`/api/plans/${approvalId}/decide`, { method: 'POST', body: JSON.stringify({ projectId, decision }) }),
  dispatch: (projectId: string, taskId: string) =>
    request<unknown>(`/api/tasks/${taskId}/dispatch`, { method: 'POST', body: JSON.stringify({ projectId }) }),
  review: (projectId: string, taskId: string, decision: 'approved' | 'rejected', note = '') =>
    request<unknown>(`/api/tasks/${taskId}/review`, { method: 'POST', body: JSON.stringify({ projectId, decision, note }) }),
};

export const COLUMNS: Array<{ key: CardView['status']; label: string }> = [
  { key: 'todo', label: '待办' },
  { key: 'doing', label: '进行中' },
  { key: 'review', label: '评审中' },
  { key: 'done', label: '已完成' },
];
