import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { EngineHost, type EngineEvent } from '@tina/engine-port';

const BIN = fileURLToPath(new URL('../src/bin.ts', import.meta.url));
const SCENARIO_PATH = fileURLToPath(new URL('../scenarios/basic.yml', import.meta.url));

test('假引擎作为子进程拨回节点，跑完一轮后随节点关闭退出', async () => {
  const host = await EngineHost.create({ token: 'bin-token', callTimeoutMs: 5000 });
  const events: EngineEvent[] = [];
  host.onEvent((event) => events.push(event));

  const stderr: string[] = [];
  const child = spawn(process.execPath, [BIN], {
    env: {
      ...process.env,
      TINA_ENGINE_PORT: String(host.port),
      TINA_ENGINE_TOKEN: host.token,
      TINA_ENGINE_SCENARIO: SCENARIO_PATH,
    },
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  child.stderr.on('data', (chunk: Buffer) => stderr.push(chunk.toString('utf8')));
  const exited = new Promise<number | null>((resolve) => child.on('exit', (code) => resolve(code)));

  try {
    const info = await host.whenReady();
    assert.equal(info.engine, 'fake-engine');
    assert.equal(info.protocol, 1);

    const { sessionId } = await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' });
    await host.call('subscribe', {});
    await host.call('send', { sessionId, input: '帮我拆卡' });

    const deadline = Date.now() + 3000;
    while (events.length < 6) {
      if (Date.now() > deadline) throw new Error(`只有 ${events.length} 条事件\n${stderr.join('')}`);
      await new Promise((resolve) => setTimeout(resolve, 5));
    }
    assert.equal(events.at(-1)?.type, 'turn.end');
  } finally {
    await host.close();
  }

  assert.equal(await exited, 0, `子进程应该干净退出\n${stderr.join('')}`);
});
