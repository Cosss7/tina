# H0 启动链路与 P1 Go 契约的验证入口。离线可跑，不调真实模型。
# 依赖先装一次：pnpm install（Go 模块第一次 build 时拉）。
#
# USERPROFILE / LOCALAPPDATA 是给 Windows 版 go.exe 补的：make 的配方环境里没有
# 这两个变量，go 定位不到模块缓存和构建缓存，最后会去 C:\WINDOWS 建临时目录。
# 这两个值从嵌套 bash 的 $HOME 推——make 自己的 $HOME 是 MSYS 的 /home/xxx，
# 不是 Windows 的用户目录。没有 cygpath 的机器（Linux、CI）整段跳过。

HAVE_CYGPATH := $(shell command -v cygpath >/dev/null 2>&1 && echo yes)
ifeq ($(HAVE_CYGPATH),yes)
USERPROFILE := $(shell bash -c 'cygpath -w "$$HOME"')
LOCALAPPDATA := $(shell bash -c 'cygpath -w "$$HOME/AppData/Local"')
export USERPROFILE
export LOCALAPPDATA
endif

TSC := node ./node_modules/typescript/bin/tsc
EXE := $(shell go env GOEXE)
BIN := .build/control-plane$(EXE)

.PHONY: verify web build typecheck test up demo clean

verify: web build typecheck test

web:
	node apps/web/build.mjs
	rm -rf services/control-plane/web
	mkdir -p services/control-plane/web
	cp -R apps/web/dist/. services/control-plane/web/

build: web
	@mkdir -p .build
	go -C services/control-plane build -o ../../$(BIN) .

typecheck:
	$(TSC) -p tsconfig.json

test:
	go -C services/control-plane test ./...
	go -C packages/protocol test ./...
	go -C services/tinad test ./...
	node --test

up: build
	node apps/cli/src/main.ts up

demo: build
	bash demo.sh

clean:
	rm -rf .build services/control-plane/web apps/web/dist
