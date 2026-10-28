.PHONY: web build test
web:
	cd web && npm ci --no-audit --no-fund && npm run build
	find internal/publicui/dist -mindepth 1 ! -name placeholder.txt -exec rm -rf {} +
	cp -R web/out/. internal/publicui/dist/

build: web
	go build -trimpath -o cineroute ./cmd/cineroute

test:
	go test ./...
