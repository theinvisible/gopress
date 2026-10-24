BIN      := bin/gopress
PKG      := github.com/theinvisible/gopress
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)
COMPOSE  := docker compose -f dev/compose.yaml

.PHONY: build run test fmt clean testdata devdb-up devdb-down devdb-reset devdb-counts

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
