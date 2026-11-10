/**
 * 引擎这一侧：拨回节点、握手、按方法分派给 backend、把事件推回去。
 *
 * dsh 的 bridge 将来也走这条路：进程内开一个 socket 客户端连回节点，把 stdio 上的
 * JSON-RPC 翻译成这里的调用。fake engine 是它的第一个实现。
 */

import { connect, type Socket } from 'node:net';
import { createInterface } from 'node:readline';
import {
  ErrorCode,
  EVENT_METHOD,
  PROTOCOL_VERSION,
  PortError,
  encodeMessage,
  isNotification,
  isRequest,
  isResponse,
  parseMessage,
  toErrorBody,
  type Capabilities,
  type CloseParams,
  type CloseResult,
  type CreateSessionParams,
  type CreateSessionResult,
  type EngineEvent,
  type EngineEventType,
  type InterruptParams,
  type InterruptResult,
  type JsonRpcMessage,
  type RespondParams,
  type RespondResult,
  type SendParams,
  type SendResult,
  type SubscribeParams,
  type SubscribeResult,
} from './wire.ts';

export type Emit = (type: EngineEventType, payload: Record<string, unknown>, ts?: string) => void;

export interface EngineBackend {
  capabilities(): Capabilities;
  createSession(params: CreateSessionParams): Promise<CreateSessionResult>;
  send(params: SendParams): Promise<SendResult>;
  interrupt(params: InterruptParams): Promise<InterruptResult>;
  close(params: CloseParams): Promise<CloseResult>;
  respond(params: RespondParams): Promise<RespondResult>;
  subscribe(params: SubscribeParams): Promise<SubscribeResult>;
}

export interface ServeEngineOptions {
  port: number;
  token: string;
  engine: string;
  engineVersion?: string;
  /** 连接建立后才调用，拿到的 emit 已经绑定好 seq 与时间戳。 */
  createBackend(emit: Emit): EngineBackend;
  connectTimeoutMs?: number;
}

export interface ServedEngine {
  /** 连接结束时 settle，不会 reject。 */
  readonly closed: Promise<void>;
  close(): void;
}

interface Pending {
  resolve(value: unknown): void;
  reject(error: Error): void;
}

export async function serveEngine(options: ServeEngineOptions): Promise<ServedEngine> {
  const socket = await open(options.port, options.connectTimeoutMs ?? 10_000);
  const pending = new Map<number, Pending>();
  let nextId = 1;
  let seq = 0;
  let disposed = false;
  let settleClosed: () => void = () => {};
  const closed = new Promise<void>((resolve) => {
    settleClosed = resolve;
  });

  const send = (message: JsonRpcMessage): void => {
    if (!disposed && !socket.destroyed) socket.write(encodeMessage(message));
  };

  const emit: Emit = (type, payload, ts) => {
    const event: EngineEvent = {
      seq: seq++,
      type,
      ts: ts ?? new Date().toISOString(),
      payload,
    };
    send({ jsonrpc: '2.0', method: EVENT_METHOD, params: event });
  };

  const backend = options.createBackend(emit);
  const lines = createInterface({ input: socket });
  lines.on('error', () => {});

  socket.on('close', () => {
    disposed = true;
    for (const [, call] of pending) call.reject(new PortError(ErrorCode.Disconnected, 'host disconnected'));
    pending.clear();
    settleClosed();
  });
  socket.on('error', () => {});

  lines.on('line', (line) => {
    if (line.trim() === '') return;
    let message: JsonRpcMessage;
    try {
      message = parseMessage(line);
    } catch {
      return;
    }
    if (isResponse(message)) {
      const call = pending.get(message.id as number);
      if (call === undefined) return;
      pending.delete(message.id as number);
      if ('error' in message) call.reject(new PortError(message.error.code, message.error.message));
      else call.resolve(message.result);
      return;
    }
    if (isRequest(message)) {
      void dispatch(message.id, message.method, message.params);
      return;
    }
    // 剩下的只会是通知；节点目前不推，忽略。
  });

  const call = <T>(role: string, run: () => Promise<T>): Promise<T> =>
    run().then(
      (result) => result,
      (error: unknown) => {
        const body = toErrorBody(error);
        throw new PortError(body.code, `${role}: ${body.message}`);
      },
    );

  async function dispatch(id: string | number, method: string, params: unknown): Promise<void> {
    const fail = (error: unknown): void => {
      const body = toErrorBody(error);
      send({ jsonrpc: '2.0', id, error: body });
    };
    try {
      switch (method) {
        case 'capabilities':
          send({ jsonrpc: '2.0', id, result: await call(method, () => Promise.resolve(backend.capabilities())) });
          return;
        case 'createSession':
          send({
            jsonrpc: '2.0',
            id,
            result: await call(method, () => backend.createSession(params as CreateSessionParams)),
          });
          return;
        case 'send':
          send({ jsonrpc: '2.0', id, result: await call(method, () => backend.send(params as SendParams)) });
          return;
        case 'interrupt':
          send({ jsonrpc: '2.0', id, result: await call(method, () => backend.interrupt(params as InterruptParams)) });
          return;
        case 'close':
          send({ jsonrpc: '2.0', id, result: await call(method, () => backend.close(params as CloseParams)) });
          return;
        case 'respond':
          send({ jsonrpc: '2.0', id, result: await call(method, () => backend.respond(params as RespondParams)) });
          return;
        case 'subscribe':
          send({ jsonrpc: '2.0', id, result: await call(method, () => backend.subscribe(params as SubscribeParams)) });
          return;
        case 'handshake':
          fail(new PortError(ErrorCode.NotReady, 'already connected'));
          return;
        default:
          fail(new PortError(ErrorCode.MethodNotFound, `unknown method: ${method}`));
      }
    } catch (error) {
      fail(error);
    }
  }

  // 握手：连上之后第一件事，等节点确认协议和令牌。
  const handshakeId = nextId++;
  const handshake = new Promise<void>((resolve, reject) => {
    pending.set(handshakeId, { resolve: () => resolve(), reject });
  });
  send({
    jsonrpc: '2.0',
    id: handshakeId,
    method: 'handshake',
    params: {
      token: options.token,
      protocol: PROTOCOL_VERSION,
      engine: options.engine,
      ...(options.engineVersion === undefined ? {} : { engineVersion: options.engineVersion }),
    },
  });
  await handshake;

  return {
    closed,
    close(): void {
      if (disposed) return;
      disposed = true;
      socket.end();
      socket.destroy();
      settleClosed();
    },
  };
}

function open(port: number, timeoutMs: number): Promise<Socket> {
  return new Promise<Socket>((resolve, reject) => {
    const socket = connect({ host: '127.0.0.1', port });
    const timer = setTimeout(() => {
      socket.destroy();
      reject(new PortError(ErrorCode.Timeout, `connect to 127.0.0.1:${port} timed out`));
    }, timeoutMs);
    socket.once('connect', () => {
      clearTimeout(timer);
      socket.setNoDelay(true);
      resolve(socket);
    });
    socket.once('error', (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });
}
