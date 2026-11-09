#!/usr/bin/env node
/**
 * tina 命令。H0 只有 `up`：把控制面和节点一起拉起来，Ctrl+C 一起收掉
 *
 * 控制面是 Go 二进制，节点是 `packages/node-runtime` 的入口
 * 两边的日志都带上前缀
 */

import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const EXE = process.platform === 'win32' ? '.exe' : '';
const BIN = path.join(ROOT, '.build', `control-plane${EXE}`);
const NODE_MAIN = path.join(ROOT, 'packages', 'node-runtime', 'src', 'main.ts');

interface Options {
  addr: string;
  port: number;
  withNode: boolean;
}

function parseArgs(argv: string[]): Options {
  let addr = '127.0.0.1:7801';
  let withNode = true;
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--addr') {
      const value = argv[index + 1];
      if (value === undefined) throw new Error('--addr requires HOST:PORT');
      addr = value;
      index += 1;
    } else if (arg === '--no-node') {
      withNode = false;
    } else {
      throw new Error(`Unknown argument: ${arg}`);
    }
  }
  const port = Number(addr.split(':').pop());
  if (!Number.isInteger(port) || port <= 0) throw new Error(`Invalid port in address: ${addr}`);
  return { addr, port, withNode };
}

async function main(): Promise<void> {
  const command = process.argv[2];
  if (command !== 'up') {
    console.log('Usage: tina up [--addr 127.0.0.1:7801] [--no-node]');
    process.exit(command === undefined || command === '--help' ? 0 : 1);
  }

  const options = parseArgs(process.argv.slice(3));
  ensureControlPlane();

  const children: ChildProcess[] = [];
  let stopping = false;

  const stop = (code: number): void => {
    if (stopping) return;
    stopping = true;
    for (const child of children) child.kill();
    process.exit(code);
  };

  process.on('SIGINT', () => stop(0));
  process.on('SIGTERM', () => stop(0));

  const control = spawn(BIN, ['-addr', options.addr], { stdio: ['ignore', 'pipe', 'pipe'] });
  children.push(control);
  pipe(control, 'Control Plane');
  control.on('exit', (code) => {
    if (!stopping) {
      console.error(`[tina] Control plane exited (code ${String(code)})`);
      stop(code ?? 1);
    }
  });

  await waitForHealth(options.port, 15_000).catch((error: unknown) => {
    console.error(`[tina] Control plane failed to start: ${error instanceof Error ? error.message : String(error)}`);
    stop(1);
  });

  if (options.withNode) {
    const node = spawn(process.execPath, [NODE_MAIN], {
      env: {
        ...process.env,
        TINA_CONTROL_PLANE_URL: `ws://127.0.0.1:${options.port}/ws/node`,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    children.push(node);
    pipe(node, 'Agent Node');
    node.on('exit', (code) => {
      if (!stopping) {
        console.error(`[tina] Agent node exited (code ${String(code)})`);
        stop(code ?? 1);
      }
    });
  }

  console.log('');
  console.log(`  Tina started: http://${options.addr}`);
  console.log(`  Agent node: ${options.withNode ? 'started' : 'disabled (--no-node)'}`);
  console.log('  Press Ctrl+C to stop');
  console.log('');
}

function ensureControlPlane(): void {
  if (!existsSync(BIN)) {
    throw new Error(`Control plane binary not found: ${BIN}. Run make build first.`);
  }
}

function pipe(child: ChildProcess, prefix: string): void {
  child.stdout?.on('data', (chunk: Buffer) => process.stdout.write(prefixLine(prefix, chunk)));
  child.stderr?.on('data', (chunk: Buffer) => process.stderr.write(prefixLine(prefix, chunk)));
}

function prefixLine(prefix: string, chunk: Buffer): string {
  return chunk
    .toString('utf8')
    .split('\n')
    .map((line, index, lines) => (index === lines.length - 1 && line === '' ? '' : `[${prefix}] ${line}`))
    .join('\n');
}

async function waitForHealth(port: number, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/health`);
      if (response.ok) return;
    } catch {
      // 还没起来，继续等
    }
    if (Date.now() > deadline) throw new Error(`Timed out waiting for /health after ${timeoutMs}ms`);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

main().catch((error: unknown) => {
  console.error(`[tina] ${error instanceof Error ? error.message : String(error)}`);
  process.exit(1);
});
