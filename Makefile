.PHONY: build cli controller ui-build test test-race linux clean quality check packages packages-linux
# Frontend source changes regenerate checked-in assets; ordinary Go builds need no Node runtime.
ui-build:
	cd ui && npm ci --no-audit --no-fund && npm run build
build: cli controller

cli:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/hostlier ./cmd/hostlier
	mkdir -p dist/packages
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/packages/minion ./packages/minion/cmd/hostlier-minion
controller:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/hostlier-controller ./cmd/hostlier-controller
test:
	go test ./...
test-race:
	go test -race ./...
linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/hostlier-linux-amd64 ./cmd/hostlier
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/hostlier-controller-linux-amd64 ./cmd/hostlier-controller
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/hostlier-linux-arm64 ./cmd/hostlier
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/hostlier-controller-linux-arm64 ./cmd/hostlier-controller
clean:
	rm -rf dist

quality:
	go run ./cmd/quality .
check: quality
	go vet ./...
	go test -race ./...

# Optional engines and provider helpers are built independently from the controller.
PACKAGE_COMMANDS := postgres redis neo4j aiven railway package
packages:
	mkdir -p dist
	@for name in $(PACKAGE_COMMANDS); do CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o dist/hostlier-$$name ./cmd/hostlier-$$name || exit $$?; done
packages-linux:
	mkdir -p dist
	@for arch in amd64 arm64; do for name in $(PACKAGE_COMMANDS); do CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags='-s -w' -o dist/hostlier-$$name-linux-$$arch ./cmd/hostlier-$$name || exit $$?; done; done

# Release archives include the image assets automatically installed by hostlier init.
.PHONY: release
release:
	./scripts/release.sh

.PHONY: cli-release
cli-release:
	./scripts/package-cli.sh

# Minion is Hostlier's project-local framework payload, included by the CLI release target.
.PHONY: minion-package
minion-package:
	./scripts/package-minion.sh "$$(pwd)/dist/minion"

.PHONY: e2e-fleet
e2e-fleet:
	python3 test/e2e/fleet/run.py
ifeq ($(FLEET_TEST_IDLE),1)
	$(MAKE) e2e-k3s-idle
endif

.PHONY: e2e-k3s-idle
e2e-k3s-idle:
	python3 test/e2e/fleet/run.py --k3s-idle


# Regenerate after editing the controller contract (requires protoc and both Go plugins).
.PHONY: control-proto
control-proto:
	protoc --go_out=. --go_opt=module=hostlier --connect-go_out=. --connect-go_opt=module=hostlier internal/controlrpc/controller.proto
	node scripts/build-cloud-sdk.mjs
