/**
 * 节点到控制面的连接：注册一次，然后每秒心跳一次，断了按退避重连；
 * 顺带承载控制面转发下来的请求（看板、建卡、拆卡、派活、终审）。
 *
 * 控制面在连接断开时会立刻把节点标成离线，所以"杀掉节点 → 界面显示离线"这条
 * 验收不依赖心跳超时，心跳只是兜住"连接还在但进程卡死"。
 */

import WebSocket from 'ws';
import type { NodeConfig } from './config.ts';

export interface ConnectionEvents {
  onOpen?(): void;
  onClose?(reason: string): void;
  onError?(error: Error): void;
  /** 控制面转发过来的请求。抛出的错误会变成 error 响应。 */
  onRequest?(method: string, params: unknown): Promise<unknown>;
}

export interface ConnectionOptions {
  /** 重连退避的起点，默认 250ms。 */
  retryBaseMs?: number;
  /** 重连退避的上限，默认 2s。 */
  retryMaxMs?: number;
}

interface IncomingRequest {
  type: 'request';
  id: string | number;
  method: string;
  params?: unknown;
}

export class NodeConnection {
  readonly #config: NodeConfig;
  readonly #events: ConnectionEvents;
  readonly #retryBaseMs: number;
  readonly #retryMaxMs: number;
  #socket: WebSocket | null = null;
  #heartbeat: ReturnType<typeof setInterval> | null = null;
  #retry: ReturnType<typeof setTimeout> | null = null;
  #attempt = 0;
  #stopped = false;

  constructor(config: NodeConfig, events: ConnectionEvents = {}, options: ConnectionOptions = {}) {
    this.#config = config;
    this.#events = events;
    this.#retryBaseMs = options.retryBaseMs ?? 250;
    this.#retryMaxMs = options.retryMaxMs ?? 2_000;
  }

  /** 开始连接；重复调用没有副作用。 */
  start(): void {
    if (this.#stopped || this.#socket !== null || this.#retry !== null) return;
    this.#connect();
  }

  /** 主动断开，不再重连。 */
  stop(): void {
    this.#stopped = true;
    this.#clearHeartbeat();
    if (this.#retry !== null) {
      clearTimeout(this.#retry);
      this.#retry = null;
    }
    const socket = this.#socket;
    this.#socket = null;
    socket?.removeAllListeners();
    socket?.close();
  }

  #connect(): void {
    const socket = new WebSocket(this.#config.controlPlaneUrl);
    this.#socket = socket;

    socket.on('open', () => {
      this.#attempt = 0;
      this.#send({
        type: 'register',
        nodeId: this.#config.nodeId,
        name: this.#config.name,
        version: this.#config.version,
        projects: this.#config.projects,
      });
      this.#clearHeartbeat();
      this.#heartbeat = setInterval(() => {
        this.#send({ type: 'heartbeat', nodeId: this.#config.nodeId });
      }, this.#config.heartbeatMs);
      this.#events.onOpen?.();
    });

    socket.on('message', (raw: WebSocket.RawData) => {
      void this.#onMessage(raw);
    });

    socket.on('error', (error: Error) => {
      this.#events.onError?.(error);
    });

    socket.on('close', () => {
      if (this.#socket !== socket) return;
      this.#socket = null;
      this.#clearHeartbeat();
      this.#events.onClose?.('connection closed');
      this.#scheduleRetry();
    });
  }

  async #onMessage(raw: WebSocket.RawData): Promise<void> {
    let message: unknown;
    try {
      message = JSON.parse(raw.toString());
    } catch {
      return;
    }
    if (typeof message !== 'object' || message === null) return;
    const request = message as IncomingRequest;
    if (request.type !== 'request' || typeof request.method !== 'string') return;

    const handler = this.#events.onRequest;
    if (handler === undefined) {
      this.#send({ type: 'response', id: request.id, error: { code: 'unimplemented', message: '节点没有挂请求处理器' } });
      return;
    }
    try {
      const result = await handler(request.method, request.params);
      this.#send({ type: 'response', id: request.id, result });
    } catch (error) {
      this.#send({ type: 'response', id: request.id, error: describeError(error) });
    }
  }

  #scheduleRetry(): void {
    if (this.#stopped || this.#retry !== null) return;
    const delay = Math.min(this.#retryBaseMs * 2 ** this.#attempt, this.#retryMaxMs);
    this.#attempt += 1;
    this.#retry = setTimeout(() => {
      this.#retry = null;
      if (!this.#stopped) this.#connect();
    }, delay);
    // 重连定时器不该吊住进程退出。
    this.#retry.unref?.();
  }

  #clearHeartbeat(): void {
    if (this.#heartbeat !== null) {
      clearInterval(this.#heartbeat);
      this.#heartbeat = null;
    }
  }

  #send(message: Record<string, unknown>): void {
    if (this.#socket === null || this.#socket.readyState !== WebSocket.OPEN) return;
    this.#socket.send(JSON.stringify(message));
  }
}

function describeError(error: unknown): { code: string; message: string } {
  if (error instanceof Error) {
    const code = (error as Error & { code?: unknown }).code;
    return { code: typeof code === 'string' ? code : 'internal', message: error.message };
  }
  return { code: 'internal', message: String(error) };
}
