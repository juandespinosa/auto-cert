# SAM invokes `build-<LogicalId>` when Metadata.BuildMethod: makefile is set
# on the function. Target receives ARTIFACTS_DIR env var with the staging dir
# where the bootstrap binary and bundled assets must end up.

GO_BUILD_FLAGS := -ldflags="-s -w" -tags lambda.norpc -trimpath

.PHONY: build-MonitorFunction local-build local-vet local-test local-run dry-run package clean

# Target name MUST match the function LogicalId in template.yaml.
build-MonitorFunction:
	@if [ -z "$$ARTIFACTS_DIR" ]; then echo "ARTIFACTS_DIR not set (run via 'sam build')"; exit 1; fi
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) -o $$ARTIFACTS_DIR/bootstrap ./cmd/lambda
	cp configs/config.lambda.yaml $$ARTIFACTS_DIR/
	cp configs/static_domains.yaml $$ARTIFACTS_DIR/

# ── Local helpers (run outside SAM) ──────────────────────────────────────

local-build:
	go build ./...

local-vet:
	go vet ./...

local-test:
	go test ./...

local-run:
	go run ./cmd/monitor -config configs/config.yaml

dry-run:
	go run ./cmd/monitor -config configs/config.yaml -dry-run

clean:
	rm -rf .aws-sam state/ bootstrap

# ── SAM convenience ──────────────────────────────────────────────────────

sam-build:
	cd infra && sam build --template template.yaml

sam-deploy: sam-build
	cd infra && sam deploy --guided

sam-validate:
	cd infra && sam validate --template template.yaml

sam-logs:
	sam logs --stack-name auto-certs --tail
