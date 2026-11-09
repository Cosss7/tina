/**
 * 控制面转发下来的请求怎么落到 Service 上。
 *
 * 方法名用点分：`board` / `card.create` / `plan.request` / `plan.decide` /
 * `task.dispatch` / `task.review`。控制面的 REST 路由就是这几条的薄壳。
 */

import { ServiceError, type Service } from './service.ts';

export type RpcHandler = (method: string, params: unknown) => Promise<unknown>;

export function createRpcHandler(service: Service, actor = 'human:local'): RpcHandler {
  return async (method, params) => {
    const args = asRecord(params);
    try {
      switch (method) {
        case 'board':
          return service.board(requireString(args, 'projectId'));
        case 'card.create':
          return service.createDemand({
            projectId: requireString(args, 'projectId'),
            title: requireString(args, 'title'),
            description: optionalString(args, 'description') ?? '',
            actor,
          });
        case 'plan.request':
          return service.requestPlan({
            projectId: requireString(args, 'projectId'),
            taskId: requireString(args, 'taskId'),
            actor,
          });
        case 'plan.decide': {
          const decision = requireDecision(args);
          const result = service.decidePlan({
            approvalId: requireString(args, 'approvalId'),
            decision,
            actor,
          });
          return { approval: result.approval, tasks: result.tasks };
        }
        case 'task.dispatch':
          return service.dispatch({ taskId: requireString(args, 'taskId'), actor });
        case 'task.review': {
          const decision = requireDecision(args);
          return service.finalReview({
            taskId: requireString(args, 'taskId'),
            decision,
            actor,
            note: optionalString(args, 'note') ?? '',
          });
        }
        default:
          throw new ServiceError('method_not_found', `Unknown method: ${method}`);
      }
    } catch (error) {
      if (error instanceof ServiceError) throw error;
      throw new ServiceError('internal', error instanceof Error ? error.message : String(error));
    }
  };
}

function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null) return {};
  return value as Record<string, unknown>;
}

function requireString(args: Record<string, unknown>, key: string): string {
  const value = args[key];
  if (typeof value !== 'string' || value === '') throw new ServiceError('invalid_argument', `Missing parameter: ${key}`);
  return value;
}

function optionalString(args: Record<string, unknown>, key: string): string | undefined {
  const value = args[key];
  return typeof value === 'string' ? value : undefined;
}

function requireDecision(args: Record<string, unknown>): 'approved' | 'rejected' {
  const value = args.decision;
  if (value !== 'approved' && value !== 'rejected') {
    throw new ServiceError('invalid_argument', 'decision must be approved or rejected');
  }
  return value;
}
