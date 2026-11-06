#!/usr/bin/env bash
# 从空库跑到"卡片进已完成"，每一步状态打在终端上。
#
# 它自己起控制面和节点，跑完收掉；不需要先 make up。默认用内存库，
# 想留证据就 TINA_DB=.tina/demo.db bash demo.sh（记得先建目录）。
#
# 默认引擎是 fake。换成真 dsh：
#   TINA_ENGINE_BIN=<packages/engine-dsh/src/bin.ts> \
#   TINA_DSH_BIN=<dsh 的 lib/bin.js> \
#   TINA_WORK_DIR=<工作目录> \
#   bash demo.sh

set -euo pipefail

PORT="${TINA_PORT:-7801}"
BASE="http://127.0.0.1:${PORT}"
PROJECT="${TINA_PROJECT:-proj-website}"
BIN="$(ls .build/control-plane* 2>/dev/null | head -1 || true)"

if [[ -z "${BIN}" ]]; then
  echo "先跑一次 make build，生成 .build/control-plane" >&2
  exit 1
fi

step() { printf '\n== %s ==\n' "$*"; }
note() { printf '  %s\n' "$*"; }

# 请求体写进临时文件，再用 --data-binary @file 发出去。
# 直接把中文放在命令行参数里，Git Bash 会按系统代码页（GBK）转给 curl.exe，
# 控制面收到的就不是合法 UTF-8 了。
post() {
  local path="$1" body="$2" tmp
  tmp="$(mktemp)"
  printf '%s' "$body" >"${tmp}"
  curl -fsS -X POST "${BASE}${path}" -H 'content-type: application/json' --data-binary "@${tmp}"
  rm -f "${tmp}"
}

json() { node -e '
  let raw = "";
  process.stdin.on("data", (chunk) => (raw += chunk));
  process.stdin.on("end", () => {
    let value = JSON.parse(raw);
    for (const key of process.argv[1].split(".")) value = value?.[key];
    if (value === undefined) process.exit(1);
    process.stdout.write(typeof value === "string" ? value : JSON.stringify(value));
  });
' "$1"; }

board() {
  curl -fsS "${BASE}/api/board?project=${PROJECT}" | node -e '
    let raw = "";
    process.stdin.on("data", (chunk) => (raw += chunk));
    process.stdin.on("end", () => {
      const board = JSON.parse(raw);
      for (const card of board.cards) {
        const kind = card.kind === "demand" ? "需求" : "任务";
        console.log(`   - [${card.status}] ${card.title}（${kind}）`);
      }
      for (const approval of board.approvals) {
        console.log(`   ! 待批 ${approval.kind}：${approval.payload?.summary ?? ""}`);
      }
    });
  '
}

card_status() {
  curl -fsS "${BASE}/api/board?project=${PROJECT}" | node -e '
    let raw = "";
    process.stdin.on("data", (chunk) => (raw += chunk));
    process.stdin.on("end", () => {
      const board = JSON.parse(raw);
      const card = board.cards.find((item) => item.id === process.argv[1]);
      process.stdout.write(card === undefined ? "missing" : card.status);
    });
  ' "$1"
}

wait_status() {
  local id="$1" want="$2"
  for _ in $(seq 1 600); do
    [[ "$(card_status "${id}")" == "${want}" ]] && return 0
    sleep 0.5
  done
  return 1
}

CP_PID=""
NODE_PID=""
cleanup() {
  [[ -n "${NODE_PID}" ]] && kill "${NODE_PID}" 2>/dev/null || true
  [[ -n "${CP_PID}" ]] && kill "${CP_PID}" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

step "起控制面和节点"
"${BIN}" -addr "127.0.0.1:${PORT}" >/tmp/tina-demo-cp.log 2>&1 &
CP_PID=$!
for _ in $(seq 1 100); do
  curl -fsS "${BASE}/health" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -fsS "${BASE}/health" >/dev/null || { echo "控制面没起来" >&2; exit 1; }
TINA_CONTROL_PLANE_URL="ws://127.0.0.1:${PORT}/ws/node" node packages/node-runtime/src/main.ts >/tmp/tina-demo-node.log 2>&1 &
NODE_PID=$!
for _ in $(seq 1 100); do
  curl -fsS "${BASE}/api/state" | grep -q '"online":true' && break
  sleep 0.1
done
note "控制面 ${BASE}，节点已注册"

step "1. 建卡"
TASK_ID=$(post "/api/cards" "{\"projectId\":\"${PROJECT}\",\"title\":\"网站改版\",\"description\":\"首屏和接口一起改，下周上线\"}" | json id)
note "需求卡 ${TASK_ID}"

step "2. Router 拆卡"
APPROVAL_ID=$(post "/api/plans" "{\"projectId\":\"${PROJECT}\",\"taskId\":\"${TASK_ID}\"}" | json id)
note "计划 ${APPROVAL_ID} 待批"
board

step "3. 批计划（批准后才会创建任务卡）"
PLAN_TASKS=$(post "/api/plans/${APPROVAL_ID}/decide" "{\"projectId\":\"${PROJECT}\",\"decision\":\"approved\"}" | json tasks)
mapfile -t IDS < <(echo "${PLAN_TASKS}" | node -e '
  let raw = "";
  process.stdin.on("data", (chunk) => (raw += chunk));
  process.stdin.on("end", () => {
    for (const task of JSON.parse(raw)) console.log(task.id);
  });
')
note "新任务卡 ${#IDS[@]} 张"
board

step "4. 派活（每张卡一个会话）"
for id in "${IDS[@]}"; do
  post "/api/tasks/${id}/dispatch" "{\"projectId\":\"${PROJECT}\"}" >/dev/null
  note "派出 ${id}"
done
for id in "${IDS[@]}"; do
  wait_status "${id}" review || { echo "有卡没跑到评审中：${id}" >&2; board; exit 1; }
done
note "全部跑完，报告已落库"
board

step "5. 人终审第一张"
post "/api/tasks/${IDS[0]}/review" "{\"projectId\":\"${PROJECT}\",\"decision\":\"approved\",\"note\":\"验收通过\"}" >/dev/null
board

step "完成"
note "库里：1 张需求卡 + ${#IDS[@]} 张任务卡；第一张已终审通过，其余停在评审中"
