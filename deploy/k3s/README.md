# deploy/k3s — edge appliance (MVP)

Kustomize base for running OpenDesk core services on a single-node **k3s**
edge appliance (a salon/clinic back-office box) with Kafka MirrorMaker2
store-and-forward to the central cluster.

## Honest scope (what this is / is not)

**Is:**

- A working kustomize base: `namespace` + **3 representative Deployments**
  (`identity`, `booking`, `notification`) with Services, probes, and the
  same env wiring as the compose stack.
- Image names follow the compose build convention (`opendesk-<service>`,
  project name `opendesk` + service name). Build on the appliance
  (`docker build` + `k3s ctr images import` or `kind load`), or retag and
  push to a registry the appliance can reach.
- A **kind config** to rehearse the appliance locally, and a **MirrorMaker2
  skeleton** (Strimzi CR) for edge→central replication of
  `opendesk.transcripts-raw`.

**Is not (yet):**

- The remaining 7 app services are **not** included — they follow the exact
  same Deployment+Service pattern as the three here; add them as they are
  validated on the appliance (voice-agent-runtime additionally needs the
  `voice` profile model volumes/Ollama story decided).
- **Middleware is not in this base.** The manifests reference in-cluster
  DNS names (`postgres`, `kafka`, `redis`, `temporal`, `permify`,
  `keycloak`) — the MVP assumes either (a) a minimal middleware set deployed
  alongside (single-replica Postgres with the **local-path provisioner** —
  k3s ships it as the default StorageClass; no extra setup needed), or
  (b) tunnelled access back to the central cluster's middleware. Option (a)
  with local-path volumes is the intended appliance default; the middleware
  manifests are a follow-up.
- **No dapr sidecars.** The compose stack injects `daprd` per service; the
  edge MVP runs services directly (same ports/env). Service invocation that
  goes through Dapr in compose must point at direct service DNS on the
  appliance — this is the main functional gap to close before the appliance
  is real. Stated plainly: **today this base proves the services run on
  k3s; the full saga/Dapr dataplane still expects the compose middleware.**
- MM2 needs the Strimzi operator installed and a real central endpoint
  (see `mirror-maker2.yaml` comments). It has not been run end-to-end.

## Usage

```bash
# Rehearse locally with kind:
kind create cluster --config deploy/k3s/kind-config.yaml --name opendesk-edge
kubectl apply -k deploy/k3s/
kubectl -n opendesk get pods

# On a real k3s appliance:
kubectl apply -k deploy/k3s/
```

Images must exist in the appliance's containerd before apply, e.g.:

```bash
docker build -t opendesk-booking:latest services/booking-service
docker save opendesk-booking:latest | sudo k3s ctr images import -
# (repeat per service; with kind: `kind load docker-image opendesk-booking:latest --name opendesk-edge`)
```

## Files

| File | Purpose |
| --- | --- |
| `namespace.yaml` | `opendesk` namespace |
| `identity-deployment.yaml` | identity-service Deployment+Service (:7001) |
| `booking-deployment.yaml` | booking-service Deployment+Service (:7002) |
| `notification-deployment.yaml` | notification-worker Deployment+Service (:7003) |
| `kustomization.yaml` | base wiring all of the above + MM2 |
| `kind-config.yaml` | local rehearsal cluster (gateway :9080, grafana :3002 NodePorts) |
| `mirror-maker2.yaml` | Strimzi KafkaMirrorMaker2 edge→central skeleton |
