"""
Main entrypoint — renders all clusterbook-operator deployment manifests.
"""

import namespace
import serviceaccount
import rbac
import deploy

manifests = [
    m for m in [
        namespace.namespace
        serviceaccount.serviceAccount
        rbac.clusterRole
        rbac.clusterRoleBinding
        deploy.deployment
    ] if m
]
