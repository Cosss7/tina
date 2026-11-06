# H0 的验证入口。离线可跑：不联网、不调真实模型，引擎一律用 fake。
# 依赖先装一次：pnpm install

TSC := node ./node_modules/typescript/bin/tsc

.PHONY: verify typecheck test

verify: typecheck test

typecheck:
	$(TSC) -p tsconfig.json

test:
	node --test
