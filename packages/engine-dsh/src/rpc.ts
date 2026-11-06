/**
 * dsh 的 stdio JSON-RPC。只实现 M0.1 实测过的那几个方法，多一个都不认。
 *
 * 协议细节见 `.agents/notes/proposed/2026-11-06-m0.1-bridge-spike.md`：
 * 七个方法、没有 cancel、shutdown 只作用于进程。
 */

import { createInterface } from 'node:readline';
import type { ChildProcess } from 'node:child_process';

export interface DshEvent {
  type: string;
  seq: number;
  time: number;
  data: Record<string, unknown>;
}

export interface DshNotification {
  sessionId: string;
  event: DshEvent;
}

interface Pending {
  resolve(value: unknown): void;
  reject(error: Error): void;
}

export class DshRpc {
  readonly #child: ChildProcess;
  #nextId = 1;
  readonly #pending = new Map<number, Pending>();
  readonly #listeners = new Set<(notification: DshNotification) => void>();
  #closed = false;

  constructor(child: ChildProcess, onStderr?: (line: string) => void) {
    this.#child = child;
    const lines = createInterface({ input: child.stdout! });
    lines.on('line', (line) => this.#onLine(line));
    if (onStderr !== undefined && child.stderr !== null) {
      createInterface({ input: child.stderr }).on('line', onStderr);
    }
    child.on('exit', (code, signal) => {
      this.#closed = true;
      const reason = `dsh 退出（code=${String(code)} signal=${String(signal)}）`;
      for (const [, pending] of this.#pending) pending.reject(new Error(reason));
      this.#pending.clear();
    });
  }

  get closed(): boolean {
    return this.#closed;
  }

  onNotification(listener: (notification: DshNotification) => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  async call<T>(method: string, params?: unknown, timeoutMs = 120_000): Promise<T> {
    if (this.#closed) throw new Error('dsh 已退出');
    const id = this.#nextId++;
    const result = new Promise<unknown>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.#pending.delete(id);
        reject(new Error(`${method} 超时（${timeoutMs}ms）`));
      }, timeoutMs);
      this.#pending.set(id, {
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
    });
    this.#child.stdin!.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, ...(params === undefined ? {} : { params }) })}\n`);
    return (await result) as T;
  }

  #onLine(line: string): void {
    if (line.trim() === '') return;
    let message: Record<string, unknown>;
    try {
      message = JSON.parse(line) as Record<string, unknown>;
    } catch {
      return;
    }
    if (typeof message.method === 'string') {
      if (message.method === 'session.event') {
        const params = message.params as DshNotification | undefined;
        if (params !== undefined) for (const listener of this.#listeners) listener(params);
      }
      return;
    }
    const id = message.id;
    if (typeof id !== 'number') return;
    const pending = this.#pending.get(id);
    if (pending === undefined) return;
    this.#pending.delete(id);
    const error = message.error as { message?: string } | undefined;
    if (error !== undefined) pending.reject(new Error(error.message ?? 'dsh 报了错'));
    else pending.resolve(message.result);
  }
}
