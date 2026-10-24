BIN      := bin/gopress
PKG      := github.com/theinvisible/gopress
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)
COMPOSE  := docker compose -f dev/compose.yaml

.PHONY: build run test fmt clean testdata devdb-up devdb-down devdb-reset devdb-counts wpref-up wpref-down golden import serve parity vulncheck

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gopress

run: build
	./$(BIN) serve

test:
	go vet ./...
	go test ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin

# --- local test data (gitignored, see dev/) ---

testdata:
	dev/fetch-testdata.sh

devdb-up:
	@test -f local/wp-dump.sql.gz || { echo "local/wp-dump.sql.gz missing, run 'make testdata'"; exit 1; }
	$(COMPOSE) up -d --wait

devdb-down:
	$(COMPOSE) down

devdb-reset:
	$(COMPOSE) down -v
	$(MAKE) devdb-up

devdb-counts:
	$(COMPOSE) exec -T wpdb mariadb -uwordpress -pwordpress wordpress < dev/counts.sql | tee local/reference-counts.txt

wpref-up: devdb-up
	$(COMPOSE) up -d wpref
	@until curl -fsS -o /dev/null http://127.0.0.1:8081/; do sleep 1; done
	@echo "reference WordPress on http://127.0.0.1:8081"

wpref-down:
	$(COMPOSE) stop wpref

# Regenerate golden files for the render package from the reference WordPress.
golden: wpref-up
	$(COMPOSE) exec -T wpref php /golden/gen.php > internal/render/testdata/golden.json

# Import the dev database into ./local/data (replaces it).
import: build
	./$(BIN) import-wp --dsn 'wordpress:wordpress@tcp(127.0.0.1:33066)/wordpress' --uploads ./local/uploads --data ./local/data --force

# (Re)start GoPress on 127.0.0.1:8080 in the background.
serve:
	dev/serve-local.sh

# Compare GoPress (8080) with the reference WordPress (8081).
parity: wpref-up serve
	go run ./dev/parity

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
