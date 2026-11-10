/**
 * 节点运行时的启动配置。
 *
 * H0 是单机单用户，配置全部从环境变量来，不读配置文件也不连远端。
 */

import { fileURLToPath } from 'node:url';

export interface ProjectRef {
  id: string;
  name: string;
}

export interface NodeConfig {
  nodeId: string;
  name: string;
  version: string;
  /** 控制面的节点入口，形如 ws://127.0.0.1:7801/ws/node */
  controlPlaneUrl: string;
  projects: ProjectRef[];
  /** 心跳间隔。控制面的掉线阈值是 3 秒，这里取 1 秒。 */
  heartbeatMs: number;
  /** 节点库路径，默认在内存里；demo 用文件，方便看。 */
  dbPath: string;
  /** fake engine 的可执行入口。 */
  engineBin: string;
  /** fake engine 的场景文件。 */
  scenario: string;
  /** 跑会话子进程时的工作目录。 */
  workDir: string;
}

export const VERSION = '0.1.0';

/** 两个演示项目：H0 的验收里"Web 上看到两个项目的看板"。 */
export const DEFAULT_PROJECTS: ProjectRef[] = [
  { id: 'proj-website', name: '网站改版' },
  { id: 'proj-mobile', name: '移动端' },
];

/** 这些默认路径都从本文件推出来，不写死机器上的目录。 */
const PACKAGE_ROOT = new URL('../../../', import.meta.url);
const pathFrom = (relative: string): string => fileURLToPath(new URL(relative, PACKAGE_ROOT));

export function loadConfig(env: Record<string, string | undefined> = process.env): NodeConfig {
  return {
    nodeId: env.TINA_NODE_ID ?? 'node-local',
    name: env.TINA_NODE_NAME ?? '本机',
    version: VERSION,
    controlPlaneUrl: env.TINA_CONTROL_PLANE_URL ?? 'ws://127.0.0.1:7801/ws/node',
    projects: parseProjects(env.TINA_PROJECTS),
    heartbeatMs: parsePositiveInt(env.TINA_HEARTBEAT_MS, 1000),
    dbPath: env.TINA_DB ?? ':memory:',
    engineBin: env.TINA_ENGINE_BIN ?? pathFrom('packages/engine-fake/src/bin.ts'),
    scenario: env.TINA_ENGINE_SCENARIO ?? pathFrom('packages/engine-fake/scenarios/demo.yml'),
    workDir: env.TINA_WORK_DIR ?? process.cwd(),
  };
}

function parseProjects(raw: string | undefined): ProjectRef[] {
  if (raw === undefined || raw.trim() === '') return DEFAULT_PROJECTS.map((project) => ({ ...project }));
  const parsed: unknown = JSON.parse(raw);
  if (!Array.isArray(parsed) || parsed.length === 0) throw new Error('TINA_PROJECTS must be a non-empty array');
  return parsed.map((item, index) => {
    if (typeof item !== 'object' || item === null) throw new Error(`TINA_PROJECTS[${index}] must be an object`);
    const project = item as Record<string, unknown>;
    if (typeof project.id !== 'string' || project.id === '') throw new Error(`TINA_PROJECTS[${index}].id is required`);
    if (typeof project.name !== 'string' || project.name === '')
      throw new Error(`TINA_PROJECTS[${index}].name is required`);
    return { id: project.id, name: project.name };
  });
}

function parsePositiveInt(raw: string | undefined, fallback: number): number {
  if (raw === undefined || raw.trim() === '') return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value <= 0) throw new Error(`Expected a positive integer, got ${raw}`);
  return value;
}
