.PHONY: build vet test run dry-run install clean

# Build el binario en la raíz del repo (donde lo busca scripts/cron-run.sh).
build:
	go build -o auto-certs ./cmd/monitor

vet:
	go vet ./...

test:
	go test ./...

run:
	go run ./cmd/monitor -config configs/config.yaml

dry-run:
	go run ./cmd/monitor -config configs/config.yaml -dry-run

# install = build + apretar permisos del .env (idempotente, defensa contra
# alguien que edita .env y deja perms abiertos por accidente).
install: build
	@chmod 600 .env 2>/dev/null || true
	@echo "binario actualizado: ./auto-certs"

clean:
	rm -f auto-certs
