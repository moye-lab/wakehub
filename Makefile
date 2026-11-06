.PHONY: tidy test build-web build-go run-server run-agent docker-up

tidy:
	go mod tidy

test:
	go test -v ./internal/...

build-web:
	cd web && npm ci && npm run build

build-go:
	CGO_ENABLED=0 go build -o bin/wakehub-server ./cmd/server
	CGO_ENABLED=0 go build -o bin/wakehub-agent ./cmd/agent

run-server:
	go run ./cmd/server

run-agent:
	go run ./cmd/agent

docker-up:
	@if [ ! -f .env ]; then echo "缺少 .env，请先运行 ./install.sh 生成配置" >&2; exit 1; fi
	@docker compose up -d --build
