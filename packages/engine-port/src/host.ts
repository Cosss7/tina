/**
 * 节点这一侧：开监听、发令牌、等引擎拨进来。
 *
 * 一个 host 只接受一条连接（一个引擎进程 = 一个会话，见 D48）。第二个人拨进来
 * 直接掐掉；令牌不对也掐掉，并且让 whenReady() 失败。
 */

import { createServer, type Server, type Socket } from 'node:net';
import { createInterface } from 'node:readline';
import { randomBytes } from 'node:crypto';
import {
  ErrorCode,
  EVENT_METHOD,
  PortError,
  encodeMessage,
  isNotification,
  isRequest,
  isResponse,
  parseMessage,
  type EngineEvent,
  type HandshakeParams,
  type JsonRpcMessage,
  type MethodMap,
  type MethodName,
} from './wire.ts';

export interface EngineInfo {
  engine: string;
  protocol: number;
  engineVersion?: string;
}

export interface EngineHostOptions {
  /** 不给就随机生成。 */
  token?: string;
  /** 单次方法调用超时，默认 60 秒。 */
  callTimeoutMs?: number;
}

interface PendingCall {
  resolve(value: unknown): void;
  reject(error: Error): void;
  timer: ReturnType<typeof setTimeout>;
}

export class EngineHost {
  #server: Server;
  #token: string;
  #callTimeoutMs: number;
  #socket: Socket | null = null;
  #info: EngineInfo | null = null;
  #pending = new Map<number, PendingCall>();
  #nextId = 1;
  #eventHandlers: ((event: EngineEvent) => void)[] = [];
  #disconnectHandlers: ((reason: string) => void)[] = [];
  #settleReady: { resolve(info: EngineInfo): void; reject(error: Error): void } | null = null;
  #ready: Promise<EngineInfo>;
  #closed = false;
  #failed = false;

