# KubeAtlas

KubeAtlas is a Kubernetes resource dependency graph tool. Currently in PoC stage.

## Build & run

Requires Go 1.22+ and a reachable Kubernetes cluster (kubeconfig at `~/.kube/config` or pointed to by `$KUBECONFIG`).

```sh
go run ./cmd/kubeatlas/ > output/kubeatlas.json
dot -Tsvg output/kubeatlas.dot -o output/kubeatlas.svg
```

## Dependency chain discovered by the PoC

```
Ingress    --backend-----> Service --selector--> Deployment --configMapRef--> ConfigMap
HTTPRoute  --backendRef--> Service                          \--secretRef----> Secret
HTTPRoute  --parentRef---> Gateway
```
