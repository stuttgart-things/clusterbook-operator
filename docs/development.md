# Development

## Setup

```bash
git clone https://github.com/stuttgart-things/clusterbook-operator.git
cd clusterbook-operator
make deps
```

Go 1.26+ toolchain is pinned by `go.mod` (`toolchain go1.26.2`).

## Typical flow

| Target | What it does |
|---|---|
| `make deps` | `go mod tidy` |
| `make generate` | `zz_generated.deepcopy.go` + CRD manifests under `kcl/crds/` |
| `make build` | Compile `bin/manager` |
| `make test` | Auto-install `setup-envtest`, download k8s binaries, run `go test -race ./...` |
| `make run` | Run the controller against your current kubecontext |
| `make docker-build` | Build the runtime image |

## Running tests

Unit tests (`pkg/client`) and integration tests (`controller/`) live side by side. The controller tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) to stand up an in-process API server with the CRDs from `kcl/crds/`, and an `httptest`-based fake clusterbook.

```bash
make test
```

First run downloads ~50MB of envtest binaries into `~/.local/share/kubebuilder-envtest`; subsequent runs are instant.

## Running the controller locally

```bash
# Needs a valid KUBECONFIG and a ClusterbookProviderConfig + CRDs on the cluster
make run
```

## KCL rendering

```bash
kcl run kcl/
```

Produces 7 manifests: 2 CRDs, Namespace, SA, ClusterRole, ClusterRoleBinding, Deployment.

Override via `-D config.<field>=<value>`:

```bash
kcl run kcl/ \
  -D config.image=ghcr.io/stuttgart-things/clusterbook-operator:dev \
  -D config.replicas=2
```

## Release process

All releases are driven by [semantic-release](https://semantic-release.gitbook.io/) using the Angular commit convention. The Release workflow (`.github/workflows/release.yaml`) triggers after every successful `CI - Build & Test` run on `main` and:

1. Runs `semantic-release` — analyzes commits since the last tag, cuts a new tag if anything qualifies
2. If a new tag is cut: builds + pushes `ghcr.io/stuttgart-things/clusterbook-operator:v<version>` and `:latest`
3. Renders KCL and pushes `ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v<version>` as an OCI artifact
4. Appends an Artifacts table to the GitHub Release notes

Conventional commit prefixes that trigger a bump:

| Prefix | Bump |
|---|---|
| `fix:` | patch (`v0.6.0 → v0.6.1`) |
| `feat:` | minor (`v0.6.0 → v0.7.0`) |
| `feat!:` or `BREAKING CHANGE:` in body | major |
| `chore:` `ci:` `docs:` `test:` `refactor:` | no release |

## Repo layout

```
api/v1alpha1/          # CRD types + generated deepcopy
cmd/                   # manager entrypoint
controller/            # reconciler + integration tests
pkg/client/            # clusterbook REST client (copied from xplane-provider-clusterbook)
cluster/images/        # Dockerfile
kcl/                   # deployment module (CRDs live in kcl/crds/)
tests/                 # kcl-deploy-profile.yaml
docs/                  # this site
.github/workflows/     # CI, release, pages
```
