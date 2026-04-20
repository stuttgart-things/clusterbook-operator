# clusterbook-operator
#
# Typical flow:
#   make deps           # go mod tidy
#   make generate       # zz_generated.deepcopy.go + CRD manifests
#   make build          # compile binary
#   make test           # unit tests
#   make docker-build   # container image

IMG ?= ghcr.io/stuttgart-things/clusterbook-operator:dev
CRD_DIR := kcl/crds
ENVTEST_K8S_VERSION ?= 1.31.0

.PHONY: deps
deps:
	go mod tidy

.PHONY: generate
generate:
	go tool controller-gen object paths="./api/..."
	go tool controller-gen crd paths="./api/..." output:crd:artifacts:config=$(CRD_DIR)

.PHONY: build
build:
	CGO_ENABLED=0 go build -o bin/manager ./cmd

.PHONY: envtest
envtest:
	@command -v setup-envtest >/dev/null 2>&1 || go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: test
test: envtest
	KUBEBUILDER_ASSETS="$$(setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" go test -race ./...

.PHONY: run
run:
	go run ./cmd

.PHONY: docker-build
docker-build:
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push:
	docker push $(IMG)
