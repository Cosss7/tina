#!/usr/bin/env node
/** Starts the Go control plane and the owner's locally authorized Go tinad. */
import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { prepareConfig, saveConfig, synchronizeProjects, type LocalConfig } from './local-node.ts';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const EXE = process.platform === 'win32' ? '.exe' : '';
const BIN = path.join(ROOT, '.build', `control-plane${EXE}`);
const TINAD = path.join(ROOT, '.build', `tinad${EXE}`);

interface Options {
  addr: string;
  base: string;
  dbPath: string;
  nodeConfig: string;
  workDir: string;
  withNode: boolean;
  engine: string | undefined;
}
function parseArgs(argv: string[]): Options {
  let addr = '127.0.0.1:7801';
  let dbPath = process.env.TINA_CONTROL_DB || path.join(ROOT, '.tina', 'control-plane.db');
  let nodeConfig = '';
  let workDir = process.env.TINA_WORK_DIR || ROOT;
  let withNode = true;
  let engine = process.env.TINA_ENGINE;
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--no-node') {
      withNode = false;
      continue;
    }
    const value = argv[index + 1];
    if (!value) throw new Error(`${String(arg)} requires a value`);
    switch (arg) {
      case '--addr':
        addr = value;
        break;
      case '--db':
        dbPath = value;
        break;
      case '--node-config':
        nodeConfig = value;
        break;
      case '--work-dir':
        workDir = value;
        break;
      case '--engine':
        engine = value;
        break;
      default:
        throw new Error(`Unknown argument: ${String(arg)}`);
    }
    index += 1;
  }
  if (engine && engine !== 'fake' && engine !== 'dsh') throw new Error('--engine must be fake or dsh');
  const url = new URL('http://' + addr);
  if (!url.port || Number(url.port) <= 0) throw new Error('--addr requires HOST:PORT');
  if (url.hostname === '0.0.0.0') url.hostname = '127.0.0.1';
  if (url.hostname === '[::]') url.hostname = '[::1]';
  dbPath = dbPath === ':memory:' ? dbPath : path.resolve(dbPath);
  if (!nodeConfig) {
    const directory = dbPath === ':memory:' ? path.join(ROOT, '.tina') : path.dirname(dbPath);
    const name =
      dbPath === ':memory:' || path.basename(dbPath) === 'control-plane.db'
        ? 'tinad.json'
        : path.basename(dbPath, path.extname(dbPath)) + '.tinad.json';
    nodeConfig = path.join(directory, name);
  }
  return {
    addr,
    base: url.origin,
    dbPath,
    nodeConfig: path.resolve(nodeConfig),
    workDir: path.resolve(workDir),
    withNode,
    engine,
  };
}

async function main(): Promise<void> {
  if (process.argv[2] !== 'up') {
    console.log(
      'Usage: tina up [--addr 127.0.0.1:7801] [--db .tina/control-plane.db] [--no-node] [--engine fake|dsh] [--node-config FILE] [--work-dir DIR]',
    );
    process.exit(process.argv[2] === undefined || process.argv[2] === '--help' ? 0 : 1);
  }
  const options = parseArgs(process.argv.slice(3));
  for (const binary of options.withNode ? [BIN, TINAD] : [BIN])
    if (!existsSync(binary)) throw new Error(`${binary} is missing. Run make build first.`);
  try {
    const existing = await fetch(options.base + '/health', { signal: AbortSignal.timeout(500) });
    if (existing.ok) throw new Error('Control plane is already running at ' + options.base);
  } catch (error) {
    if (error instanceof Error && error.message.startsWith('Control plane is already')) throw error;
  }
  const children: ChildProcess[] = [];
  let stopping = false;
  let synchronizing = false;
  let timer: ReturnType<typeof setInterval> | undefined;
  const stop = async (code: number): Promise<void> => {
    if (stopping) return;
    stopping = true;
    if (timer) clearInterval(timer);
    if (options.withNode && existsSync(options.nodeConfig)) {
      try {
        const config = JSON.parse(readFileSync(options.nodeConfig, 'utf8')) as LocalConfig;
        config.stopping = true;
        saveConfig(options.nodeConfig, config);
      } catch {
        /* child cleanup still follows */
      }
      const node = children.find((child) => child.spawnfile === TINAD);
      if (node) {
        const deadline = Date.now() + 8_000;
        while (node.exitCode === null && node.signalCode === null && Date.now() < deadline)
          await new Promise((resolve) => setTimeout(resolve, 50));
      }
    }
    await Promise.all(
      children.map(async (child) => {
        if (!child.pid || child.exitCode !== null || child.signalCode !== null) return;
        await new Promise<void>((resolve) => {
          const timeout = setTimeout(resolve, 2000);
          child.once('exit', () => {
            clearTimeout(timeout);
            resolve();
          });
          child.kill();
        });
      }),
    );
    process.exit(code);
  };
  process.on('SIGINT', () => void stop(0));
  process.on('SIGTERM', () => void stop(0));
  const start = (binary: string, args: string[], prefix: string) => {
    const child = spawn(binary, args, { cwd: ROOT, stdio: ['ignore', 'pipe', 'pipe'] });
    children.push(child);
    child.stdout?.on('data', (chunk: Buffer) => process.stdout.write(`[${prefix}] ${chunk.toString('utf8')}`));
    child.stderr?.on('data', (chunk: Buffer) => process.stderr.write(`[${prefix}] ${chunk.toString('utf8')}`));
    child.on('error', (error) => {
      console.error(`[tina] Failed to spawn ${binary}: ${error.message}`);
      void stop(1);
    });
    return child;
  };
  let config: LocalConfig | undefined;
  if (options.withNode) config = prepareConfig(options.nodeConfig, options.base, options.workDir, options.engine);
  const control = start(BIN, ['-addr', options.addr, '-db', options.dbPath], 'Control Plane');
  control.on('exit', (code) => {
    if (!stopping) {
      console.error('[tina] Control plane stopped');
      void stop(code ?? 1);
    }
  });
  try {
    const deadline = Date.now() + 15_000;
    for (;;) {
      if (stopping) return;
      try {
        if ((await fetch(options.base + '/health', { signal: AbortSignal.timeout(1000) })).ok) break;
      } catch {
        /* starting */
      }
      if (Date.now() > deadline) throw new Error('Control plane health timeout');
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    if (config) {
      const enrollment = await fetch(options.base + '/api/nodes/enroll', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ nodeId: config.nodeId, name: config.name, token: config.token }),
      });
      if (!enrollment.ok) throw new Error(`Node enrollment failed (${enrollment.status}): ${await enrollment.text()}`);
      await synchronizeProjects(options.nodeConfig, options.base, options.engine);
      const node = start(TINAD, ['-config', options.nodeConfig], 'tinad');
      node.on('exit', (code) => {
        if (!stopping) void stop(code ?? 1);
      });
      timer = setInterval(() => {
        if (synchronizing || stopping) return;
        synchronizing = true;
        void synchronizeProjects(options.nodeConfig, options.base, options.engine)
          .catch((error: unknown) => console.error('[tina] Local project authorization: ' + String(error)))
          .finally(() => {
            synchronizing = false;
          });
      }, 1500);
    }
    console.log(
      `\nTina: ${options.base}\nProject database: ${options.dbPath}\nLocal tinad: ${options.withNode ? options.nodeConfig : 'disabled'}\nPress Ctrl+C to stop.\n`,
    );
  } catch (error) {
    console.error('[tina] ' + String(error));
    await stop(1);
  }
}

main().catch((error: unknown) => {
  console.error('[tina] ' + String(error));
  process.exit(1);
});
