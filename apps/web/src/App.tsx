import { useCallback, useEffect, useState } from 'react';
import {
  api,
  COLUMNS,
  type AgentView,
  type ApprovalView,
  type BoardView,
  type CardView,
  type ReportView,
  type StateView,
} from './api.ts';

export function App() {
  const [state, setState] = useState<StateView | null>(null);
  const [connected, setConnected] = useState(false);
  const [projectId, setProjectId] = useState<string | null>(null);
  const [board, setBoard] = useState<BoardView | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState('');
  const [agents, setAgents] = useState<AgentView[]>([]);

  useEffect(() => {
    let alive = true;
    const load = async (): Promise<void> => {
      try {
        const next = await api.state();
        if (!alive) return;
        setState(next);
        setConnected(true);
        setProjectId((current) => current ?? next.projects[0]?.id ?? null);
      } catch {
        if (alive) setConnected(false);
      }
    };
    void load();
    const timer = setInterval(() => void load(), 2000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, []);

  useEffect(() => {
    if (projectId === null) return;
    let alive = true;
    const load = async (): Promise<void> => {
      try {
        const [next, directory] = await Promise.all([api.board(projectId), api.agents(projectId)]);
        if (alive) {
          setBoard(next);
          setAgents(directory);
        }
      } catch {
        // 控制面短暂不可达时保留上一帧。
      }
    };
    void load();
    const timer = setInterval(() => void load(), 1000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [projectId]);

  const act = useCallback(
    async (fn: () => Promise<unknown>) => {
      if (projectId === null) return;
      setBusy(true);
      setError(null);
      try {
        await fn();
        setBoard(await api.board(projectId));
      } catch (cause) {
        setError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        setBusy(false);
      }
    },
    [projectId],
  );

  const project = state?.projects.find((item) => item.id === projectId) ?? null;
  const createProject = async (): Promise<void> => {
    const name = window.prompt('项目名称')?.trim();
    if (!name) return;
    setBusy(true);
    setError(null);
    try {
      const created = await api.createProject(name);
      setState(await api.state());
      setBoard(null);
      setSelected(null);
      setProjectId(created.id);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };
  const cards = board?.cards ?? [];
  const approvals = board?.approvals ?? [];
  const selectedCard = cards.find((card) => card.id === selected) ?? null;
  const online = state?.nodes.some((node) => node.ready) ?? false;

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">Tina</div>
        <nav className="projects">
          {(state?.projects ?? []).map((item) => (
            <button
              key={item.id}
              type="button"
              disabled={busy}
              className={item.id === projectId ? 'tab active' : 'tab'}
              onClick={() => {
                setProjectId(item.id);
                setSelected(null);
                setBoard(null);
              }}
            >
              {item.name}
            </button>
          ))}
          <button type="button" className="tab" disabled={busy || !connected} onClick={() => void createProject()}>
            ＋项目
          </button>
        </nav>
        <div className={online && connected ? 'status online' : 'status offline'}>
          {!connected
            ? state === null
              ? '连接控制面…'
              : '控制面未连接 · 显示上次记录'
            : online
              ? `${state!.nodes
                  .filter((n) => n.ready)
                  .map((n) => n.name)
                  .join('/')} 可执行`
              : '执行节点离线 · 项目可用'}
        </div>
        {state !== null && <div className="version">v{state.version}</div>}
      </header>

      {error !== null && <div className="banner error">{error}</div>}

      {approvals.length > 0 && (
        <section className="approvals">
          {approvals.map((approval) => (
            <ApprovalBanner
              key={approval.id}
              approval={approval}
              busy={busy || !connected}
              onDecide={(decision) => act(() => api.decidePlan(projectId!, approval.id, decision))}
            />
          ))}
        </section>
      )}

      <form
        className="composer"
        onSubmit={(event) => {
          event.preventDefault();
          const title = draft.trim();
          if (title === '') return;
          void act(async () => {
            await api.createCard(projectId!, title, '');
            setDraft('');
          });
        }}
      >
        <input
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          placeholder={project === null ? '先创建一个项目' : `在「${project.name}」建一张卡`}
          disabled={project === null || busy || !connected}
        />
        <button type="submit" disabled={project === null || busy || !connected || draft.trim() === ''}>
          建卡
        </button>
      </form>

      <main className="board">
        {COLUMNS.map((column) => {
          const columnCards = cards.filter((card) => card.status === column.key);
          return (
            <section key={column.key} className="column">
              <h2>
                {column.label}
                <span className="count">{columnCards.length}</span>
              </h2>
              {columnCards.length === 0 && <p className="empty">还没有卡片</p>}
              {columnCards.map((card) => (
                <button
                  key={card.id}
                  type="button"
                  className={card.id === selected ? 'card selected' : 'card'}
                  onClick={() => setSelected(card.id === selected ? null : card.id)}
                >
                  <span className="card-title">{card.title}</span>
                  <span className="card-meta">
                    {card.kind === 'demand' ? '需求' : '任务'}
                    {card.runs.some((run) => run.state === 'queued') && ' · 等待执行'}
                    {card.latestReport !== null && ` · 报告 ${card.latestReport.state}`}
                  </span>
                </button>
              ))}
            </section>
          );
        })}
      </main>

      {selectedCard !== null && (
        <CardDetail
          key={selectedCard.id}
          card={selectedCard}
          projectId={projectId!}
          agents={agents}
          nodes={state?.nodes ?? []}
          busy={busy || !connected}
          onClose={() => setSelected(null)}
          onPlan={() => act(() => api.requestPlan(projectId!, selectedCard.id))}
          onDispatch={(resume) => act(() => api.dispatch(projectId!, selectedCard.id, resume))}
          onAssign={(id) => act(() => api.assign(projectId!, selectedCard.id, id))}
          onCancel={(id) => act(() => api.cancel(projectId!, id))}
          onReconcile={(id) => act(() => api.reconcile(projectId!, id))}
          onReview={(decision) => act(() => api.review(projectId!, selectedCard.id, decision))}
        />
      )}
    </div>
  );
}

function ApprovalBanner(props: {
  approval: ApprovalView;
  busy: boolean;
  onDecide: (decision: 'approved' | 'rejected') => void;
}) {
  const { approval, busy, onDecide } = props;
  const cards = approval.payload.cards ?? [];
  return (
    <article className="approval">
      <div className="approval-head">
        <strong>计划待批</strong>
        <span className="muted">{approval.payload.task?.title ?? ''}</span>
      </div>
      {approval.payload.summary !== undefined && <p className="approval-summary">{approval.payload.summary}</p>}
      <ol className="approval-cards">
        {cards.map((card) => (
          <li key={card.key}>
            <span className="card-title">{card.title}</span>
            <ul className="acceptance">
              {card.acceptance.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          </li>
        ))}
      </ol>
      <div className="approval-actions">
        <button type="button" disabled={busy} onClick={() => onDecide('approved')}>
          批准
        </button>
        <button type="button" className="ghost" disabled={busy} onClick={() => onDecide('rejected')}>
          打回
        </button>
      </div>
    </article>
  );
}

function CardDetail(props: {
  card: CardView;
  projectId: string;
  agents: AgentView[];
  nodes: StateView['nodes'];
  busy: boolean;
  onClose: () => void;
  onPlan: () => void;
  onDispatch: (resume?: string) => void;
  onAssign: (id: string) => void;
  onCancel: (id: string) => void;
  onReconcile: (id: string) => void;
  onReview: (decision: 'approved' | 'rejected') => void;
}) {
  const { card, busy, onClose, onPlan, onDispatch, onReview } = props;
  const [historyId, setHistoryId] = useState('');
  const [history, setHistory] = useState<ReportView | null>(null);
  const [historyError, setHistoryError] = useState('');
  useEffect(() => {
    if (!historyId) return;
    let alive = true;
    setHistory(null);
    setHistoryError('');
    void api
      .run(props.projectId, historyId)
      .then((value) => {
        if (alive) setHistory(value.report);
      })
      .catch((error: unknown) => {
        if (alive) setHistoryError(String(error));
      });
    return () => {
      alive = false;
    };
  }, [historyId, props.projectId, card.updatedAt]);
  const report = historyId ? history : card.latestReport;
  const active = card.runs.some((run) => ['queued', 'dispatched', 'running'].includes(run.state));
  const canPlan = card.kind === 'demand' && !card.planApproved && !active;
  const canDispatch =
    card.kind === 'task' &&
    card.planApproved &&
    card.status !== 'done' &&
    !active &&
    !card.runs.some((run) => run.needsReconciliation);
  const canReview = card.status === 'review';
  const latest = card.runs.at(-1);

  return (
    <aside className="detail">
      <header>
        <h3>{card.title}</h3>
        <button type="button" className="ghost" onClick={onClose}>
          关闭
        </button>
      </header>
      <p className="muted">
        {card.kind === 'demand' ? '需求卡' : '任务卡'} · {card.id}
      </p>
      {card.description !== '' && <pre className="description">{card.description}</pre>}
      {card.kind === 'task' && (
        <label>
          执行 Agent{' '}
          <select
            aria-label="执行 Agent"
            disabled={busy || active}
            value={card.assigneeId ?? ''}
            onChange={(event) => props.onAssign(event.target.value)}
          >
            {!props.agents.some((agent) => agent.id === card.assigneeId) && (
              <option value={card.assigneeId ?? ''}>{card.assigneeId ?? '选择 Worker'}</option>
            )}
            {props.agents
              .filter((agent) => agent.role === 'worker')
              .map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.name} · {agent.engineId} ·{' '}
                  {props.nodes.some((node) => node.id === agent.nodeId && node.ready) ? '在线' : '未就绪'}
                </option>
              ))}
          </select>
        </label>
      )}

      <h4>Run</h4>
      {card.runs.length === 0 ? (
        <p className="muted">还没有跑过</p>
      ) : (
        <ul className="runs">
          {card.runs.map((run) => (
            <li key={run.id}>
              <span className={`pill ${run.state}`}>{run.state}</span>
              {run.state === 'queued' && <span className="muted"> 等待执行服务接单</span>}
              {run.error !== null && <span className="muted"> {run.error}</span>}
              {run.cancellation && (
                <p>
                  取消请求：{run.cancellation.state}（{run.cancellation.admission ?? '等待回执'}）
                </p>
              )}
              {run.needsReconciliation && (
                <p className="banner error">
                  待核查：{run.diagnostic || '执行或清理结果尚未确认，请核查工作目录和外部状态。'}
                </p>
              )}
              {run.stop && (
                <p className="muted">
                  停止方式 {run.stop.mode} · 主进程 {run.stop.root} · 工具进程 {run.stop.workload} ·{' '}
                  {run.stop.sessionUsable ? '会话可继续' : '续跑需恢复原生会话'}
                </p>
              )}
              {['queued', 'dispatched', 'running'].includes(run.state) && (
                <button type="button" disabled={busy || !!run.cancellation} onClick={() => props.onCancel(run.id)}>
                  {run.state === 'queued' ? '撤回排队' : '请求取消'}
                </button>
              )}
              {run.needsReconciliation && (
                <button type="button" disabled={busy} onClick={() => props.onReconcile(run.id)}>
                  重新对账
                </button>
              )}
            </li>
          ))}
        </ul>
      )}

      <h4>报告</h4>
      {card.runs.length > 1 && (
        <select aria-label="报告版本" value={historyId} onChange={(event) => setHistoryId(event.target.value)}>
          <option value="">最新报告</option>
          {card.runs.map((run, index) => (
            <option key={run.id} value={run.id}>
              第 {index + 1} 次 · {run.state}
            </option>
          ))}
        </select>
      )}
      {historyError && <p className="banner error">{historyError}</p>}
      {report === null ? (
        <p className="muted">还没有报告</p>
      ) : (
        <div className="report">
          <div>
            <span className={`pill ${report.state}`}>{report.state}</span>
            {report.generated && <span className="muted">（控制面兜底生成）</span>}
          </div>
          <p>{report.summary}</p>
          {report.blockers.length > 0 && (
            <>
              <h5>卡住的地方</h5>
              <ul>
                {report.blockers.map((item, index) => (
                  <li key={index}>{String(item)}</li>
                ))}
              </ul>
            </>
          )}
          {report.decisions.length > 0 && (
            <>
              <h5>做过的判断</h5>
              <ul>
                {report.decisions.map((item, index) => (
                  <li key={index}>{String(item)}</li>
                ))}
              </ul>
            </>
          )}
          {report.artifacts.length > 0 && (
            <>
              <h5>产物</h5>
              <ul>
                {report.artifacts.map((item, index) => (
                  <li key={index}>{describeArtifact(item)}</li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}

      <div className="detail-actions">
        {canPlan && (
          <button type="button" disabled={busy} onClick={onPlan}>
            让 Router 拆卡
          </button>
        )}
        {canDispatch && (
          <button type="button" disabled={busy} onClick={() => onDispatch()}>
            派给 worker
          </button>
        )}
        {canDispatch && latest?.handle && latest.handle.metadata?.resume !== 'false' && (
          <button type="button" disabled={busy} onClick={() => onDispatch(latest.id)}>
            恢复原生会话续跑
          </button>
        )}
        {canReview && (
          <>
            <button type="button" disabled={busy} onClick={() => onReview('approved')}>
              终审通过
            </button>
            <button type="button" className="ghost" disabled={busy} onClick={() => onReview('rejected')}>
              打回
            </button>
          </>
        )}
      </div>
    </aside>
  );
}

function describeArtifact(item: unknown): string {
  if (typeof item === 'string') return item;
  if (typeof item === 'object' && item !== null) {
    const artifact = item as { path?: unknown; note?: unknown };
    const path = typeof artifact.path === 'string' ? artifact.path : JSON.stringify(item);
    return typeof artifact.note === 'string' ? `${path} — ${artifact.note}` : path;
  }
  return String(item);
}
