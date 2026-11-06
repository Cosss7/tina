/**
 * 把端口上的调用翻译成 dsh 的 SDK 协议。
 *
 * 按 D48：一个会话一个 dsh 进程。所以这个 backend 只管一个子进程，`close` 就是
 * 让它退出。按 D49：打断先 `shutdown`，2 秒不退再硬杀。
 *
 * 事件映射按 D50：dsh 的原生事件名留在这一层，向上只吐端口认的那七种。
 */

import { spawn, type ChildProcess } from 'node:child_process';
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
import { DshRpc, type DshEvent } from './rpc.ts';

export interface DshBackendOptions {
  /** dsh 的 lib/bin.js 绝对路径，由环境变量给。 */
  dshBin: string;
  profile: string;
  provider: string;
  model: string;
  cwd: string;
  /** 子进程额外环境变量。 */
  env?: Record<string, string>;
  shutdownGraceMs?: number;
  /** 单轮输出上限。定太小会把答案挤没：模型会用光 token 只写思考。 */
  maxTokens?: number;
  /** 推理强度。H0 要的是稳定的结构化输出，默认关掉思考。 */
  reasoningEffort?: string;
}

const CAPABILITIES: Capabilities = { interrupt: true, respond: false, fork: false, resume: true };

export class DshBackend implements EngineBackend {
  readonly #options: DshBackendOptions;
  readonly #emit: Emit;
  #child: ChildProcess | null = null;
  #rpc: DshRpc | null = null;
  #sessionId: string | null = null;
  #unsubscribe: (() => void) | null = null;
  #turn = 0;
  #stderrTail: string[] = [];

  constructor(options: DshBackendOptions, emit: Emit) {
    this.#options = options;
    this.#emit = emit;
  }

  capabilities(): Capabilities {
    return { ...CAPABILITIES };
  }

  async createSession(_params: CreateSessionParams): Promise<CreateSessionResult> {
    if (this.#child !== null) throw new Error('这个引擎已经有一个会话了');
    const child = spawn(process.execPath, [this.#options.dshBin, '--profile', this.#options.profile], {
      cwd: this.#options.cwd,
      env: { ...process.env, ...this.#options.env },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.#child = child;
    this.#rpc = new DshRpc(child, (line) => {
      this.#stderrTail.push(line);
      if (this.#stderrTail.length > 40) this.#stderrTail.shift();
    });
    this.#unsubscribe = this.#rpc.onNotification((notification) => this.#onEvent(notification.event));

    try {
      await this.#rpc.call('initialize', {
        cwd: this.#options.cwd,
        provider: this.#options.provider,
        model: this.#options.model,
        maxTokens: this.#options.maxTokens ?? 8192,
        ...(this.#options.reasoningEffort === undefined ? {} : { reasoningEffort: this.#options.reasoningEffort }),
      });
    } catch (error) {
      // dsh 的报错都在 stderr 上，带上最后一小段，省得只看到"退出了"。
      const tail = this.#stderrTail.slice(-12).join(String.fromCharCode(10));
      const reason = error instanceof Error ? error.message : String(error);
      const message = `dsh 初始化失败：${reason}` + String.fromCharCode(10) + tail;
      throw new Error(message);
    }
    const sessionId = `dsh-${process.pid}`;
    this.#sessionId = sessionId;
    return { sessionId };
  }

  async send(params: SendParams): Promise<SendResult> {
    const rpc = this.#require();
    this.#turn += 1;
    const result = await rpc.call<{ messageId: string }>('session/prompt', {
      sessionId: this.#sessionId,
      contentBlocks: [{ type: 'text', text: params.input }],
    });
    return { receipt: result.messageId };
  }

  async interrupt(_params: InterruptParams): Promise<InterruptResult> {
    await this.#stop();
    return { ok: true };
  }

  async close(_params: CloseParams): Promise<CloseResult> {
    await this.#stop();
    return { ok: true };
  }

  async respond(_params: RespondParams): Promise<RespondResult> {
    // 协议里没有 server→client 请求，这里只能是空实现（capabilities.respond = false）。
    return { ok: true };
  }

  async subscribe(_params: SubscribeParams): Promise<SubscribeResult> {
    return { ok: true };
  }

  async #stop(): Promise<void> {
    const child = this.#child;
    const rpc = this.#rpc;
    this.#unsubscribe?.();
    this.#unsubscribe = null;
    this.#child = null;
    this.#rpc = null;
    if (child === null || child.exitCode !== null) return;

    const exited = new Promise<void>((resolve) => child.once('exit', () => resolve()));
    try {
      await rpc?.call('shutdown', undefined, 2_000);
    } catch {
      // 超时就硬杀（D49）
    }
    const grace = new Promise<void>((resolve) => setTimeout(resolve, this.#options.shutdownGraceMs ?? 2_000));
    await Promise.race([exited, grace]);
    if (child.exitCode === null) child.kill('SIGKILL');
  }

  #require(): DshRpc {
    if (this.#rpc === null) throw new Error('会话还没建');
    return this.#rpc;
  }

  /** dsh 原生事件 → 端口事件。不认识的直接丢。 */
  #onEvent(event: DshEvent): void {
    const data = event.data;
    switch (event.type) {
      case 'turn/start':
        this.#emit('turn.start', { turn: data.turn ?? this.#turn });
        return;
      case 'user/message':
        this.#emit('message.start', { text: contentText(data.message) });
        return;
      case 'assistant/message':
        this.#emit('message.end', { text: contentText(data.message), usage: data.usage ?? {} });
        return;
      case 'tool/call':
        this.#emit('tool.call', { callId: data.callId, name: data.name, arguments: data.arguments });
        return;
      case 'tool/result': {
        const error = data.error as { code?: string } | undefined;
        this.#emit('tool.result', {
          callId: (data.message as { toolCallId?: string } | undefined)?.toolCallId,
          isError: (data.message as { isError?: boolean } | undefined)?.isError === true,
          text: contentText(data.message),
          ...(error?.code === undefined ? {} : { errorCode: error.code }),
        });
        return;
      }
      case 'turn/end': {
        const reason = data.reason as { kind?: string } | undefined;
        this.#emit('turn.end', { turn: data.turn ?? this.#turn, reason: mapReason(reason?.kind) });
        return;
      }
      default:
        return;
    }
  }
}

function mapReason(kind: string | undefined): string {
  switch (kind) {
    case 'completed':
      return 'completed';
    case 'interrupted':
      return 'interrupted';
    case 'aborted':
      return 'cancelled';
    default:
      return kind ?? 'completed';
  }
}

function contentText(message: unknown): string {
  if (typeof message !== 'object' || message === null) return '';
  const content = (message as { content?: unknown }).content;
  if (!Array.isArray(content)) return '';
  return content
    .map((block) => (typeof block === 'object' && block !== null && (block as { type?: string }).type === 'text' ? String((block as { text?: unknown }).text ?? '') : ''))
    .join('');
}
