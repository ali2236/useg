SHELL := /bin/bash
HOSTARCH := $(shell go env GOARCH 2>/dev/null)
DB ?= /var/lib/useg/useg.db

.PHONY: test build install update reset dev dev-fresh down logs sh client-a a-curl b-curl probe redeploy

test:
	go vet ./...
	go test ./...

# The Linux binary the container and the box both run.
build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$(HOSTARCH) go build -trimpath -o bin/useg-linux ./cmd/useg
	CGO_ENABLED=0 go build -trimpath -o bin/useg ./cmd/useg

# Install on the Debian box (deps, build, config, systemd units, start).
# Run from the checkout as root: sudo make install
install:
	bash deploy/install.sh

# Pull the latest commit and reinstall: rebuild + restart the service.
# Config in /etc/useg/config.yaml and data in /var/lib/useg are untouched.
update:
	git pull --ff-only
	$(MAKE) install

# Delete every usage row (all periods). Run as root on the box: sudo make reset
# The agent is stopped first so its in-memory buckets cannot flush back in.
# For the dev rig: make reset DB=dev/data/useg.db
reset:
	test -f $(DB)
	systemctl stop useg 2>/dev/null || true
	sqlite3 $(DB) "delete from usage;"
	# sqlite may have created WAL files as root; the service runs as useg
	chown useg:useg $(DB) $(DB)-wal $(DB)-shm 2>/dev/null || true
	systemctl start useg 2>/dev/null || true

dev: build
	TARGETARCH=$(HOSTARCH) docker compose up --build

# Rebuild the binary + gateway image and swap the container, keeping dev/data.
redeploy:
	./dev/redeploy.sh

down:
	docker compose down

dev-fresh:
	rm -rf dev/data
	docker compose down --remove-orphans

logs:
	docker compose logs -f gateway

sh:
	docker compose exec gateway sh

# Real traffic through the box, from a container that is a LAN client.
a-curl:
	docker compose exec client-a sh -c 'curl -sS -o /dev/null -w "http %{http_code} via $(uname -m) %{time_total}s\n" https://example.com'

b-curl:
	docker compose exec client-b sh -c 'curl -sS -o /dev/null -w "http %{http_code} %{time_total}s\n" https://example.com'

probe:
	docker compose exec gateway useg probe -c /etc/useg/config.yaml
