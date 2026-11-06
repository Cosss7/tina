# H0 的验证入口与构建。离线可跑：不联网、不调真实模型，引擎一律用 fake。
# 依赖先装一次：pnpm install（Go 模块第一次 build 时拉）。
#
# USERPROFILE / LOCALAPPDATA 是给 Windows 版 go.exe 补的：make 的配方环境里没有
# 这两个变量，go 定位不到模块缓存和构建缓存，最后会去 C:\WINDOWS 建临时目录。
# 这两个值从嵌套 bash 的 $HOME 推——make 自己的 $HOME 是 MSYS 的 /home/xxx，
# 不是 Windows 的用户目录。

USERPROFILE := $(shell bash -c 'cygpath -w "$$HOME"')
LOCALAPPDATA := $(shell bash -c 'cygpath -w "$$HOME/AppData/Local"')
export USERPROFILE
export LOCALAPPDATA

TSC := node ./node_modules/typescript/bin/tsc
EXE := $(shell go env GOEXE)
BIN := .build/control-plane$(EXE)

.PHONY: verify build typecheck test up clean

verify: build typecheck test

build:
	@mkdir -p .build
	go -C services/control-plane build -o ../../$(BIN) .

typecheck:
	$(TSC) -p tsconfig.json

test: build
	go -C services/control-plane test ./...
	node --test

up: build
	node apps/cli/src/main.ts up

clean:
	rm -rf .build
