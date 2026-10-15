# How to run E2E Tests

This document covers running E2E tests locally on KinD/xKS and OpenShift. The OpenShift
suite includes the DAG ordering and registered-module contract; xKS contract coverage is
deferred. For CI-triggered integration tests via Jenkins, see
[integration-testing.md](integration-testing.md).

## When E2E Tests are Required

E2E tests are **MANDATORY** for pull requests that modify:

- **Component Controllers** (`internal/controller/components/` changes)
- **Service Controllers** (`internal/controller/services/` changes)
- **API Definitions** (`api/` directory changes)
- **Webhook Implementations** (`internal/webhook/` changes)
- **Operator Configuration** (`config/` directory changes)

E2E tests are **RECOMMENDED** for:
- Multi-component feature implementations
- Reconciliation logic changes
- Manifest or kustomize overlay updates

## Prerequisites

- **Go** (version matching `go.mod`)
- **Podman** or **Docker**
- **kubectl**
- **Red Hat registry credentials** (for `registry.redhat.io` images) — `podman login registry.redhat.io`

For KinD tests additionally:
- **KinD** (`go install sigs.k8s.io/kind@latest`)

For OpenShift tests additionally:
- **OpenShift cluster** with `oc` CLI configured
- **Dependent operators** installed (OpenShift Service Mesh, Serverless, etc.)

The DAG/module-contract suite idempotently installs the OpenShift cert-manager, JobSet, and Red Hat Connectivity Link operators through OLM when they are missing. It also waits for cert-manager's `Issuer` and `Certificate` CRDs and creates the cluster-scoped `JobSetOperator` resource. Other module dependencies remain cluster prerequisites.

## Understanding the Test Suites

The E2E test suite is organized into test groups that run sequentially:

- **`make e2e-test-xks`** — Gateway-focused tests on KinD / vanilla Kubernetes.
  This is the CI-equivalent E2E for KinD (see `.github/workflows/test-kind-odh-e2e.yaml`).
  The target explicitly sets `E2E_TEST_PLATFORM_ORCHESTRATION=false`; the xKS module contract is
  deferred and is not implied by this target.

- **`make e2e-test`** — Full OpenShift suite across DSC/DSCI lifecycle, the DAG
  ordering and module contract, Auth/Gateway services, webhooks, and operator resilience.

Legacy per-component suites and files no longer exist. Registered modules are discovered from
production registrations, ordered by the production orchestration DAG, and checked individually as child tests
within `Platform Orchestration E2E Tests`. The complete contract is controlled by
`E2E_TEST_PLATFORM_ORCHESTRATION` or `--test-platform-orchestration`; there is no per-module selection flag.

For each registered module, the contract verifies:

- the module CR is owned by its configuration source (`DataScienceCluster`, or
  `DSCInitialization` for Monitoring);
- `Ready=True`, current `observedGeneration`, a per-module ConfigMap version matching the Platform resource, and a module CR `platform` release matching that ConfigMap;
- mandatory `Degraded=False` and `ProvisioningSucceeded=True` conformance, reported in the final `MODULE_CONTRACT_REPORT` without failing platform-owned coverage;
- an available module-operator Deployment with `APPLICATIONS_NAMESPACE` and every
  handler-declared `RELATED_IMAGE_*` that is configured on the platform operator injected with the same value; and
- readiness propagation to DSC status, or DSCI readiness and `MonitoringReady=True` for Monitoring. Platform release is validated on the authoritative module CR rather than DSC status.

### Conversion webhook tests

The OpenShift suite also preserves dedicated CRD conversion coverage. These tests exercise
v2↔v3 conversion for the DSC and Platform APIs, including KServe/WVA compatibility, rather
than module lifecycle behavior. They are controlled independently from the platform orchestration contract:

- `E2E_TEST_CONVERSION_WEBHOOK` / `--test-conversion-webhook`
- `E2E_TEST_CONVERSION_WEBHOOK_DSC` / `--test-conversion-webhook-dsc`
- `E2E_TEST_CONVERSION_WEBHOOK_PLATFORM` / `--test-conversion-webhook-platform`

The conversion scenarios run after the functional suites because they temporarily use the
singleton DSC. They are skipped on xKS clusters.

The operator's default DAG runlevel timeout is 10 minutes and is configurable with
`ODH_DAG_RUNLEVEL_TIMEOUT` or `--dag-runlevel-timeout`. The OpenShift DAG E2E temporarily
sets it to 5 minutes for upgrade-order checks. Initial bootstrap is intentionally ungated
when a module has not yet reported a platform release; runlevel ordering is validated during
a simulated platform-version upgrade.

