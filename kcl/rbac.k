"""
ClusterRole + ClusterRoleBinding for clusterbook-operator.

The controller needs to:
- watch/update ClusterbookCluster CRs (cluster-scoped)
- read ClusterbookProviderConfig CRs
- read Secrets (kubeconfig + optional custom CA) across namespaces
- create/update/delete Secrets (ArgoCD cluster secrets)
- emit Events
- hold leader-election Leases
"""

import labels

config = labels.config

clusterRole = {
    apiVersion: "rbac.authorization.k8s.io/v1"
    kind: "ClusterRole"
    metadata: {
        name: config.name
        labels: labels.commonLabels
    }
    rules: [
        {
            apiGroups: ["clusterbook.stuttgart-things.com"]
            resources: ["clusterbookclusters"]
            verbs: ["get", "list", "watch", "update", "patch"]
        }
        {
            apiGroups: ["clusterbook.stuttgart-things.com"]
            resources: ["clusterbookproviderconfigs"]
            verbs: ["get", "list", "watch"]
        }
        {
            apiGroups: ["clusterbook.stuttgart-things.com"]
            resources: ["clusterbookclusters/status"]
            verbs: ["get", "update", "patch"]
        }
        {
            apiGroups: ["clusterbook.stuttgart-things.com"]
            resources: ["clusterbookclusters/finalizers"]
            verbs: ["update"]
        }
        {
            apiGroups: [""]
            resources: ["secrets"]
            verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
        }
        {
            apiGroups: [""]
            resources: ["events"]
            verbs: ["create", "patch"]
        }
        {
            apiGroups: ["coordination.k8s.io"]
            resources: ["leases"]
            verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
        }
    ]
}

clusterRoleBinding = {
    apiVersion: "rbac.authorization.k8s.io/v1"
    kind: "ClusterRoleBinding"
    metadata: {
        name: config.name
        labels: labels.commonLabels
    }
    roleRef: {
        apiGroup: "rbac.authorization.k8s.io"
        kind: "ClusterRole"
        name: config.name
    }
    subjects: [
        {
            kind: "ServiceAccount"
            name: config.name
            namespace: config.namespace
        }
    ]
}
