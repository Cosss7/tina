/**
 * 一次"会话内的一轮"：拉起一个引擎子进程，发 prompt，收事件，等 turn.end，收工。
 *
 * 按 D48，一个会话就是一个引擎进程。所以这里的每次调用都是
 * 起进程 → 握手 → 建会话 → 订阅 → 发 → 等 → 关，做完就拆干净。
 */

import { spawn, type ChildProcess } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { EngineHost, type EngineEvent } from '@tina/engine-port';

export interface TurnOptions {
  /** 引擎可执行入口（fake engine 的 bin.ts）。 */
  engineBin: string;
  /** 场景文件；真 dsh 之后换成 profile 名。 */
  scenario: string;
  /** 子进程工作目录。 */
  cwd: string;
  prompt: string;
  /** 整轮的上限，默认 60 秒。 */
  timeoutMs?: number;
  /** 额外环境变量，测试里用得上。 */
  env?: Record<string, string>;
}

export interface TurnResult {
  events: EngineEvent[];
  /** 最后一条 message.end 的文本，模型交付的东西在这里。 */
  text: string;
  usage: Record<string, unknown>;
  engine: string;
}

export async function runTurn(options: TurnOptions): Promise<TurnResult> {
  const timeoutMs = options.timeoutMs ?? 60_000;
  const host = await EngineHost.create({ token: randomUUID(), callTimeoutMs: timeoutMs });
  const events: EngineEvent[] = [];
  host.onEvent((event) => {
    events.push(event);
  });

  let child: ChildProcess | null = null;
  try {
    child = spawn(process.execPath, [options.engineBin], {
      cwd: options.cwd,
      env: {
        ...process.env,
        ...options.env,
        TINA_ENGINE_PORT: String(host.port),
        TINA_ENGINE_TOKEN: host.token,
        TINA_ENGINE_SCENARIO: options.scenario,
      },
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    const stderr: string[] = [];
    child.stderr?.on('data', (chunk: Buffer) => stderr.push(chunk.toString('utf8')));
    child.on('error', (error) => stderr.push(String(error)));

    const info = await host.whenReady();
    const { sessionId } = await host.call('createSession', {
      profile: 'fake',
      cwd: options.cwd,
      model: 'fake',
    });
    await host.call('subscribe', {});
    await host.call('send', { sessionId, input: options.prompt });

    const finished = await waitUntil(() => events.some((event) => event.type === 'turn.end'), timeoutMs);
    if (!finished) throw new Error(`一轮没在 ${timeoutMs}ms 内结束\n${stderr.join('')}`);

    await host.call('close', { sessionId }).catch(() => {});
    return { events, text: lastText(events), usage: lastUsage(events), engine: info.engine };
  } finally {
    await host.close().catch(() => {});
    if (child !== null && child.exitCode === null) child.kill();
  }
}

function lastText(events: EngineEvent[]): string {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const event = events[index];
    if (event?.type !== 'message.end') continue;
    const text = event.payload.text;
    if (typeof text === 'string') return text;
  }
  return '';
}

function lastUsage(events: EngineEvent[]): Record<string, unknown> {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    const event = events[index];
    if (event?.type !== 'message.end') continue;
    const usage = event.payload.usage;
    if (typeof usage === 'object' && usage !== null) return usage as Record<string, unknown>;
  }
  return {};
}

async function waitUntil(predicate: () => boolean, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (predicate()) return true;
    if (Date.now() > deadline) return false;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}
