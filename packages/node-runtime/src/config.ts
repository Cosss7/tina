/**
 * 节点运行时的启动配置。
 *
 * H0 是单机单用户，配置全部从环境变量来，不读文件也不连远端。
 */

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
}

export const VERSION = '0.1.0-dev';

/** 两个演示项目：H0 的验收里"Web 上看到两个项目的看板"。 */
export const DEFAULT_PROJECTS: ProjectRef[] = [
  { id: 'proj-website', name: '网站改版' },
  { id: 'proj-mobile', name: '移动端' },
];

export function loadConfig(env: Record<string, string | undefined> = process.env): NodeConfig {
  const url = env.TINA_CONTROL_PLANE_URL ?? 'ws://127.0.0.1:7801/ws/node';
  return {
    nodeId: env.TINA_NODE_ID ?? 'node-local',
    name: env.TINA_NODE_NAME ?? '本机',
    version: VERSION,
    controlPlaneUrl: url,
    projects: parseProjects(env.TINA_PROJECTS),
    heartbeatMs: parsePositiveInt(env.TINA_HEARTBEAT_MS, 1000),
  };
}

function parseProjects(raw: string | undefined): ProjectRef[] {
  if (raw === undefined || raw.trim() === '') return DEFAULT_PROJECTS.map((project) => ({ ...project }));
  const parsed: unknown = JSON.parse(raw);
  if (!Array.isArray(parsed) || parsed.length === 0) throw new Error('TINA_PROJECTS 必须是非空数组');
  return parsed.map((item, index) => {
    if (typeof item !== 'object' || item === null) throw new Error(`TINA_PROJECTS[${index}] 不是对象`);
    const project = item as Record<string, unknown>;
    if (typeof project.id !== 'string' || project.id === '') throw new Error(`TINA_PROJECTS[${index}].id 缺失`);
    if (typeof project.name !== 'string' || project.name === '') throw new Error(`TINA_PROJECTS[${index}].name 缺失`);
    return { id: project.id, name: project.name };
  });
}

function parsePositiveInt(raw: string | undefined, fallback: number): number {
  if (raw === undefined || raw.trim() === '') return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value <= 0) throw new Error(`需要正整数，收到 ${raw}`);
  return value;
}