  private constructor(server: Server, token: string, callTimeoutMs: number) {
    this.#server = server;
    this.#token = token;
    this.#callTimeoutMs = callTimeoutMs;
    this.#ready = new Promise<EngineInfo>((resolve, reject) => {
      this.#settleReady = { resolve, reject };
    });
    // 没人消费也不会冒 unhandled rejection；调用方自己再 await 一次仍然拿得到原因。
    this.#ready.catch(() => {});
  }

  static async create(options: EngineHostOptions = {}): Promise<EngineHost> {
    const token = options.token ?? randomBytes(16).toString('hex');
    const server = createServer();
    const host = new EngineHost(server, token, options.callTimeoutMs ?? 60_000);
    await new Promise<void>((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', () => {
        server.off('error', reject);
        resolve();
      });
    });
    server.on('connection', (socket) => host.#accept(socket));
    return host;
  }

  get port(): number {
    const address = this.#server.address();
    if (address === null || typeof address === 'string') throw new Error('host is not listening');
    return address.port;
  }

  get token(): string {
    return this.#token;
  }

  get engine(): EngineInfo | null {
    return this.#info;
  }

  /** 握手完成或失败。失败的 Promise 也已经挂了 catch，重复 await 不会抛两次。 */
  whenReady(): Promise<EngineInfo> {
    return this.#ready;
  }

  onEvent(handler: (event: EngineEvent) => void): () => void {
    this.#eventHandlers.push(handler);
    return () => {
      this.#eventHandlers = this.#eventHandlers.filter((item) => item !== handler);
    };
  }

  onDisconnect(handler: (reason: string) => void): () => void {
    this.#disconnectHandlers.push(handler);
    return () => {
      this.#disconnectHandlers = this.#disconnectHandlers.filter((item) => item !== handler);
    };
  }

  async call<M extends MethodName>(
    method: M,
    params: MethodMap[M]['params'],
    options: { timeoutMs?: number } = {},
  ): Promise<MethodMap[M]['result']> {
    if (this.#closed) throw new PortError(ErrorCode.NotReady, 'host is closed');
    if (this.#socket === null) throw new PortError(ErrorCode.NotReady, 'no engine connected');
    const id = this.#nextId++;
    const timeoutMs = options.timeoutMs ?? this.#callTimeoutMs;
    const result = new Promise<unknown>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.#pending.delete(id);
        reject(new PortError(ErrorCode.Timeout, `${method} timed out after ${timeoutMs}ms`));
      }, timeoutMs);
      this.#pending.set(id, { resolve, reject, timer });
    });
    const request: JsonRpcMessage =
      params === undefined ? { jsonrpc: '2.0', id, method } : { jsonrpc: '2.0', id, method, params };
    this.#socket.write(encodeMessage(request));
    return (await result) as MethodMap[M]['result'];
  }

  async close(): Promise<void> {
    if (this.#closed) return;
    this.#closed = true;
    for (const [, call] of this.#pending) {
      clearTimeout(call.timer);
      call.reject(new PortError(ErrorCode.Disconnected, 'host closed'));
    }
    this.#pending.clear();
    this.#socket?.end();
    this.#socket = null;
    await new Promise<void>((resolve) => this.#server.close(() => resolve()));
  }

  #accept(socket: Socket): void {
    if (this.#socket !== null || this.#closed) {
      socket.destroy();
      return;
    }
    socket.setNoDelay(true);
    this.#socket = socket;
    const lines = createInterface({ input: socket });
    lines.on('error', () => {});
    lines.on('line', (line) => this.#onLine(line));
    socket.on('error', () => {});
    socket.on('close', () => {
      if (this.#socket !== socket) return;
      this.#socket = null;
      this.#fail(ErrorCode.Disconnected, 'engine disconnected');
    });
  }

  #onLine(line: string): void {
    if (line.trim() === '') return;
    let message: JsonRpcMessage;
    try {
      message = parseMessage(line);
    } catch {
      this.#socket?.destroy();
      return;
    }
    if (isResponse(message)) {
      const call = this.#pending.get(message.id as number);
      if (call === undefined) return;
      this.#pending.delete(message.id as number);
      clearTimeout(call.timer);
      if ('error' in message) {
        call.reject(new PortError(message.error.code, message.error.message, message.error.data));
      } else {
        call.resolve(message.result);
      }
      return;
    }
    if (isRequest(message) && message.method === 'handshake') {
      this.#handshake(message.id, message.params as HandshakeParams);
      return;
    }
    if (isNotification(message) && message.method === EVENT_METHOD) {
      const event = message.params as EngineEvent;
      for (const handler of this.#eventHandlers) handler(event);
      return;
    }
    if (isRequest(message)) {
      this.#socket?.write(
        encodeMessage({
          jsonrpc: '2.0',
          id: message.id,
          error: { code: ErrorCode.MethodNotFound, message: `unknown method: ${message.method}` },
        }),
      );
    }
  }

  #handshake(id: string | number, params: HandshakeParams): void {
    const reply = (error?: { code: number; message: string }): void => {
      this.#socket?.write(
        encodeMessage(
          error === undefined
            ? { jsonrpc: '2.0', id, result: { protocol: params.protocol } }
            : { jsonrpc: '2.0', id, error },
        ),
      );
    };
    if (params === null || typeof params !== 'object' || params.token !== this.#token) {
      reply({ code: ErrorCode.Unauthorized, message: 'handshake token mismatch' });
      this.#socket?.end();
      this.#fail(ErrorCode.Unauthorized, 'handshake token mismatch');
      return;
    }
    if (params.protocol !== 1) {
      reply({ code: ErrorCode.InvalidParams, message: `unsupported protocol ${String(params.protocol)}` });
      this.#socket?.end();
      this.#fail(ErrorCode.InvalidParams, 'protocol mismatch');
      return;
    }
    const info: EngineInfo = { engine: params.engine, protocol: params.protocol };
    if (params.engineVersion !== undefined) info.engineVersion = params.engineVersion;
    this.#info = info;
    reply();
    this.#settleReady?.resolve(info);
    this.#settleReady = null;
  }

  #fail(code: number, reason: string): void {
    if (this.#failed) return;
    this.#failed = true;
    this.#settleReady?.reject(new PortError(code, reason));
    this.#settleReady = null;
    for (const [, call] of this.#pending) {
      clearTimeout(call.timer);
      call.reject(new PortError(ErrorCode.Disconnected, 'engine disconnected'));
    }
    this.#pending.clear();
    for (const handler of this.#disconnectHandlers) handler(reason);
  }
}
