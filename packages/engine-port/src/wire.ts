/**
 * 引擎端口的线格式。
 *
 * 一条 TCP 连接，一行一个 JSON-RPC 2.0 消息。节点是监听方（host），引擎是拨入方
 * （engine）：节点先开监听，再带着端口和一次性令牌把引擎拉起来，引擎拨回来握手。
 *
 * 为什么是引擎拨入：dsh 只肯说 stdio，它的 bridge 是进程内的插件，能开 socket，
 * 但没地方把"我监听了哪个端口"告诉父进程。反过来由节点监听，端口从环境变量进，
 * 子进程一句话不用多说。
 */

export const PROTOCOL_VERSION = 1;

/** 端口事件只有这七种，属于产品侧词汇；引擎原生类型不越过这条线。 */
export type EngineEventType =
  | 'turn.start'
  | 'turn.end'
  | 'message.start'
  | 'message.end'
  | 'tool.call'
  | 'tool.result'
  | 'error';

export interface EngineEvent {
  /** 单调递增，从 0 开始，跨会话共享。 */
  seq: number;
  type: EngineEventType;
  /** ISO 8601，带毫秒。 */
  ts: string;
  payload: Record<string, unknown>;
}

export interface Capabilities {
  interrupt: boolean;
  respond: boolean;
  fork: boolean;
  resume: boolean;
}

export interface HandshakeParams {
  token: string;
  protocol: number;
  /** 引擎自报名字，进日志用，例如 "fake-engine"、"dsh"。 */
  engine: string;
  engineVersion?: string;
}

export interface HandshakeResult {
  protocol: number;
}

export interface CreateSessionParams {
  profile: string;
  cwd: string;
  model: string;
  systemPrompt?: string;
}

export interface CreateSessionResult {
  sessionId: string;
}

export interface SendParams {
  sessionId: string;
  input: string;
}

export interface SendResult {
  /** 入队回执，不是结果。结果从事件流里读。 */
  receipt: string;
}

export interface InterruptParams {
  sessionId: string;
}

export interface InterruptResult {
  ok: true;
}

export interface CloseParams {
  sessionId: string;
}

export interface CloseResult {
  ok: true;
}

export interface RespondParams {
  sessionId: string;
  requestId: string;
  answer: string;
}

export interface RespondResult {
  ok: true;
}

export interface SubscribeParams {
  /** 上次收到的 seq。引擎可以不认，H0 的实现都不重放历史。 */
  cursor?: number;
}

export interface SubscribeResult {
  ok: true;
}

/** 请求与结果的名字、形状，两个端点共用这一张表。 */
export interface MethodMap {
  handshake: { params: HandshakeParams; result: HandshakeResult };
  capabilities: { params: undefined; result: Capabilities };
  createSession: { params: CreateSessionParams; result: CreateSessionResult };
  send: { params: SendParams; result: SendResult };
  interrupt: { params: InterruptParams; result: InterruptResult };
  close: { params: CloseParams; result: CloseResult };
  respond: { params: RespondParams; result: RespondResult };
  subscribe: { params: SubscribeParams; result: SubscribeResult };
}

export type MethodName = keyof MethodMap;

/** 引擎推向节点的唯一通知名。 */
export const EVENT_METHOD = 'event' as const;

export const ErrorCode = {
  ParseError: -32700,
  InvalidRequest: -32600,
  MethodNotFound: -32601,
  InvalidParams: -32602,
  InternalError: -32603,
  Unauthorized: -32001,
  NotReady: -32002,
  Timeout: -32003,
  Disconnected: -32004,
} as const;

export class PortError extends Error {
  readonly code: number;
  readonly data: unknown;

  constructor(code: number, message: string, data?: unknown) {
    super(message);
    this.name = 'PortError';
    this.code = code;
    this.data = data;
  }
}

export type JsonRpcId = string | number;

export interface JsonRpcRequest {
  jsonrpc: '2.0';
  id: JsonRpcId;
  method: string;
  params?: unknown;
}

export interface JsonRpcNotification {
  jsonrpc: '2.0';
  method: string;
  params?: unknown;
}

export interface JsonRpcSuccess {
  jsonrpc: '2.0';
  id: JsonRpcId;
  result: unknown;
}

export interface JsonRpcFailure {
  jsonrpc: '2.0';
  id: JsonRpcId;
  error: { code: number; message: string; data?: unknown };
}

export type JsonRpcMessage =
  | JsonRpcRequest
  | JsonRpcNotification
  | JsonRpcSuccess
  | JsonRpcFailure;

export function encodeMessage(message: JsonRpcMessage): string {
  return `${JSON.stringify(message)}\n`;
}

export function isRequest(message: JsonRpcMessage): message is JsonRpcRequest {
  return 'method' in message && 'id' in message;
}

export function isNotification(message: JsonRpcMessage): message is JsonRpcNotification {
  return 'method' in message && !('id' in message);
}

export function isResponse(message: JsonRpcMessage): message is JsonRpcSuccess | JsonRpcFailure {
  return !('method' in message) && 'id' in message;
}

/** 把任意异常翻成 JSON-RPC 错误体，PortError 保留自己的 code。 */
export function toErrorBody(error: unknown): { code: number; message: string; data?: unknown } {
  if (error instanceof PortError) {
    return error.data === undefined
      ? { code: error.code, message: error.message }
      : { code: error.code, message: error.message, data: error.data };
  }
  const message = error instanceof Error ? error.message : String(error);
  return { code: ErrorCode.InternalError, message };
}

export function parseMessage(line: string): JsonRpcMessage {
  let value: unknown;
  try {
    value = JSON.parse(line);
  } catch {
    throw new PortError(ErrorCode.ParseError, 'frame is not valid JSON');
  }
  if (typeof value !== 'object' || value === null) {
    throw new PortError(ErrorCode.InvalidRequest, 'frame is not an object');
  }
  const message = value as JsonRpcMessage;
  if (message.jsonrpc !== '2.0') {
    throw new PortError(ErrorCode.InvalidRequest, 'missing jsonrpc 2.0 marker');
  }
  return message;
}