## KinD E2E (KServe and Gateway)

### Step-by-step guide

1. **Create the KinD cluster**: `make kind-create`
   - Creates a 2-node cluster (1 control-plane + 1 worker) named `kind-odh` using
     `config/kind/kind-config.yaml`

2. **Build the operator image**: `IMG=localhost/odh-operator:e2e make image-build`
   - Always specify `IMG=localhost/odh-operator:e2e` to avoid conflicts with the
     default image tag
   - The Makefile defaults to `podman` as the image builder. If using Docker, set
     `IMAGE_BUILDER=docker`

3. **Load the image into KinD**: `make image-kind-load IMG=localhost/odh-operator:e2e KIND_CLUSTER_NAME=kind-odh`

4. **Create operator namespace**: `kubectl create namespace opendatahub-operator-system`

5. **Setup pull secrets** (required for `registry.redhat.io` images):
   `make kind-setup-pull-secrets PULL_SECRET=$XDG_RUNTIME_DIR/containers/auth.json`
   - This creates `rhai-pull-secret` in the namespaces: `cert-manager`, `cert-manager-operator`,
     `openshift-lws-operator`, `istio-system`
   - Alternative paths: `~/.docker/config.json` or `$HOME/.config/containers/auth.json`

6. **Deploy Cloud Manager** (installs cert-manager, Gateway API, LWS, Sail/Istio via Helm):
   `IMG=localhost/odh-operator:e2e make deploy-ccm-local-azure`

7. **Wait for Cloud Manager, then deploy AzureKubernetesEngine CR**:
   ```bash
   kubectl rollout status deployment -n opendatahub-cloudmanager-system \
     -l control-plane=controller-manager --timeout=180s
   kubectl apply -f config/cloudmanager/azure/samples/azurekubernetesengine_v1alpha1.yaml
   kubectl wait --for=condition=Ready azurekubernetesengine/default-azurekubernetesengine --timeout=300s
   ```

8. **Deploy operator** (rhaii-local, KServe + gateway): `IMG=localhost/odh-operator:e2e make deploy-rhaii-local`
   - Gateway service reconciliation is enabled (`RHAI_DISABLE_GATEWAY_SERVICE=false` in the rhaii overlay)

9. **Wait for operator**:
   ```bash
   kubectl wait --for=condition=Available deployment/opendatahub-operator-controller-manager \
     -n opendatahub-operator-system --timeout=600s
   ```

