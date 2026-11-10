import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  EngineHost,
  ErrorCode,
  PortError,
  serveEngine,
  type Capabilities,
  type Emit,
  type EngineBackend,
  type EngineEvent,
} from '../src/index.ts';

const CAPABILITIES: Capabilities = { interrupt: true, respond: true, fork: false, resume: true };

function stubBackend(emit: Emit): EngineBackend {
  return {
    capabilities: () => CAPABILITIES,
    createSession: async (params) => ({ sessionId: `session-for-${params.model}` }),
    send: async (params) => {
      emit('turn.start', { turn: 1 });
      emit('message.start', { text: params.input });
      emit('message.end', { text: `echo: ${params.input}` });
      emit('turn.end', { turn: 1, reason: 'completed' });
      return { receipt: `${params.sessionId}/r1` };
    },
    interrupt: async () => ({ ok: true }),
    close: async () => ({ ok: true }),
    respond: async () => ({ ok: true }),
    subscribe: async () => ({ ok: true }),
  };
}

async function start(createBackend: (emit: Emit) => EngineBackend = stubBackend) {
  const host = await EngineHost.create({ token: 'test-token', callTimeoutMs: 5000 });
  const events: EngineEvent[] = [];
  host.onEvent((event) => events.push(event));
  const served = await serveEngine({ port: host.port, token: host.token, engine: 'stub', createBackend });
  await host.whenReady();
  return { host, served, events };
}

test('握手成功后才算就绪，引擎名能读到', async () => {
  const { host, served } = await start();
  assert.equal(host.engine?.engine, 'stub');
  assert.equal(host.engine?.protocol, 1);
  await host.close();
  served.close();
});

test('令牌不对：引擎被拒，host 就绪失败', async () => {
  const host = await EngineHost.create({ token: 'right' });
  await assert.rejects(
    serveEngine({ port: host.port, token: 'wrong', engine: 'stub', createBackend: stubBackend }),
    (error: unknown) => error instanceof PortError && error.code === ErrorCode.Unauthorized,
  );
  await assert.rejects(
    host.whenReady(),
    (error: unknown) => error instanceof PortError && error.code === ErrorCode.Unauthorized,
  );
  await host.close();
});

test('capabilities / createSession / send 走通，回执是回执不是结果', async () => {
  const { host, served } = await start();
  assert.deepEqual(await host.call('capabilities', undefined), CAPABILITIES);
  const { sessionId } = await host.call('createSession', { profile: 'p', cwd: '.', model: 'm' });
  assert.equal(sessionId, 'session-for-m');
  const receipt = await host.call('send', { sessionId, input: 'hello' });
  assert.deepEqual(receipt, { receipt: 'session-for-m/r1' });
  await host.close();
  served.close();
});

test('事件按发出顺序到达，seq 单调递增', async () => {
  const { host, served, events } = await start();
  const { sessionId } = await host.call('createSession', { profile: 'p', cwd: '.', model: 'm' });
  await host.call('send', { sessionId, input: 'hello' });
  await waitFor(() => events.length >= 4);
  assert.deepEqual(
    events.map((event) => event.type),
    ['turn.start', 'message.start', 'message.end', 'turn.end'],
  );
  assert.deepEqual(
    events.map((event) => event.seq),
    [0, 1, 2, 3],
  );
  assert.deepEqual(events[2]?.payload, { text: 'echo: hello' });
  await host.close();
  served.close();
});

test('未到握手就发方法是 NotReady', async () => {
  const host = await EngineHost.create({ token: 'test-token' });
  await assert.rejects(
    host.call('capabilities', undefined),
    (error: unknown) => error instanceof PortError && error.code === ErrorCode.NotReady,
  );
  await host.close();
});

test('未知方法返回 MethodNotFound', async () => {
  const { host, served } = await start();
  await assert.rejects(
    host.call('fork' as never, { sessionId: 'x', at: 0 } as never),
    (error: unknown) => error instanceof PortError && error.code === ErrorCode.MethodNotFound,
  );
  await host.close();
  served.close();
});

test('引擎断开：待完成的调用失败，断开回调触发', async () => {
  let disconnected = 0;
  const host = await EngineHost.create({ token: 'test-token', callTimeoutMs: 5000 });
  host.onDisconnect(() => {
    disconnected += 1;
  });
  const served = await serveEngine({
    port: host.port,
    token: host.token,
    engine: 'stub',
    createBackend: (emit) => ({ ...stubBackend(emit), send: () => new Promise(() => {}) }),
  });
  await host.whenReady();
  const pending = host.call('send', { sessionId: 's', input: 'hang' });
  await waitFor(() => true);
  served.close();
  await assert.rejects(
    pending,
    (error: unknown) => error instanceof PortError && error.code === ErrorCode.Disconnected,
  );
  assert.equal(disconnected, 1);
  await host.close();
});

async function waitFor(predicate: () => boolean, timeoutMs = 3000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error('waitFor timed out');
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}
