SHELL := /bin/bash
IMAGE  := neo:0.1.0
VER    := 0.1.0

# 宿主没有 Go/Node 工具链，所有编译都在容器里完成。
# 基础镜像用本地已存在的 ubuntu:24.04，Go/Node 从 build/ 下的官方包解压。
GO_IMAGE  := ubuntu:24.04
NODE_MNT  := $(CURDIR)/build:/build:ro

.PHONY: help vet test build web image up down logs restart smoke clean token release

help:
	@echo "vet      静态检查（go vet）"
	@echo "test     单元测试（golden fixture 对齐真实 Incus 响应）"
	@echo "build    编译二进制到 bin/"
	@echo "web      构建前端产物到 web/dist"
	@echo "image    构建 Docker 镜像"
	@echo "up/down  启动 / 停止服务"
	@echo "smoke    冒烟验证：健康、鉴权、与 incus list 逐字段比对"
	@echo "token    显示首发管理员口令（仅在尚未登录过时存在）"

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
	docker run --rm --network host --dns 223.5.5.5 -v "$(NODE_MNT)" -v "$(CURDIR)/web":/app -w /app $(GO_IMAGE) bash -c \
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

token:
	@cat data/bootstrap-password.txt 2>/dev/null || echo "明文口令已不存在：要么已完成首次登录，要么服务尚未启动"

clean:
	rm -rf bin web/dist

release: vet test image
	@echo "镜像就绪：$(IMAGE)"
