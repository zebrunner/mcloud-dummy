# Project checks; `make check` runs the linters, the tests and the image tests (CONVENTIONS.md)
# >>> mcloud-conventions
# The shared part of the Makefile of every MCloud repository (CONVENTIONS.md#makefile), synced from mcloud-conventions
SHELL := /bin/bash
VENV := .venv
export PATH := $(CURDIR)/$(VENV)/bin:$(PATH)
.DEFAULT_GOAL := help

help: ## this list
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | sed -E 's/:.*## /\t/'

# The tools of tests/requirements-lint.txt in .venv; the hadolint-py wheel for macOS is broken, brew provides it there
$(VENV)/.installed: tests/requirements-lint.txt
	python3 -m venv $(VENV)
	@if [[ "$$(uname)" == Darwin ]]; then \
	  grep -v '^hadolint-py' tests/requirements-lint.txt > $(VENV)/requirements.txt; \
	else \
	  cp tests/requirements-lint.txt $(VENV)/requirements.txt; \
	fi
	$(VENV)/bin/pip install --quiet --disable-pip-version-check --requirement $(VENV)/requirements.txt
	@command -v hadolint > /dev/null || { echo "hadolint is missing: brew install hadolint"; exit 1; }
	touch $@
# <<< mcloud-conventions

DOCKER_TESTS := tests/image_test.sh
# gotestsum writes the JUnit XML of the Go tests when JUNIT_DIR is set (CI)
GOTESTSUM := gotest.tools/gotestsum@v1.13.0

# The image (CONVENTIONS.md#makefile): `make build VERSION=<tag>`, `make push VERSION=<tag>` (docker login first);
# IMAGE_REPO overrides the repository, PLATFORMS the platforms of the push
IMAGE_REPO ?= public.ecr.aws/zebrunner/mcloud-dummy
VERSION ?= dev
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: help check lint test docker build push binary run e2e contract

check: ## everything below but build, push, run and e2e, as CI runs it; every step runs, the failed ones fail the target
	@status=0; for target in lint test docker; do $(MAKE) --no-print-directory $$target || status=1; done; exit $$status

lint: $(VENV)/.installed ## the linters and the text file checks (tests/lint.sh), gofmt, go vet and ruff
	tests/lint.sh

test: ## the Go tests with the race detector: the adb protocol, the shell of the device, WDA, the app, the configuration
	@if [[ -n "$(JUNIT_DIR)" ]]; then \
	  mkdir -p "$(JUNIT_DIR)" && go run $(GOTESTSUM) --junitfile "$(JUNIT_DIR)/go-test.xml" -- -race ./...; \
	else \
	  go test -race ./...; \
	fi

docker: ## DOCKER_TESTS: the image is built once, IMAGE overrides its tag and REBUILD=true rebuilds it
	@status=0; for t in $(DOCKER_TESTS); do $$t || status=1; done; exit $$status

build: ## the image IMAGE_REPO:VERSION for the platform of this machine: make build VERSION=<tag>
	docker build --progress=plain --provenance=false --build-arg VERSION=$(VERSION) --tag $(IMAGE_REPO):$(VERSION) .

push: ## the image IMAGE_REPO:VERSION for PLATFORMS, built by docker buildx and pushed: make push VERSION=<tag>
	@[[ "$(VERSION)" != dev ]] || { echo "make push VERSION=<tag>: the tag of the image is required"; exit 1; }
	@docker buildx inspect mcloud > /dev/null 2>&1 || docker buildx create --name mcloud --driver docker-container --driver-opt network=host > /dev/null
	docker buildx build --builder mcloud --progress=plain --provenance=false --platform $(PLATFORMS) \
	  --build-arg VERSION=$(VERSION) --tag $(IMAGE_REPO):$(VERSION) --push .

binary: ## the binary bin/mcloud-dummy for this machine
	CGO_ENABLED=0 go build -trimpath -o bin/mcloud-dummy ./cmd/mcloud-dummy

run: binary ## one Android and one iOS dummy on this host: adb connect 127.0.0.1:15555, WDA 127.0.0.1:18100
	bin/mcloud-dummy -android 1 -ios 1 -host 127.0.0.1

e2e: ## the Appium sessions of both platforms through a grid: make e2e HUB=https://<mcloud>/mcloud/wd/hub
	python3 tests/e2e.py "$(HUB)" all

contract: ## the fake UiAutomator2 server against the recordings of the real one: make contract UIA2=http://<host>:<port>/wd/hub
	python3 tests/contract/uia2_contract.py verify "$(UIA2)" tests/contract/fixtures/uia2-*.json
