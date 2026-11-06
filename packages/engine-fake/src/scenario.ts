/**
 * fake engine 的场景文件：一段输入该回放哪些事件。
 *
 * 场景是纯数据，同一份场景 + 同一段输入必须产生同一串事件——这是 `make verify`
 * 能拿它写断言的前提。
 */

import { readFileSync } from 'node:fs';
import { parse } from 'yaml';
import type { Capabilities, EngineEventType } from '@tina/engine-port';

const EVENT_TYPES = new Set<EngineEventType>([
  'turn.start',
  'turn.end',
  'message.start',
  'message.end',
  'tool.call',
  'tool.result',
  'error',
]);

export interface EventSpec {
  type: EngineEventType;
  payload?: Record<string, unknown>;
  /** 这条事件发出之前等多久，用来给 interrupt 留窗口。 */
  delayMs?: number;
}

export interface Rule {
  /** 输入里包含这段文字就算命中；`"*"` 命中一切。 */
  match: string;
  events: EventSpec[];
}

export interface Scenario {
  engine: string;
  engineVersion?: string;
  capabilities: Capabilities;
  /** 事件时间戳的基准，默认 2026-01-01T00:00:00.000Z。 */
  baseTime: string;
  rules: Rule[];
  /** 没有任何规则命中时回放的事件，默认一条 error。 */
  fallback: EventSpec[];
}

const DEFAULT_BASE_TIME = '2026-01-01T00:00:00.000Z';

const DEFAULT_CAPABILITIES: Capabilities = { interrupt: true, respond: true, fork: false, resume: true };

export function loadScenarioFile(path: string): Scenario {
  return parseScenario(parse(readFileSync(path, 'utf8')) as unknown, path);
}

export function parseScenario(raw: unknown, source = '<inline>'): Scenario {
  if (typeof raw !== 'object' || raw === null) throw new Error(`${source}: scenario must be a mapping`);
  const input = raw as Record<string, unknown>;
  const rules = input.rules;
  if (!Array.isArray(rules) || rules.length === 0) throw new Error(`${source}: rules must be a non-empty list`);

  const parsedRules = rules.map((rule, index) => parseRule(rule, `${source}: rules[${index}]`));
  const scenario: Scenario = {
    engine: typeof input.engine === 'string' ? input.engine : 'fake-engine',
    capabilities: parseCapabilities(input.capabilities, `${source}: capabilities`),
    baseTime: typeof input.baseTime === 'string' ? input.baseTime : DEFAULT_BASE_TIME,
    rules: parsedRules,
    fallback: input.fallback === undefined ? [{ type: 'error', payload: { code: 'no_rule', message: 'no scenario rule matched' } }] : parseEvents(input.fallback, `${source}: fallback`),
  };
  if (Number.isNaN(Date.parse(scenario.baseTime))) throw new Error(`${source}: baseTime is not a valid ISO time`);
  if (typeof input.engineVersion === 'string') scenario.engineVersion = input.engineVersion;
  return scenario;
}

function parseCapabilities(raw: unknown, source: string): Capabilities {
  if (raw === undefined) return { ...DEFAULT_CAPABILITIES };
  if (typeof raw !== 'object' || raw === null) throw new Error(`${source} must be a mapping`);
  const input = raw as Record<string, unknown>;
  return {
    interrupt: input.interrupt !== false,
    respond: input.respond !== false,
    fork: input.fork === true,
    resume: input.resume !== false,
  };
}

function parseRule(raw: unknown, source: string): Rule {
  if (typeof raw !== 'object' || raw === null) throw new Error(`${source} must be a mapping`);
  const input = raw as Record<string, unknown>;
  if (typeof input.match !== 'string' || input.match === '') throw new Error(`${source}.match must be a non-empty string`);
  return { match: input.match, events: parseEvents(input.events, `${source}.events`) };
}

function parseEvents(raw: unknown, source: string): EventSpec[] {
  if (!Array.isArray(raw)) throw new Error(`${source} must be a list`);
  return raw.map((event, index) => {
    const at = `${source}[${index}]`;
    if (typeof event !== 'object' || event === null) throw new Error(`${at} must be a mapping`);
    const input = event as Record<string, unknown>;
    if (typeof input.type !== 'string' || !EVENT_TYPES.has(input.type as EngineEventType)) {
      throw new Error(`${at}.type must be one of ${[...EVENT_TYPES].join(', ')}`);
    }
    const spec: EventSpec = { type: input.type as EngineEventType };
    if (input.payload !== undefined) {
      if (typeof input.payload !== 'object' || input.payload === null) throw new Error(`${at}.payload must be a mapping`);
      spec.payload = input.payload as Record<string, unknown>;
    }
    if (input.delayMs !== undefined) {
      if (typeof input.delayMs !== 'number' || input.delayMs < 0) throw new Error(`${at}.delayMs must be a non-negative number`);
      spec.delayMs = input.delayMs;
    }
    return spec;
  });
}

export function pickRule(scenario: Scenario, input: string): EventSpec[] {
  for (const rule of scenario.rules) {
    if (rule.match === '*' || input.includes(rule.match)) return rule.events;
  }
  return scenario.fallback;
}
