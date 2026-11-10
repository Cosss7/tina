import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { EngineHost, serveEngine, type EngineEvent } from '@tina/engine-port';
import { createFakeBackend, type FakeEngineOptions } from '../src/fake.ts';
import { loadScenarioFile } from '../src/scenario.ts';

const SCENARIO_PATH = fileURLToPath(new URL('../scenarios/basic.yml', import.meta.url));
const scenario = loadScenarioFile(SCENARIO_PATH);

async function startFake(options: FakeEngineOptions = {}) {
  const host = await EngineHost.create({ token: 'fake-token', callTimeoutMs: 5000 });
  const events: EngineEvent[] = [];
  host.onEvent((event) => events.push(event));
  const served = await serveEngine({
    port: host.port,
    token: host.token,
    engine: scenario.engine,
    createBackend: createFakeBackend(scenario, options),
  });
  await host.whenReady();
  const close = async (): Promise<void> => {
    await host.close();
    served.close();
  };
  return { host, events, close };
}

test('capabilities 来自场景，fork 是 false', async () => {
  const { host, close } = await startFake();
  assert.deepEqual(await host.call('capabilities', undefined), {
    interrupt: true,
    respond: true,
    fork: false,
    resume: true,
  });
  await close();
});

test('createSession 的 id 从 1 开始、每次自增', async () => {
  const { host, close } = await startFake();
  assert.deepEqual(await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' }), {
    sessionId: 'fake-session-1',
  });
  assert.deepEqual(await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' }), {
    sessionId: 'fake-session-2',
  });
  await close();
});

test('按输入匹配规则，回放整段事件', async () => {
  const { host, events, close } = await startFake();
  await host.call('subscribe', {});
  const { sessionId } = await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' });
  const receipt = await host.call('send', { sessionId, input: '帮我拆卡' });
  assert.deepEqual(receipt, { receipt: 'fake-session-1/receipt-1' });
  await waitFor(() => events.length >= 6);
  assert.deepEqual(
    events.map((event) => event.type),
    ['turn.start', 'message.start', 'tool.call', 'tool.result', 'message.end', 'turn.end'],
  );
  assert.deepEqual(events[3]?.payload, { callId: 'call-1', isError: false, text: '已拆成 2 张' });
  await close();
});

test('没命中规则时回放 fallback', async () => {
  const { host, events, close } = await startFake();
  const { sessionId } = await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' });
  await host.call('send', { sessionId, input: '听不懂的话' });
  await waitFor(() => events.length >= 1);
  assert.equal(events[0]?.type, 'error');
  assert.equal(events[0]?.payload.code, 'no_rule');
  await close();
});

test('同一输入两次运行，事件序列完全一致', async () => {
  const run = async (): Promise<EngineEvent[]> => {
    const { host, events, close } = await startFake();
    const { sessionId } = await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' });
    await host.call('send', { sessionId, input: '帮我拆卡' });
    await waitFor(() => events.length >= 6);
    await close();
    return events;
  };
  assert.deepEqual(await run(), await run());
});

test('interrupt 之后不再有新事件，并补一条 cancelled 的 turn.end', async () => {
  const { host, events, close } = await startFake({ intervalMs: 40 });
  const { sessionId } = await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' });
  await host.call('send', { sessionId, input: '慢活' });
  await waitFor(() => events.length >= 2);
  assert.deepEqual(await host.call('interrupt', { sessionId }), { ok: true });
  const afterInterrupt = events.length;
  await new Promise((resolve) => setTimeout(resolve, 300));
  assert.equal(events.length, afterInterrupt, '打断之后不该再冒事件');
  const last = events.at(-1);
  assert.equal(last?.type, 'turn.end');
  assert.deepEqual(last?.payload, { turn: 1, reason: 'cancelled' });
  await close();
});

test('respond 与 close 都是 ok，关掉的会话再用会报错', async () => {
  const { host, close } = await startFake();
  const { sessionId } = await host.call('createSession', { profile: 'fake', cwd: '.', model: 'm' });
  assert.deepEqual(await host.call('respond', { sessionId, requestId: 'r1', answer: 'yes' }), { ok: true });
  assert.deepEqual(await host.call('close', { sessionId }), { ok: true });
  await assert.rejects(host.call('send', { sessionId, input: '再来' }), /closed/);
  await close();
});

async function waitFor(predicate: () => boolean, timeoutMs = 3000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error('waitFor timed out');
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}
