"""
Main entrypoint — renders all clusterbook-operator deployment manifests.

Includes the generated CRD manifests from kcl/crds/ so the kustomize OCI
bundle produced at release time is self-contained: a single
`kubectl apply -k` installs CRDs, Deployment, RBAC — everything.
"""

import file
import yaml

import namespace
import serviceaccount
import rbac
import deploy

_mod = file.modpath()
_crdFiles = [
    _mod + "/crds/clusterbook.stuttgart-things.com_clusterbookallocations.yaml"
    _mod + "/crds/clusterbook.stuttgart-things.com_clusterbookclusters.yaml"
    _mod + "/crds/clusterbook.stuttgart-things.com_clusterbookloadbalancers.yaml"
    _mod + "/crds/clusterbook.stuttgart-things.com_clusterbookproviderconfigs.yaml"
]
_crds = [yaml.decode(file.read(f)) for f in _crdFiles]

manifests = _crds + [
    m for m in [
        namespace.namespace
        serviceaccount.serviceAccount
        rbac.clusterRole
        rbac.clusterRoleBinding
        deploy.deployment
    ] if m
]
