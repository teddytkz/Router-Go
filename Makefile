.PHONY: web build dev test clean

web:            ## build the React dashboard into web/dist (embedded by Go)
	cd web && bun install && bun run build

build: web      ## single binary with embedded dashboard
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o router-go ./cmd/router-go

dev:            ## Go API with live reload (Air) on :20128 — run `cd web && bun run dev` alongside for HMR on :5173
	@command -v air >/dev/null 2>&1 || { echo "air not found: go install github.com/air-verse/air@latest"; exit 1; }
	air

test:
	go vet ./... && go test ./... && cd web && bun run typecheck

clean:
	rm -rf router-go web/dist/assets
