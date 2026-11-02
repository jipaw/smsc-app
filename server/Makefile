# Server SMSC. Butuh Go 1.24+, PostgreSQL 16 (untuk tes), Node 22 (untuk dashboard).
DB ?= postgres://nobody@localhost:55432/postgres?sslmode=disable

.PHONY: build test unit integration contract web verify
build:
	go build -o bin/smscd ./cmd/smscd

unit:
	go test ./internal/...

# tes integrasi memakai DB sementara yang dibuat/dihapus otomatis
integration:
	SMSC_TEST_DATABASE_URL='$(DB)' go test ./tests -count=1 -timeout 300s

contract: build
	SMSC_TEST_DATABASE_URL='$(DB)' python3 tests/contract_check.py bin/smscd

test: unit integration contract

# bangun dashboard lalu salin ke folder embed
web:
	cd ../web && npm ci && npm run build
	rm -rf internal/webui/dist && cp -r ../web/dist internal/webui/dist

verify: test
