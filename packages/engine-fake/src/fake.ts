/**
 * 按场景回放事件的假引擎。它实现的是端口协议，不是 dsh——H0 的 CI 全部跑在它上面。
 *
 * 时间戳从场景的 baseTime 起、每条事件隔一秒，完全确定；seq 由端口层发号。
 */

import type {
  Capabilities,
  CloseParams,
  CloseResult,
  CreateSessionParams,
  CreateSessionResult,
  Emit,
  EngineBackend,
  InterruptParams,
  InterruptResult,
  RespondParams,
  RespondResult,
  SendParams,
  SendResult,
  SubscribeParams,
  SubscribeResult,
} from '@tina/engine-port';
import { pickRule, type Scenario } from './scenario.ts';

interface SessionState {
  sessionId: string;
  interrupted: boolean;
  closed: boolean;
  receipts: number;
}

export interface FakeEngineOptions {
  /** 每条事件之间额外等多久，默认 0；想让测试有窗口 interrupt 就调大。 */
  intervalMs?: number;
}

export function createFakeBackend(scenario: Scenario, options: FakeEngineOptions = {}): (emit: Emit) => EngineBackend {
  return (emit) => new FakeEngine(scenario, options, emit);
}

class FakeEngine implements EngineBackend {
  readonly #scenario: Scenario;
  readonly #intervalMs: number;
  readonly #emit: Emit;
  readonly #sessions = new Map<string, SessionState>();
  #created = 0;

  constructor(scenario: Scenario, options: FakeEngineOptions, emit: Emit) {
    this.#scenario = scenario;
    this.#intervalMs = options.intervalMs ?? 0;
    this.#emit = emit;
  }

  capabilities(): Capabilities {
    return { ...this.#scenario.capabilities };
  }

  createSession(_params: CreateSessionParams): Promise<CreateSessionResult> {
    this.#created += 1;
    const sessionId = `fake-session-${this.#created}`;
    this.#sessions.set(sessionId, { sessionId, interrupted: false, closed: false, receipts: 0 });
    return Promise.resolve({ sessionId });
  }

  send(params: SendParams): Promise<SendResult> {
    const session = this.#require(params.sessionId);
    session.receipts += 1;
    session.interrupted = false;
    const events = pickRule(this.#scenario, params.input);
    void this.#replay(session, events);
    return Promise.resolve({ receipt: `${session.sessionId}/receipt-${session.receipts}` });
  }

  interrupt(params: InterruptParams): Promise<InterruptResult> {
    const session = this.#require(params.sessionId);
    session.interrupted = true;
    this.#emit('turn.end', { turn: 1, reason: 'cancelled' }, this.#stamp(0));
    return Promise.resolve({ ok: true });
  }

  close(params: CloseParams): Promise<CloseResult> {
    const session = this.#require(params.sessionId);
    session.closed = true;
    return Promise.resolve({ ok: true });
  }

  respond(_params: RespondParams): Promise<RespondResult> {
    return Promise.resolve({ ok: true });
  }

  subscribe(_params: SubscribeParams): Promise<SubscribeResult> {
    return Promise.resolve({ ok: true });
  }

  async #replay(session: SessionState, events: readonly { type: string; payload?: Record<string, unknown>; delayMs?: number }[]): Promise<void> {
    for (const [index, spec] of events.entries()) {
      const delay = (spec.delayMs ?? 0) + this.#intervalMs;
      if (delay > 0) await new Promise<void>((resolve) => setTimeout(resolve, delay));
      if (session.interrupted || session.closed) return;
      this.#emit(spec.type as Parameters<Emit>[0], spec.payload ?? {}, this.#stamp(index));
    }
  }

  #stamp(index: number): string {
    return new Date(Date.parse(this.#scenario.baseTime) + index * 1000).toISOString();
  }

  #require(sessionId: string): SessionState {
    const session = this.#sessions.get(sessionId);
    if (session === undefined) throw new Error(`unknown session ${sessionId}`);
    if (session.closed) throw new Error(`session ${sessionId} is closed`);
    return session;
  }
}
