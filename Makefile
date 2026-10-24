BIN      := bin/gopress
PKG      := github.com/theinvisible/gopress
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)
COMPOSE  := docker compose -f dev/compose.yaml

.PHONY: build run test fmt clean testdata devdb-up devdb-down devdb-reset devdb-counts wpref-up wpref-down golden import serve parity vulncheck editor admin-assets e2e roundtrip

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

# Needs WP_HOST=user@server (see dev/fetch-testdata.sh for more settings).
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

# Regenerate golden files for the render tests from the reference WordPress:
# the generic corpus (committed) and the imported site's content (local only).
golden: wpref-up
	$(COMPOSE) exec -T wpref php /golden/gen.php edge > internal/render/testdata/golden.json
	$(COMPOSE) exec -T wpref php /golden/gen.php site > local/golden-site.json
	$(COMPOSE) exec -T wpref php /golden/auth.php > internal/auth/testdata/wphash.json

# Import the dev database into ./local/data (replaces it).
import: build
	./$(BIN) import-wp --dsn 'wordpress:wordpress@tcp(127.0.0.1:33066)/wordpress' --uploads ./local/uploads --data ./local/data --force

# (Re)start GoPress on 127.0.0.1:8080 in the background.
serve:
	dev/serve-local.sh

# Compare GoPress (8080) with the reference WordPress (8081). Set SITE_HOST
# to the original site's host name (absolute links in the content).
parity: wpref-up serve
	go run ./dev/parity -site-host "$(SITE_HOST)"

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# The post editor (web/editor, TipTap) is built into web/admin/static/editor,
# which is committed: building GoPress itself needs no Node.
editor:
	cd web/editor && npm ci --no-audit --no-fund && npm run build

# Copy the WordPress admin stylesheets from a WordPress checkout
# (default local/wp-core) into web/admin/static/wp.
admin-assets:
	dev/admin-assets.sh $(WP_CORE)

# End-to-end tests of the admin with the local Chrome against a running
# server (BASE_URL=http://localhost:8080 dev/serve-local.sh), credentials
# in local/e2e-credentials.json.
e2e:
	cd dev/e2e && npm ci --no-audit --no-fund && node e2e.mjs

# Editor round trip over all local posts: no content loss, same rendering.
roundtrip:
	go run ./dev/exportcontent > local/editor-content.json
	node web/editor/test/roundtrip.mjs local/editor-content.json
	go test ./internal/content -run 'RoundTrip|Classic' -v
