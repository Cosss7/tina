import { useCallback, useEffect, useState } from 'react';
import { api, COLUMNS, type ApprovalView, type BoardView, type CardView, type StateView } from './api.ts';

export function App() {
  const [state, setState] = useState<StateView | null>(null);
  const [projectId, setProjectId] = useState<string | null>(null);
  const [board, setBoard] = useState<BoardView | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState('');

  useEffect(() => {
    let alive = true;
    const load = async (): Promise<void> => {
      try {
        const next = await api.state();
        if (!alive) return;
        setState(next);
        setProjectId((current) => current ?? next.projects[0]?.id ?? null);
      } catch {
        if (alive) setState((current) => (current === null ? current : { ...current }));
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
        const next = await api.board(projectId);
        if (alive) setBoard(next);
      } catch {
        // 节点离线时保持上一帧，别把看板清空
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
  const cards = board?.cards ?? [];
  const approvals = board?.approvals ?? [];
  const selectedCard = cards.find((card) => card.id === selected) ?? null;
  const online = state?.nodes.some((node) => node.online) ?? false;

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">Tina</div>
        <nav className="projects">
          {(state?.projects ?? []).map((item) => (
            <button
              key={item.id}
              type="button"
              className={item.id === projectId ? 'tab active' : 'tab'}
              onClick={() => {
                setProjectId(item.id);
                setSelected(null);
              }}
            >
              {item.name}
            </button>
          ))}
        </nav>
        <div className={online ? 'status online' : 'status offline'}>
          {state === null ? '连接中…' : online ? `${state.nodes.filter((n) => n.online).map((n) => n.name).join('/')} 在线` : '节点离线'}
        </div>
      </header>

      {error !== null && <div className="banner error">{error}</div>}

      {approvals.length > 0 && (
        <section className="approvals">
          {approvals.map((approval) => (
            <ApprovalBanner
              key={approval.id}
              approval={approval}
              busy={busy}
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
          setDraft('');
          void act(() => api.createCard(projectId!, title, ''));
        }}
      >
        <input
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          placeholder={project === null ? '等节点上线…' : `在「${project.name}」建一张卡`}
          disabled={project === null || busy}
        />
        <button type="submit" disabled={project === null || busy || draft.trim() === ''}>
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
          card={selectedCard}
          busy={busy}
          onClose={() => setSelected(null)}
          onPlan={() => act(() => api.requestPlan(projectId!, selectedCard.id))}
          onDispatch={() => act(() => api.dispatch(projectId!, selectedCard.id))}
          onReview={(decision) => act(() => api.review(projectId!, selectedCard.id, decision))}
        />
      )}
    </div>
  );
}

function ApprovalBanner(props: { approval: ApprovalView; busy: boolean; onDecide: (decision: 'approved' | 'rejected') => void }) {
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
  busy: boolean;
  onClose: () => void;
  onPlan: () => void;
  onDispatch: () => void;
  onReview: (decision: 'approved' | 'rejected') => void;
}) {
  const { card, busy, onClose, onPlan, onDispatch, onReview } = props;
  const report = card.latestReport;
  const canPlan = card.kind === 'demand' && !card.planApproved;
  const canDispatch = card.kind === 'task' && card.planApproved && card.status === 'todo';
  const canReview = card.status === 'review';

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

      <h4>Run</h4>
      {card.runs.length === 0 ? (
        <p className="muted">还没有跑过</p>
      ) : (
        <ul className="runs">
          {card.runs.map((run) => (
            <li key={run.id}>
              <span className={`pill ${run.state}`}>{run.state}</span>
              {run.error !== null && <span className="muted"> {run.error}</span>}
            </li>
          ))}
        </ul>
      )}

      <h4>报告</h4>
      {report === null ? (
        <p className="muted">还没有报告</p>
      ) : (
        <div className="report">
          <div>
            <span className={`pill ${report.state}`}>{report.state}</span>
            {report.generated && <span className="muted">（节点兜底生成）</span>}
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
          <button type="button" disabled={busy} onClick={onDispatch}>
            派给 worker
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