10. **Run E2E tests**: `make e2e-test-xks`
    - The target runs Gateway service coverage and disables the platform orchestration/module contract because the xKS contract is deferred
    - Tests bootstrap `Platform`, Dex, and `GatewayConfig` (with OIDC) automatically — see [Dex OIDC provider](#dex-oidc-provider-kind-xks-gateway) below
    - Production xKS installs create `GatewayConfig` via the `xks-gateway` Helm subchart in odh-gitops instead

11. **Cleanup**: `make kind-delete`

### Dex OIDC provider (KinD xKS gateway)

KinD gateway e2e tests deploy a minimal [Dex](https://dexidp.io/) instance so
`kube-auth-proxy` can complete OIDC discovery (`--skip-oidc-discovery=false`).

#### Automatic bootstrap (recommended)

`make e2e-test-xks` calls `EnsureGatewayConfigForXKS`, which:

1. Deploys Dex in `dex-system` (issuer: `https://dex.dex-system.svc.cluster.local:5556/dex`)
2. Creates `GatewayConfig` with matching OIDC client (`odh-gateway`) and `verifyProviderCertificate: false`

This complements step 10 above: tests bootstrap `Platform`, Dex, and `GatewayConfig` automatically on KinD. Production xKS installs create `GatewayConfig` via the `xks-gateway` Helm subchart in odh-gitops instead.

#### Manual setup

If you run gateway e2e outside the test bootstrap, ensure Dex is up first:

```bash
# Dex is created automatically when tests start; to pre-provision manually, run e2e once or
# delete/recreate GatewayConfig after wiping the cluster:
kubectl delete gatewayconfig default-gateway --ignore-not-found
kubectl delete ns dex-system --ignore-not-found
make e2e-test-xks E2E_TEST_SERVICE=gateway
```

#### Notes

- **Issuer URL** uses in-cluster DNS so `kube-auth-proxy` pods can reach Dex without CoreDNS hacks.
- **Dex config** uses a `mockCallback` connector because Dex v2.41+ requires at least one connector at startup.
- **Redirect URI** is `https://rh-ai.kind.local/oauth2/callback` (gateway hostname + OAuth callback path).
- **arm64 Mac**: Dex is multi-arch, but `odh-kube-auth-proxy` is still amd64-only; deployment readiness may fail locally even with Dex running.

## OpenShift DAG Contract on KinD

The OpenShift platform orchestration/module contract is not currently supported on KinD or other xKS
clusters. The previous manual command for a broader per-component KinD suite depended on
legacy component test files that have been removed. Use `make e2e-test` on OpenShift for
registered-module contract coverage; `make e2e-test-xks` intentionally disables it with
`E2E_TEST_PLATFORM_ORCHESTRATION=false`.

## Full E2E on OpenShift

### Step-by-step guide

1. **Build and push operator image**: `IMG=quay.io/<your-repo>/opendatahub-operator:dev make image-build image-push`

2. **Deploy the operator**: `IMG=quay.io/<your-repo>/opendatahub-operator:dev make deploy`

3. **Wait for operator**:
   ```bash
   oc wait --for=condition=Available deployment/opendatahub-operator-controller-manager \
     -n opendatahub-operator-system --timeout=600s
   ```

4. **Run full E2E tests**: `make e2e-test`

5. **Run a single module contract path** (useful for debugging):
   `make e2e-test-single TEST="TestOdhOperator/Platform_Orchestration_E2E_Tests/Validate_runlevel_gating_and_convergence/Runlevel_20_module_contracts/dashboard"`

### Running a registered module contract path

There is no per-module selector. The platform orchestration suite checks every registered
module. For focused debugging, select a module child path while retaining the parent setup,
or select the dedicated upgrade-ordering scenario:

```bash
go test -v ./tests/e2e/ \
  -run "TestOdhOperator/Platform_Orchestration_E2E_Tests/Validate_runlevel_gating_and_convergence/Runlevel_31_module_contracts/kserve" \
  -timeout=60m
```

Other examples include `Runlevel_20_module_contracts/monitoring` for the DSCI-backed
Monitoring module, `Runlevel_32_module_contracts/sparkoperator`, and
`DAG_Ordering_Upgrade_E2E_Tests` for the version/runlevel upgrade scenario. The parent test
still enables the production registration set and resolves the real orchestration DAG.

### Setting up cluster prerequisites only

To create DSCI and DSC resources without running the platform orchestration/module contract (useful for manual testing):
`make e2e-setup-cluster`

### Namespace defaults

The test suite inherits namespace configuration from the Makefile based on the platform
build tag (`-tags=odh` or `-tags=rhoai`):

| Variable | ODH default | RHOAI default |
|----------|-------------|---------------|
| `E2E_TEST_OPERATOR_NAMESPACE` | `opendatahub-operator-system` | `redhat-ods-operator` |
| `E2E_TEST_APPLICATIONS_NAMESPACE` | `opendatahub` | `redhat-ods-applications` |
| `E2E_TEST_WORKBENCHES_NAMESPACE` | `opendatahub` | `rhods-notebooks` |
| `E2E_TEST_DSC_MONITORING_NAMESPACE` | `opendatahub` | `redhat-ods-monitoring` |

## Troubleshooting

### Rootless Podman: KinD nodes have no internet access

**Symptoms:** Dependency operator pods stuck in `ImagePullBackOff`. `curl` from inside KinD
nodes times out.

**Root cause:** Rootless Podman requires IP forwarding and firewall masquerading to be
enabled for KinD container networking.

**Fix (run once per boot):**

```bash
sudo sysctl -w net.ipv4.ip_forward=1
```

If using `firewalld`, enable masquerading and forwarding for your active zone:

```bash
ZONE=$(firewall-cmd --get-default-zone)
sudo firewall-cmd --zone="$ZONE" --add-masquerade
sudo firewall-cmd --zone="$ZONE" --add-forward
```

To make permanent:

```bash
echo 'net.ipv4.ip_forward = 1' | sudo tee /etc/sysctl.d/99-ip-forward.conf
sudo firewall-cmd --zone="$ZONE" --add-masquerade --permanent
sudo firewall-cmd --zone="$ZONE" --add-forward --permanent
```

If using `iptables`/`nftables` directly instead of `firewalld`, ensure the `FORWARD` chain
accepts traffic and NAT masquerading is configured for the Podman network.

### Rootless Podman: DNS resolution fails inside KinD nodes

**Symptoms:** Pods can connect to IPs but DNS lookups time out. `resolv.conf` inside KinD
nodes points to Podman's `aardvark-dns` which doesn't forward external queries.

**Fix:** Override DNS on both KinD nodes:

```bash
podman exec kind-odh-control-plane bash -c 'echo "nameserver 8.8.8.8" > /etc/resolv.conf'
podman exec kind-odh-worker bash -c 'echo "nameserver 8.8.8.8" > /etc/resolv.conf'
```

> **Note:** This must be done after each cluster creation. The cluster must be created
> **after** enabling IP forwarding and firewall rules above.

### `kind load image-archive` fails with Podman

**Symptom:** `ERROR: failed to detect containerd snapshotter`

**Fix:** Load images manually via `ctr import`:

```bash
podman save -o /tmp/image.tar localhost/odh-operator:e2e
for node in kind-odh-control-plane kind-odh-worker; do
  podman cp /tmp/image.tar "$node":/tmp/img.tar
  podman exec "$node" ctr -n k8s.io images import /tmp/img.tar
  podman exec "$node" rm /tmp/img.tar
done
```

## Reference

### Makefile targets

| Target | Description |
|--------|-------------|
| `make kind-create` | Create KinD cluster (default: `kind-odh`) |
| `make kind-delete` | Delete KinD cluster |
| `make image-build` | Build operator image |
| `make image-kind-load` | Load image into KinD cluster |
| `make kind-setup-pull-secrets` | Configure pull secrets for `registry.redhat.io` |
| `make deploy-ccm-local-azure` | Deploy Cloud Manager with local image pull policy |
| `make deploy-rhaii-local` | Deploy operator in XKS mode (KServe module + gateway service enabled) |
| `make e2e-test-xks` | Run KinD/xKS Gateway E2E tests with the platform orchestration/module contract disabled |
| `make e2e-test` | Run full E2E tests (requires OpenShift) |
| `make e2e-test-single TEST="<path>"` | Run a single E2E test |
| `make e2e-setup-cluster` | Create DSCI/DSC without running the platform orchestration/module contract |
| `make deploy` | Deploy operator via kustomize (OpenShift) |

### Environment variables

Build/cluster variables (`IMG`, `IMAGE_BUILDER`, `CLUSTER_NAME`, `PULL_SECRET`, etc.) are
documented in the Makefile help text — run `make help` for the full list.

E2E test suites are controlled by `E2E_TEST_*` env vars (or equivalent `--test-*` CLI flags).
Defaults and descriptions live in `tests/e2e/controller_test.go` (`TestMain`). Key vars:

| Variable | CLI flag | Default | Description |
|----------|----------|---------|-------------|
| `E2E_TEST_PLATFORM_ORCHESTRATION` | `--test-platform-orchestration` | `true` | Run OpenShift Platform orchestration and every registered-module contract |
| `E2E_TEST_SERVICES` | `--test-services` | `true` | Run the Services `TestGroup` |
| `E2E_TEST_SERVICE` | `--test-service` | `auth,gateway` | Select Auth and/or Gateway service tests |
| `E2E_TEST_DELETION_POLICY` | `--deletion-policy` | `always` | Delete test resources `never`, `always`, or `on-failure` |
| `E2E_AUTO_RESOLVE` | *(Makefile only)* | `false` | Apply path-based scope to service tests only; the platform orchestration/module contract remains unfiltered |

The historical V2-to-V3 upgrade suite and its E2E flag have been removed.

### Path-based test scoping

`make e2e-test` runs `cmd/manifest-tools resolve-e2e-scope`, which classifies changed files against `tests/e2e/scripts/e2e-scope-rules.yaml`. Component/module classifications remain useful for ownership and completeness checks, but they no longer filter E2E module coverage: when `E2E_TEST_PLATFORM_ORCHESTRATION=true`, all production module registrations run through the platform orchestration contract.

The resolver always logs its decision with the `SELECTIVE-E2E:` prefix. When `E2E_AUTO_RESOLVE=true`, the Makefile applies only the resolved **service** scope through `E2E_TEST_SERVICE`, or disables the Services group when no service is affected. Ambiguous classifications fall back to the safe full scope.

The resolver logic is tested in `cmd/manifest-tools` (`pkg/e2escope`, `pkg/scoperules`) under `make unit-test`. Registry-completeness tests also keep scope rules aligned with built-in module registrations, the remaining in-tree component registry, and the Auth/Gateway Services `TestGroup`. Monitoring belongs in the component/module scope as a DSCI-backed module, not in the services scope.
