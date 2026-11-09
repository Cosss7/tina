#!/usr/bin/env bash
# P2 演示：只启动控制面，创建需求并持久排队。完整引擎闭环在 P3/P4 恢复。
# TINA_CONTROL_DB=.tina/demo.db bash demo.sh 可保留数据；默认使用内存库。
set -euo pipefail
PORT="${TINA_PORT:-7801}"
BASE="http://127.0.0.1:${PORT}"
PROJECT="${TINA_PROJECT:-proj-website}"
DEMO_DB="${TINA_CONTROL_DB:-:memory:}"
BIN=".build/control-plane"
[[ -f "${BIN}.exe" ]] && BIN="${BIN}.exe"
[[ -f "${BIN}" ]] || { echo 'Run make build first.' >&2; exit 1; }
CP_PID=""
cleanup() {
  if [[ -n "${CP_PID}" ]]; then
    kill "${CP_PID}" 2>/dev/null || true
    wait "${CP_PID}" 2>/dev/null || true
  fi
}
trap cleanup EXIT
if curl -fsS "${BASE}/health" >/dev/null 2>&1; then
  printf 'Port %s already serves a control plane; choose TINA_PORT.\n' "${PORT}" >&2
  exit 1
fi
"${BIN}" -addr "127.0.0.1:${PORT}" -db "${DEMO_DB}" >.build/demo-control.log 2>&1 &
CP_PID=$!
for _ in $(seq 1 100); do
  kill -0 "${CP_PID}" 2>/dev/null || { cat .build/demo-control.log >&2; exit 1; }
  curl -fsS "${BASE}/health" >/dev/null 2>&1 && break
  sleep 0.1
done
curl -fsS "${BASE}/health" >/dev/null
post() {
  local endpoint="$1" content="$2" temporary
  temporary="$(mktemp)"
  printf '%s' "${content}" >"${temporary}"
  curl -fsS -X POST "${BASE}${endpoint}" -H 'content-type: application/json' --data-binary "@${temporary}"
  rm -f "${temporary}"
}
json_id() { node -e 'let text="";process.stdin.on("data",c=>text+=c);process.stdin.on("end",()=>process.stdout.write(JSON.parse(text).id));'; }
TASK_ID=$(post /api/cards "{\"projectId\":\"${PROJECT}\",\"title\":\"网站改版\",\"description\":\"验证无执行节点时仍可管理项目\"}" | json_id)
post /api/plans "{\"projectId\":\"${PROJECT}\",\"taskId\":\"${TASK_ID}\"}" >/dev/null
curl -fsS "${BASE}/api/board?project=${PROJECT}"
printf '\n'
curl -fsS "${BASE}/api/queue?project=${PROJECT}"
printf '\nSaved request %s. Run is queued; P3/P4 will connect execution.\n' "${TASK_ID}"
