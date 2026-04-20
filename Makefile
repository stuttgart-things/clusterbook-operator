# clusterbook-operator
#
# Typical flow:
#   make deps           # go mod tidy
#   make generate       # zz_generated.deepcopy.go + CRD manifests
#   make build          # compile binary
#   make test           # unit tests
#   make docker-build   # container image

IMG ?= ghcr.io/stuttgart-things/clusterbook-operator:dev
CRD_DIR := config/crd

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

.PHONY: test
test:
	go test ./...

.PHONY: run
run:
	go run ./cmd

.PHONY: docker-build
docker-build:
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push:
	docker push $(IMG)
