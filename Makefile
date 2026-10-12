SHELL := /bin/bash
GO    ?= go

.DEFAULT_GOAL := help

.PHONY: help
help: ## Daftar target
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ─── Go ────────────────────────────────────────────────────────────────────────

.PHONY: build
build: ## Build semua binari
	$(GO) build ./...

.PHONY: test
test: ## go vet + unit test (termasuk validasi semua contoh payload)
	$(GO) vet ./...
	$(GO) test ./... $(TESTFLAGS)

.PHONY: lint
lint: ## golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## gofmt -w
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy

# ─── Infra ─────────────────────────────────────────────────────────────────────

COMPOSE := docker compose --env-file .env -f deploy/docker-compose.yml

.PHONY: certs
certs: ## CA internal + sertifikat server (SAN dari RAIN_BROKER_HOST) + client cert opsional
	bash deploy/certs/make-certs.sh

.PHONY: up
up: ## Nyalakan seluruh stack: broker, database, migrasi, lalu ingest
	@test -f .env || { echo "Belum ada .env — salin dari .env.example dan isi passwordnya."; exit 1; }
	@test -f deploy/certs/out/server.crt || { echo "Belum ada sertifikat — jalankan 'make certs' dulu."; exit 1; }
	$(COMPOSE) up -d emqx postgres
	@echo "Menunggu broker dan database healthy…"
	@for i in $$(seq 1 60); do \
		if ! $(COMPOSE) ps emqx postgres --format '{{.Name}} {{.Health}}' | grep -qv ' healthy$$'; then break; fi; \
		sleep 2; \
	done
	bash deploy/emqx/bootstrap.sh
	@$(MAKE) --no-print-directory migrate
	$(COMPOSE) up -d ingest
	$(COMPOSE) ps --format 'table {{.Name}}\t{{.Status}}'

.PHONY: up-infra
up-infra: ## Broker + database saja, tanpa ingest
	$(COMPOSE) up -d emqx postgres
	bash deploy/emqx/bootstrap.sh

.PHONY: down
down: ## Matikan stack (volume tetap)
	$(COMPOSE) down

.PHONY: clean
clean: ## Matikan stack DAN hapus volume (data hilang)
	$(COMPOSE) down -v

.PHONY: ps
ps: ## Status container
	$(COMPOSE) ps

.PHONY: logs
logs: ## Ikuti log semua container
	$(COMPOSE) logs -f

.PHONY: bootstrap
bootstrap: ## Buat/segarkan user EMQX dari .env
	bash deploy/emqx/bootstrap.sh

# ─── Skenario uji ──────────────────────────────────────────────────────────────

.PHONY: scenario-1
scenario-1: ## Koneksi TLS + verifikasi sertifikat
	bash scripts/scenarios/01-connect-tls.sh

.PHONY: scenario-2
scenario-2: ## Uji negatif: kredensial salah & pelanggaran ACL harus ditolak
	bash scripts/scenarios/02-negative.sh

# ─── Database ──────────────────────────────────────────────────────────────────

.PHONY: migrate
migrate: ## Jalankan migrasi lalu seed devices dari contracts/devices.json
	set -a && . ./.env && set +a && $(GO) run ./cmd/migrate up

.PHONY: migrate-down
migrate-down: ## Rollback semua migrasi
	set -a && . ./.env && set +a && $(GO) run ./cmd/migrate down

.PHONY: migrate-version
migrate-version: ## Versi skema saat ini
	set -a && . ./.env && set +a && $(GO) run ./cmd/migrate version

.PHONY: seed
seed: ## Seed ulang devices saja
	set -a && . ./.env && set +a && $(GO) run ./cmd/migrate seed

.PHONY: psql
psql: ## Shell psql di container postgres
	$(COMPOSE) exec postgres psql -U $${RAIN_PG_USER:-rain} -d $${RAIN_PG_DB:-rain}

.PHONY: check
check: ## Query bukti dari sql/checks.sql. Ubah jendela: make check WINDOW="15 minutes"
	$(COMPOSE) exec -T postgres psql -U $${RAIN_PG_USER:-rain} -d $${RAIN_PG_DB:-rain} \
		$(if $(WINDOW),-v window="$(WINDOW)",) -f - < sql/checks.sql

.PHONY: reset-data
reset-data: ## Kosongkan tabel data (devices tetap). Dipakai sebelum skenario.
	$(COMPOSE) exec -T postgres psql -U $${RAIN_PG_USER:-rain} -d $${RAIN_PG_DB:-rain} \
		-c "TRUNCATE telemetry, device_state, state_log, device_status, alarms, ingest_rejects RESTART IDENTITY;"

# ─── Layanan ───────────────────────────────────────────────────────────────────
# RAIN_MQTT_USER/PASS di .env dipakai untuk peran "publisher". Ingest memakai
# kredensialnya sendiri, jadi target ini menimpanya.

.PHONY: ingest
ingest: ## Jalankan ingest (MQTT → PostgreSQL)
	set -a && . ./.env && set +a && \
	RAIN_MQTT_USER=ingest RAIN_MQTT_PASS="$$RAIN_INGEST_PASS" \
	$(GO) run ./cmd/ingest

.PHONY: test-integration
test-integration: ## Unit test + test yang butuh PostgreSQL (stack harus 'make up' dulu)
	set -a && . ./.env && set +a && \
	RAIN_TEST_PG_DSN="$$RAIN_PG_DSN" $(GO) test ./... $(TESTFLAGS)

.PHONY: sim
sim: ## Jalankan simulator (stand-in CX5240). Contoh: make sim SIMFLAGS="--duration 60s --alarm-demo"
	set -a && . ./.env && set +a && \
	RAIN_MQTT_USER=simulator-dev RAIN_MQTT_PASS="$$RAIN_SIM_PASS" \
	$(GO) run ./cmd/simulator $(SIMFLAGS)

.PHONY: scenario-3
scenario-3: ## Kelengkapan >= 99% (DURATION=60m default; pakai DURATION=5m saat dev)
	bash scripts/scenarios/03-completeness.sh

.PHONY: scenario-4
scenario-4: ## Deteksi offline lewat LWT < 10 s
	bash scripts/scenarios/04-lwt.sh

.PHONY: scenario-5
scenario-5: ## Replay buffer setelah outage, tanpa gap dan tanpa duplikat
	bash scripts/scenarios/05-replay.sh

.PHONY: scenario-6
scenario-6: ## Restart broker dan ingest, pulih otomatis < 30 s
	bash scripts/scenarios/06-restart.sh

.PHONY: scenarios
scenarios: ## Semua skenario 01..06 berurutan
	@for n in 1 2 3 4 5 6; do \
		$(MAKE) --no-print-directory scenario-$$n || exit 1; \
	done
