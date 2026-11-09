import { randomBytes, randomUUID } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync, renameSync, rmSync } from 'node:fs';
import path from 'node:path';
import { hostname } from 'node:os';

export interface Agent { id: string; projectId: string; name: string; role: string; engineId: string; nodeId: string; cwd: string; model?: string; options?: Record<string, unknown> }
export interface LocalConfig {
  nodeId: string; name: string; url: string; token: string; database: string;
  maxConcurrent: number; roots: string[]; agents: Array<{ id: string; projectId: string; role: string; engineId: string }>;
  autoProjects?: boolean; stopping?: boolean;
  dsh?: { command: string[]; profile: string; provider: string; model: string; home?: string };
}
export function saveConfig(filename: string, config: LocalConfig): void {
  mkdirSync(path.dirname(filename), { recursive: true, mode: 0o700 });
  const temporary = filename + '.' + randomUUID() + '.tmp';
  try { writeFileSync(temporary, JSON.stringify(config, null, 2) + '\n', { mode: 0o600 }); renameSync(temporary, filename); }
  finally { rmSync(temporary, { force: true }); }
}
export function prepareConfig(filename: string, base: string, workDir: string, engine?: string): LocalConfig {
  const config: LocalConfig = existsSync(filename) ? JSON.parse(readFileSync(filename, 'utf8')) as LocalConfig : {
    nodeId: 'local-' + randomUUID(), name: hostname(), url: '', token: randomBytes(32).toString('hex'),
    database: path.join(path.dirname(filename), path.basename(filename, '.json') + '.db'), maxConcurrent: 4, roots: [workDir], agents: [], autoProjects: true,
  };
  config.url = base.replace(/^http/, 'ws') + '/ws/tinad';
  config.stopping = false;
  const dshBin = process.env.TINA_DSH_BIN;
  if (dshBin) config.dsh = { command: [process.execPath, path.resolve(dshBin)], profile: process.env.TINA_DSH_PROFILE || 'sdk-minimal', provider: process.env.TINA_DSH_PROVIDER || 'deepseek-official', model: process.env.TINA_DSH_MODEL || 'deepseek-flash', ...(process.env.TINA_DSH_HOME ? { home: process.env.TINA_DSH_HOME } : {}) };
  if (engine === 'dsh' && !config.dsh) throw new Error('dsh requires TINA_DSH_BIN or dsh.command in the private node configuration');
  saveConfig(filename, config); return config;
}
export async function synchronizeProjects(filename: string, base: string, engine?: string): Promise<void> {
  const config = JSON.parse(readFileSync(filename, 'utf8')) as LocalConfig;
  if (!config.autoProjects || config.stopping) return;
  const request = async <T>(endpoint: string, init?: RequestInit): Promise<T> => {
    const response = await fetch(base + endpoint, { ...init, signal: AbortSignal.timeout(3000), headers: { 'content-type': 'application/json' } });
    if (!response.ok) throw new Error(`Local enrollment failed (${response.status}): ${await response.text()}`);
    return await response.json() as T;
  };
  const state = await request<{ projects: Array<{ id: string }> }>('/api/state');
  const grants: LocalConfig['agents'] = [];
  for (const project of state.projects) {
    const agents = await request<Agent[]>(`/api/projects/${encodeURIComponent(project.id)}/agents`);
    for (let agent of agents) {
      if (agent.nodeId && agent.nodeId !== config.nodeId) continue;
      const selectedEngine = engine || agent.engineId;
      if (selectedEngine !== 'fake' && (selectedEngine !== 'dsh' || !config.dsh)) continue;
      if (!agent.nodeId || agent.engineId !== selectedEngine) {
        agent = await request<Agent>(`/api/projects/${encodeURIComponent(project.id)}/agents/${encodeURIComponent(agent.id)}`, {
          method: 'PUT', body: JSON.stringify({ ...agent, nodeId: config.nodeId, cwd: agent.nodeId ? agent.cwd : config.roots[0], engineId: selectedEngine }),
        });
      }
      grants.push({ id: agent.id, projectId: project.id, role: agent.role, engineId: agent.engineId });
    }
  }
  if (JSON.stringify(config.agents) !== JSON.stringify(grants)) {
    // A shutdown or a manual configuration edit may have arrived during HTTP I/O.
    const latest = JSON.parse(readFileSync(filename, 'utf8')) as LocalConfig;
    if (latest.stopping || !latest.autoProjects || latest.nodeId !== config.nodeId) return;
    latest.agents = grants; saveConfig(filename, latest);
  }
}
