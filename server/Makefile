# Server SMSC. Butuh Go 1.24+, PostgreSQL 16 (untuk tes), Node 22 (untuk dashboard).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DB ?= postgres://nobody@localhost:55432/postgres?sslmode=disable

.PHONY: build test unit integration contract schema alerts load web e2e agent tailnet image verify
build:
	go build -ldflags "-X github.com/jipaw/smsc-app/server/internal/buildinfo.Version=$(VERSION)" -o bin/smscd ./cmd/smscd

unit:
	go test ./internal/...

# tes integrasi memakai DB sementara yang dibuat/dihapus otomatis
integration:
	SMSC_TEST_DATABASE_URL='$(DB)' go test ./tests -count=1 -timeout 300s

contract: build
	SMSC_TEST_DATABASE_URL='$(DB)' python3 tests/contract_check.py bin/smscd

alerts:
	python3 ../ops/prometheus/check_alerts.py
	python3 ../ops/grafana/test_import.py

schema:
	SMSC_TEST_DATABASE_URL='$(DB)' bash tests/schema_sync.sh

# beban campaign: impor 50.000 penerima + materialisasi + latensi API
load: build
	SMSC_TEST_DATABASE_URL='$(DB)' python3 tests/load_campaign.py bin/smscd 50000

test: unit integration contract schema alerts

# bangun dashboard lalu salin ke folder embed
web:
	cd ../web && npm ci && npm run build
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -r ../web/dist/. internal/webui/dist/

# E2E browser (Chromium) + HP palsu terhadap biner nyata dengan dashboard ter-embed
e2e: web build
	SMSC_TEST_DATABASE_URL='$(DB)' node ../web/e2e/run.cjs bin/smscd

# agen perangkat JVM (Kotlin :core + OkHttp, SMS simulasi) terhadap biner smscd nyata
agent: build
	cd ../android && ./gradlew -q :agent:installDist && SMSC_TEST_DATABASE_URL='$(DB)' python3 agent/e2e.py ../server/bin/smscd agent/build/install/smsc-agent/bin/smsc-agent

# tailnet ter-embed: bridge tsnet (Go) + lab end-to-end: agen JVM --SOCKS5--> tsnet --tailnet--> node "smsc" --> smscd
tailnet: build
	cd ../android && ./gradlew -q :agent:installDist
	cd ../android/tsnetbridge && SMSC_LAB_SMSCD=$(CURDIR)/bin/smscd SMSC_LAB_AGENT=$(CURDIR)/../android/agent/build/install/smsc-agent/bin/smsc-agent \
		SMSC_TEST_DATABASE_URL='$(DB)' go test -count=1 ./...

# image Docker (dashboard ter-embed); di balik proxy TLS: make image DOCKER_BUILD_ARGS='--secret id=ca_bundle,src=/path/ca.crt'
image:
	cd .. && docker build $(DOCKER_BUILD_ARGS) -t smsc/server:$(VERSION) -t smsc/server:latest --build-arg VERSION=$(VERSION) .

verify: test e2e agent tailnet
