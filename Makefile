VERSION ?= dev
GO ?= go
IMAGE ?= ghcr.io/danielskowronski/qiotlctl
CHART_VERSION ?= $(shell awk '/^version:/ {print $$2}' charts/qiotlctl/Chart.yaml)
APP_VERSION ?= $(VERSION)
CHART_REGISTRY ?= oci://ghcr.io/danielskowronski/charts
export VERSION IMAGE CHART_VERSION APP_VERSION CHART_REGISTRY
.PHONY: build test race vet check image image-archive image-push chart-check chart-package chart-push k8s-smoke
build:
	$(GO) build -trimpath -ldflags '-X main.version=$(VERSION)' -o bin/qiotlctl ./cmd/qiotlctl
	$(GO) build -trimpath -ldflags '-X main.version=$(VERSION)' -o bin/qiotld ./cmd/qiotld
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
check: test vet release-test
image:
	bash scripts/image.sh local
image-archive:
	bash scripts/image.sh archive
# Publication is explicit; no build/check/package target depends on these.
image-push:
	bash scripts/image.sh push
chart-check:
	helm lint charts/qiotlctl --strict
	$(GO) test ./tools/charttest -count=1
chart-package: chart-check
	mkdir -p dist
	helm package charts/qiotlctl --destination dist --version '$(CHART_VERSION)' --app-version '$(APP_VERSION)'
	cd dist && shasum -a 256 'qiotlctl-$(CHART_VERSION).tgz' > 'qiotlctl-$(CHART_VERSION).tgz.sha256'
chart-push: release-check chart-package
	helm push 'dist/qiotlctl-$(CHART_VERSION).tgz' '$(CHART_REGISTRY)'
k8s-smoke:
	bash scripts/k8s-smoke.sh

.PHONY: release-check release-local
# This guard performs no publication. Local candidates may come from a dirty checkout.
release-check:
	bash scripts/release-check.sh
release-local:
	bash scripts/release-local.sh

.PHONY: release-test
release-test:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools/releasetest -v
