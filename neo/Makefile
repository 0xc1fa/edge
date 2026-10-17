SHELL := /bin/bash
IMAGE  := neo:0.1.9
VER    := 0.1.9

# 宿主没有 Go/Node 工具链，所有编译都在容器里完成。
# 基础镜像用本地已存在的 ubuntu:24.04，Go/Node 从 build/ 下的官方包解压。
GO_IMAGE  := ubuntu:24.04
NODE_MNT  := $(CURDIR)/build:/build:ro

.PHONY: help vet test build web image up down logs restart smoke clean invite password release

help:
	@echo "vet      静态检查（go vet）"
	@echo "test     单元测试（golden fixture 对齐真实 Incus 响应）"
	@echo "build    编译二进制到 bin/"
	@echo "web      构建前端产物到 web/dist"
	@echo "image    构建 Docker 镜像"
	@echo "up/down  启动 / 停止服务"
	@echo "smoke    冒烟验证：健康、鉴权、与 incus list 逐字段比对"
	@echo "invite   签发一条一次性登录凭证（把人重新放进来时的后路）"
	@echo "password 显示管理员口令；RESET=1 重新生成一个"

vet:
	docker run --rm -v "$(CURDIR)":/src -w /src -e HOME=/root $(GO_IMAGE) bash -c \
	  'tar -C /usr/local -xzf build/go.tar.gz && export PATH=/usr/local/go/bin:$$PATH && go vet ./...'

test:
	docker run --rm -v "$(CURDIR)":/src -w /src -e HOME=/root $(GO_IMAGE) bash -c \
	  'tar -C /usr/local -xzf build/go.tar.gz && export PATH=/usr/local/go/bin:$$PATH && go test ./...'

build:
	docker run --rm -v "$(CURDIR)":/src -w /src -e HOME=/root $(GO_IMAGE) bash -c \
	  'tar -C /usr/local -xzf build/go.tar.gz && export PATH=/usr/local/go/bin:$$PATH && \
	   CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=$(VER)" -o bin/neo ./cmd/neo'

web:
	# logo/ 必须一并挂进去：ui.tsx 用 ../../../logo/logo.svg 引用仓库根的标识，
	# 容器内对应 /logo —— 与 Dockerfile 里 COPY logo /logo 的位置保持一致。
	docker run --rm --network host --dns 223.5.5.5 -v "$(NODE_MNT)" \
	  -v "$(CURDIR)/web":/app -v "$(CURDIR)/logo":/logo:ro -w /app $(GO_IMAGE) bash -c \
	  'tar -C /usr/local --strip-components=1 -xf /build/node.tar && \
	   npm install --no-audit --no-fund --registry=https://registry.npmmirror.com && npm run build'

image: web
	docker build --network host --build-arg VERSION=$(VER) -t $(IMAGE) .

up:
	docker compose up -d

down:
	docker compose down

restart:
	docker compose restart

logs:
	docker compose logs -f --tail=100

smoke:
	./scripts/smoke.sh

invite:
	@docker compose run --rm --no-deps --entrypoint /usr/local/bin/neo neo invite --name $(or $(NAME),管理员) --role admin --ttl 15m

password:
	@docker compose run --rm --no-deps --entrypoint /usr/local/bin/neo neo password $(if $(RESET),--reset,)

clean:
	rm -rf bin web/dist

release: vet test image
	@echo "镜像就绪：$(IMAGE)"
