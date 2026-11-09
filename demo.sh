#!/usr/bin/env bash
# 完整 Go 控制面 + Go tinad 闭环；默认数据隔离在 .build，可显式保留控制面库。
set -euo pipefail
exec node apps/cli/src/demo.ts
