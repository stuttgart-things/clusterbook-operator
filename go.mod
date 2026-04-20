module github.com/stuttgart-things/clusterbook-operator

go 1.24.0

toolchain go1.26.2

tool sigs.k8s.io/controller-tools/cmd/controller-gen

require (
	k8s.io/api v0.35.4
	k8s.io/apimachinery v0.35.4
	k8s.io/client-go v0.35.4
	sigs.k8s.io/controller-runtime v0.21.0
)
