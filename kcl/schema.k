"""
Schema definition for clusterbook-operator deployment configuration.
"""

schema ClusterbookOperator:
    """Configuration for clusterbook-operator Kubernetes deployment."""

    # Metadata
    name: str = "clusterbook-operator"
    namespace: str = "clusterbook-system"

    # Image
    image: str = "ghcr.io/stuttgart-things/clusterbook-operator:latest"
    imagePullPolicy: str = "IfNotPresent"

    # Replicas and resources
    replicas: int = 1
    cpuRequest: str = "50m"
    cpuLimit: str = "500m"
    memoryRequest: str = "64Mi"
    memoryLimit: str = "256Mi"

    # Ports
    metricsPort: int = 8080
    healthPort: int = 8081

    # Labels and annotations
    labels: {str:str} = {}
    annotations: {str:str} = {}

    check:
        replicas >= 0, "replicas must be >= 0"
        name != "", "name must not be empty"
        namespace != "", "namespace must not be empty"
