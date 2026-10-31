.PHONY: web admin build test check-admin
web:
	cd web && npm ci --no-audit --no-fund && npm run build
	find internal/publicui/dist -mindepth 1 ! -name placeholder.txt -exec rm -rf {} +
	cp -R web/out/. internal/publicui/dist/

admin:
	cd admin && npm ci --no-audit --no-fund && npm run check && npm run build

check-admin: admin
	cd admin && npm run format:check && npm test

build: web admin
	go build -trimpath -o cineroute ./cmd/cineroute

test:
	go test ./...
