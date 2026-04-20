module github.com/stuttgart-things/clusterbook-operator

go 1.24.0

toolchain go1.24.5

tool sigs.k8s.io/controller-tools/cmd/controller-gen

require (
	k8s.io/api v0.33.3
	k8s.io/apimachinery v0.33.3
	k8s.io/client-go v0.33.3
	sigs.k8s.io/controller-runtime v0.21.0
)
