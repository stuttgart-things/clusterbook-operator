"""
Deployment for clusterbook-operator.
"""

import labels

config = labels.config

deployment = {
    apiVersion: "apps/v1"
    kind: "Deployment"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
    }
    spec: {
        replicas: config.replicas
        selector: {
            matchLabels: labels.selectorLabels
        }
        strategy: {
            type: "RollingUpdate"
            rollingUpdate: {
                maxSurge: 1
                maxUnavailable: 0
            }
        }
        template: {
            metadata: {
                labels: labels.commonLabels
                annotations: config.annotations
            }
            spec: {
                serviceAccountName: config.name
                containers: [
                    {
                        name: "manager"
                        image: config.image
                        imagePullPolicy: config.imagePullPolicy
                        args: [
                            "--metrics-bind-address=:{}".format(config.metricsPort)
                            "--health-probe-bind-address=:{}".format(config.healthPort)
                        ]
                        env: [
                            {
                                name: "SSL_CERT_DIR"
                                value: "/etc/ssl/custom"
                            }
                        ] if config.trustBundleConfigMap else []
                        ports: [
                            {
                                name: "metrics"
                                containerPort: config.metricsPort
                                protocol: "TCP"
                            }
                            {
                                name: "health"
                                containerPort: config.healthPort
                                protocol: "TCP"
                            }
                        ]
                        resources: {
                            requests: {
                                cpu: config.cpuRequest
                                memory: config.memoryRequest
                            }
                            limits: {
                                cpu: config.cpuLimit
                                memory: config.memoryLimit
                            }
                        }
                        livenessProbe: {
                            httpGet: {
                                path: "/healthz"
                                port: "health"
                            }
                            initialDelaySeconds: 15
                            periodSeconds: 20
                        }
                        readinessProbe: {
                            httpGet: {
                                path: "/readyz"
                                port: "health"
                            }
                            initialDelaySeconds: 5
                            periodSeconds: 10
                        }
                        securityContext: {
                            readOnlyRootFilesystem: True
                            runAsNonRoot: True
                            allowPrivilegeEscalation: False
                            capabilities: {
                                drop: ["ALL"]
                            }
                        }
                        volumeMounts: [
                            {
                                name: "trust-bundle"
                                mountPath: "/etc/ssl/custom/trust-bundle.pem"
                                subPath: "trust-bundle.pem"
                                readOnly: True
                            }
                        ] if config.trustBundleConfigMap else []
                    }
                ]
                securityContext: {
                    runAsNonRoot: True
                    runAsUser: 65532
                    runAsGroup: 65532
                    fsGroup: 65532
                    seccompProfile: {
                        type: "RuntimeDefault"
                    }
                }
                volumes: [
                    {
                        name: "trust-bundle"
                        configMap: {
                            name: config.trustBundleConfigMap
                            optional: True
                        }
                    }
                ] if config.trustBundleConfigMap else []
                affinity: {
                    podAntiAffinity: {
                        preferredDuringSchedulingIgnoredDuringExecution: [
                            {
                                weight: 100
                                podAffinityTerm: {
                                    labelSelector: {
                                        matchLabels: labels.selectorLabels
                                    }
                                    topologyKey: "kubernetes.io/hostname"
                                }
                            }
                        ]
                    }
                }
            }
        }
    }
}
