# Server SMSC. Butuh Go 1.24+, PostgreSQL 16 (untuk tes), Node 22 (untuk dashboard).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DB ?= postgres://nobody@localhost:55432/postgres?sslmode=disable

.PHONY: build test unit integration contract schema load web e2e verify
build:
	go build -ldflags "-X github.com/jipaw/smsc-app/server/internal/buildinfo.Version=$(VERSION)" -o bin/smscd ./cmd/smscd

unit:
	go test ./internal/...

# tes integrasi memakai DB sementara yang dibuat/dihapus otomatis
integration:
	SMSC_TEST_DATABASE_URL='$(DB)' go test ./tests -count=1 -timeout 300s

contract: build
	SMSC_TEST_DATABASE_URL='$(DB)' python3 tests/contract_check.py bin/smscd

schema:
	SMSC_TEST_DATABASE_URL='$(DB)' bash tests/schema_sync.sh

# beban campaign: impor 50.000 penerima + materialisasi + latensi API
load: build
	SMSC_TEST_DATABASE_URL='$(DB)' python3 tests/load_campaign.py bin/smscd 50000

test: unit integration contract schema

# bangun dashboard lalu salin ke folder embed
web:
	cd ../web && npm ci && npm run build
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -r ../web/dist/. internal/webui/dist/

# E2E browser (Chromium) + HP palsu terhadap biner nyata dengan dashboard ter-embed
e2e: web build
	SMSC_TEST_DATABASE_URL='$(DB)' node ../web/e2e/run.cjs bin/smscd

verify: test e2e
