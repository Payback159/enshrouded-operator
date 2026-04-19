# enshrouded-operator

A Kubernetes operator for managing [Enshrouded](https://enshrouded.com) dedicated game servers as first-class Kubernetes resources.

## Features

- **Declarative server management** — define your Enshrouded server with a single `EnshroudedServer` custom resource
- **Automatic StatefulSet + Service** — the operator manages the underlying Kubernetes objects
- **Persistent storage** — savegame data is stored on a PVC; optional `retainOnDelete` protects data across CR deletions
- **Per-group passwords** — `userGroups` lets you configure multiple access groups with individual passwords and permissions
- **DeferWhilePlaying** — pending updates are held back while players are connected; optional `maintenanceWindows` (cron) override this
- **Backup** — VolumeSnapshot (snapshot-based) and S3/rclone sidecar backup strategies
- **Metrics sidecar** — optional Prometheus sidecar that queries the server via the Steam A2S protocol and exposes `enshrouded_server_up`, `enshrouded_active_players`, `enshrouded_max_players`, and `enshrouded_server_info`
- **Webhook validation** — admission webhook prevents invalid configurations from reaching the cluster

## Custom Resource

```yaml
apiVersion: enshrouded.enshrouded.io/v1alpha1
kind: EnshroudedServer
metadata:
  name: my-server
spec:
  serverName: "My Enshrouded Server"
  port: 15637        # UDP query port
  steamPort: 27015   # UDP Steam port
  serverSlots: 16

  # Optional: container image override
  image:
    repository: sknnr/enshrouded-dedicated-server
    tag: latest

  # Optional: per-group passwords (switches server to external-config mode)
  userGroups:
    - name: Admin
      passwordSecretRef:
        name: enshrouded-passwords
        key: admin-password
      canKickBan: true
      canEditWorld: true
      canEditBase: true
      canExtendBase: true
      reservedSlots: 2
    - name: Friend
      passwordSecretRef:
        name: enshrouded-passwords
        key: friend-password

  # Optional: defer updates while players are online
  updatePolicy:
    deferWhilePlaying: true
    maintenanceWindows:
      - "TZ=Europe/Berlin 0 3 * * 1-5"  # Mon–Fri 03:00 Berlin time

  # Optional: persistent storage
  storage:
    size: 20Gi
    retainOnDelete: true

  # Optional: S3 backup via rclone sidecar
  backup:
    s3:
      bucketURL: "s3://my-bucket/enshrouded/my-server"
      credentialsSecretRef:
        name: s3-credentials
      schedule: "*/30 * * * *"

  # Optional: Prometheus metrics sidecar
  metricsSidecar:
    enabled: true
    image: ghcr.io/payback159/enshrouded-metrics-sidecar:latest
    metricsPort: 9090
    scrapeIntervalSeconds: 15
```

### Status fields

| Field | Description |
|---|---|
| `status.phase` | `Pending` / `Running` / `Updating` / `Error` |
| `status.readyReplicas` | Number of ready StatefulSet replicas |
| `status.activePlayers` | Live player count (written by the metrics sidecar) |
| `status.updateDeferred` | `true` when an update is held back by `deferWhilePlaying` |
| `status.conditions` | Standard `metav1.Condition` list (e.g. `Ready`, `UpdateDeferred`) |

## Metrics sidecar

When `metricsSidecar.enabled: true`, the operator injects a sidecar container and creates:
- `ServiceAccount` `<name>-metrics` — scoped to the namespace
- `Role` + `RoleBinding` — allows the sidecar to patch the CR status

Exposed Prometheus metrics:

| Metric | Type | Description |
|---|---|---|
| `enshrouded_server_up` | Gauge | 1 if the server responds to A2S queries |
| `enshrouded_active_players` | Gauge | Number of connected players |
| `enshrouded_max_players` | Gauge | Configured player slot limit |
| `enshrouded_server_info` | Gauge | Always 1 when up; labels carry `version` and `map` |

## Getting Started

### Prerequisites

- Go v1.24+
- Docker
- kubectl
- Access to a Kubernetes v1.21+ cluster
- [cert-manager](https://cert-manager.io) (for webhook TLS)

### Deploy to a cluster

```sh
# Build and push operator image
export IMG=ghcr.io/payback159/enshrouded-operator:latest
make docker-build docker-push IMG=$IMG

# Build and push sidecar image (if using metricsSidecar)
export SIDECAR_IMG=ghcr.io/payback159/enshrouded-metrics-sidecar:latest
docker build -f Dockerfile --target metrics-sidecar -t $SIDECAR_IMG .
docker push $SIDECAR_IMG

# Install CRDs
make install

# Deploy the operator
make deploy IMG=$IMG

# Apply a sample CR
kubectl apply -k config/samples/
```

### Run locally (for development)

```sh
make install   # Install CRDs into current kubeconfig context
make run       # Run operator locally against the cluster
```

### Uninstall

```sh
kubectl delete -k config/samples/
make undeploy
make uninstall
```

## Development

```sh
make manifests   # Regenerate CRDs and RBAC from kubebuilder markers
make generate    # Regenerate DeepCopy methods
make lint-fix    # Auto-fix code style
make test        # Run unit tests (uses envtest)
make test-e2e    # Run e2e tests against a Kind cluster
```

## Integrations & Automation

The CR status exposes enough information to build lightweight integrations
without any additional tooling installed in-cluster.

### Example: Discord update notifications via a shell script

The idea: poll the CR status with `kubectl`, compare `status.phase` and
`status.gameVersion` against a cached value, and POST a Discord webhook
message whenever something changes.

```bash
#!/usr/bin/env bash
# notify-discord.sh — run this from a cron job or a simple loop
set -euo pipefail

CR_NAME="my-server"
CR_NS="default"
DISCORD_WEBHOOK_URL="https://discord.com/api/webhooks/<id>/<token>"
CACHE_FILE="/tmp/enshrouded-status.cache"

# Read current values from the CR.
STATUS=$(kubectl get enshroudedserver "$CR_NAME" -n "$CR_NS" \
  -o jsonpath='{.status.phase}|{.status.gameVersion}|{.status.activePlayers}')

if [[ "$STATUS" == "$(cat "$CACHE_FILE" 2>/dev/null)" ]]; then
  exit 0  # Nothing changed.
fi

PHASE=$(cut -d'|' -f1 <<< "$STATUS")
VERSION=$(cut -d'|' -f2 <<< "$STATUS")
PLAYERS=$(cut -d'|' -f3 <<< "$STATUS")

curl -s -X POST "$DISCORD_WEBHOOK_URL" \
  -H "Content-Type: application/json" \
  -d "{\"content\": \"**Enshrouded** · phase: \`$PHASE\` · version: \`${VERSION:-unknown}\` · players: \`$PLAYERS\`\"}"

echo "$STATUS" > "$CACHE_FILE"
```

The same approach works with any language that can call `kubectl` or the
Kubernetes API directly (e.g. the official Python/Go clients).  A more
production-grade setup would use a `Deployment` with a controller-runtime
informer that watches `EnshroudedServer` objects and reacts to
`.status.conditions` changes — but the script above is sufficient for most
home-lab scenarios.

**Relevant status fields for this use-case:**

| Field | Example value | When to act |
|---|---|---|
| `status.phase` | `Updating` → `Running` | Update completed |
| `status.gameVersion` | `1.0.7.4` | New version deployed |
| `status.updateDeferred` | `true` | Update waiting for players to leave |
| `status.activePlayers` | `3` | Players still connected |

## Distribution

### YAML bundle

```sh
make build-installer IMG=ghcr.io/payback159/enshrouded-operator:latest
# Produces dist/install.yaml — apply with:
kubectl apply -f dist/install.yaml
```

### Helm chart

```sh
kubebuilder edit --plugins=helm/v2-alpha
helm install enshrouded-operator ./dist/chart/ --namespace enshrouded-operator-system --create-namespace
```

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

