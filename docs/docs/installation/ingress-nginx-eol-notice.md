---
title: Why we don't ship ingress-nginx examples
sidebar_position: 99
---

# Why we don't ship ingress-nginx examples

KubeAtlas v0.1.0 ships example values for **F5 NGINX Ingress
Controller** (`nginxinc/kubernetes-ingress`), **Traefik**, and **AWS
ALB** — but not for the **community** `kubernetes/ingress-nginx`
project.

That community project announced retirement in late 2025; from
**March 2026** it stops receiving new releases, bug fixes, and CVE
patches ([Kubernetes blog](https://kubernetes.io/blog/2025/11/11/ingress-nginx-retirement/)).
Pointing v0.1.0 users at it would point them at a dead-end.

KubeAtlas itself doesn't care about your Ingress flavour — the
chart just needs an `ingressClassName` that maps to a controller
your cluster runs. Anything that implements the standard
`networking.k8s.io/v1` `Ingress` resource works. Pick whatever fits
your environment:

- **F5 NGINX (`nginxinc/kubernetes-ingress`)** — actively
  maintained NGINX-based controller from F5/NGINX Inc. Annotation
  prefix is `nginx.org/*`. See
  [`helm/kubeatlas/examples/ingress-nginx-f5.yaml`](https://github.com/lithastra/kubeatlas/blob/main/helm/kubeatlas/examples/ingress-nginx-f5.yaml).
- **Traefik** — what the project's PetClinic test fixture uses.
  See [`helm/kubeatlas/examples/ingress-traefik.yaml`](https://github.com/lithastra/kubeatlas/blob/main/helm/kubeatlas/examples/ingress-traefik.yaml).
- **AWS ALB** — the `aws-load-balancer-controller` if you're on
  EKS. See
  [`helm/kubeatlas/examples/ingress-alb.yaml`](https://github.com/lithastra/kubeatlas/blob/main/helm/kubeatlas/examples/ingress-alb.yaml).

If you currently run the community `kubernetes/ingress-nginx`
controller, KubeAtlas still works against it — the chart's
`ingressClassName` value points at whatever controller answers it.
You'll want a migration plan before the project's EOL date so your
production stack isn't left without CVE coverage.
