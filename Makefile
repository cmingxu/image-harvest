GO ?= go
BIN ?= bin
WEB_DIR ?= web
DOMAIN ?= ifeng.com

.PHONY: all build build-web test vet run-harvest run-server clean deploy crawl

all: build build-web

build:
	@mkdir -p $(BIN)
	$(GO) build -o $(BIN)/image-harvest ./cmd/harvest
	$(GO) build -o $(BIN)/harvest-web ./cmd/harvest-web

# build with embedded frontend (no webui/dist needed at runtime):
#   go build -tags embed -o bin/harvest-web ./cmd/harvest-web

build-web:
	cd $(WEB_DIR) && npm install && npm run build

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

run-harvest: build
	$(BIN)/image-harvest $(DOMAIN)

run-server: build build-web
	$(BIN)/harvest-web -db harvest.duckdb -out .

clean:
	rm -rf $(BIN) webui/dist web/node_modules

# remote deploy (systemd user unit) and remote crawl against the NAS media root
deploy:
	./scripts/deploy.sh

crawl:
	./scripts/crawl.sh $(ARGS)
