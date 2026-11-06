/**
 * 给两类会话的提示词。H0 用固定 prompt，不读项目约定（`docs/h0.md` 第 3 节）。
 *
 * 开头的 `[TINA:PLAN]` / `[TINA:REPORT]` 是给 fake engine 匹配场景用的稳定标记，
 * 换成真 dsh 之后它们仍然有用：日志里一眼能分出这是哪类会话。
 */

export interface PlanPromptInput {
  title: string;
  description: string;
}

export function buildPlanPrompt(input: PlanPromptInput): string {
  return [
    '[TINA:PLAN]',
    '你是这个项目的协调者。把下面这张卡拆成可执行的任务卡，不要自己动手做。',
    '',
    `标题：${input.title}`,
    `描述：${input.description || '（没有更多描述）'}`,
    '',
    '只输出一个 JSON 对象，不要别的话：',
    '{"summary":"一句话说明这批卡要达成什么",',
    ' "cards":[{"key":"c1","title":"卡标题","description":"这张卡要做什么",',
    '           "acceptance":["可验证的验收点"],"deps":["c1"]}]}',
    '',
    '要求：2 到 4 张卡；每张至少一条可验证的验收标准；deps 只写同一计划里的 key。',
  ].join('\n');
}

export interface ReportPromptInput {
  title: string;
  description: string;
}

export function buildReportPrompt(input: ReportPromptInput): string {
  return [
    '[TINA:REPORT]',
    '你是执行者。完成下面这张卡，然后交一份结构化报告。',
    '',
    `标题：${input.title}`,
    `描述：${input.description || '（没有更多描述）'}`,
    '',
    '只输出一个 JSON 对象，不要别的话：',
    '{"state":"done","summary":"做了什么、做到什么程度",',
    ' "decisions":["过程中做过的判断"],',
    ' "artifacts":[{"path":"产物路径","note":"这是什么"}],',
    ' "blockers":[],"usage":{"inputTokens":0,"outputTokens":0}}',
    '',
    'state 只能是 done / blocked / failed。有做不下去的地方写进 blockers。',
  ].join('\n');
}
