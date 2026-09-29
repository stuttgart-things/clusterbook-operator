[package]
name = "deploy-clusterbook-operator"
version = "0.1.0"
description = "KCL module for deploying clusterbook-operator on Kubernetes"

[dependencies]
k8s = "1.36"

[profile]
entries = ["main.k"]
