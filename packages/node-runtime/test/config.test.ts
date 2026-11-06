import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DEFAULT_PROJECTS, loadConfig } from '../src/config.ts';

test('默认配置指向本机控制面，托管两个项目', () => {
  const config = loadConfig({});
  assert.equal(config.controlPlaneUrl, 'ws://127.0.0.1:7801/ws/node');
  assert.equal(config.nodeId, 'node-local');
  assert.equal(config.heartbeatMs, 1000);
  assert.deepEqual(config.projects, DEFAULT_PROJECTS);
});

test('默认项目是副本，改一个不影响另一个', () => {
  const first = loadConfig({});
  first.projects[0]!.name = '改过';
  assert.equal(loadConfig({}).projects[0]!.name, DEFAULT_PROJECTS[0]!.name);
});

test('TINA_PROJECTS 覆盖项目列表', () => {
  const config = loadConfig({ TINA_PROJECTS: '[{"id":"a","name":"甲"}]' });
  assert.deepEqual(config.projects, [{ id: 'a', name: '甲' }]);
});

test('坏的项目配置会被拒绝', () => {
  assert.throws(() => loadConfig({ TINA_PROJECTS: '[]' }), /非空数组/);
  assert.throws(() => loadConfig({ TINA_PROJECTS: '[{"id":"a"}]' }), /name 缺失/);
});

test('心跳间隔必须是正整数', () => {
  assert.equal(loadConfig({ TINA_HEARTBEAT_MS: '250' }).heartbeatMs, 250);
  assert.throws(() => loadConfig({ TINA_HEARTBEAT_MS: '0' }), /正整数/);
  assert.throws(() => loadConfig({ TINA_HEARTBEAT_MS: 'abc' }), /正整数/);
});
