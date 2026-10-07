# Module Orchestrator

This package implements the platform-side orchestration for modular components.
It provides the `ModuleHandler` interface, a `BaseHandler` with shared helpers,
a registry for handler lifecycle, and watch infrastructure for status
aggregation.

For the full architectural context, see
[docs/modular/Onboarding Guide for ODH Operator Modules.md](../../../docs/modular/Onboarding%20Guide%20for%20ODH%20Operator%20Modules.md).

## Architecture

The modular orchestrator manages **out-of-tree module operators** alongside the
remaining in-tree components. OpenShift separates user-facing configuration
from operator deployment:

```text
DSC / DSCI Reconcile
  -> project source-owned management state into Platform.spec.modules
  -> build and apply source-owned module CRs
  -> watch module CR status and propagate it to DSC / DSCI status

Platform Reconcile
  -> cleanupDisabledModules    (keep operators while CR finalizers run)
  -> provisionModules          (resolve production DAG; collect operator manifests)
  -> Helm / Kustomize render
  -> inject module env and platform config
  -> deploy.NewAction          (SSA-apply operator resources)
  -> updateModuleStatus        (aggregate module CR status on Platform)
  -> gc.NewAction              (last action; clean owned operator resources)
```

On xKS, the chart supplies the `Platform` CR directly. The Platform controller
therefore uses one orchestration path on every platform, while DSC/DSCI remain
the OpenShift source of module configuration and module CR ownership.

Status readiness includes staleness detection
(`status.observedGeneration == metadata.generation`) and the platform release
handshake. A module that is degraded, stale, or reporting the wrong platform
release cannot advance DAG readiness.

### Module CR ownership and cleanup

Module CR ownership follows the registration's configuration source. The DSC
controller creates DSC-backed module CRs with the `DataScienceCluster` as
controller owner; the DSCI controller does the same with
`DSCInitialization` for DSCI-backed modules such as Monitoring. The Platform
controller deploys module operator resources and uses `WithDynamicOwnership()`
for its rendered resources. This provides:

- cascade deletion from the resource that owns each module resource;
- automatic watch registration through `EnqueueRequestForOwner`, so owned
  resource changes requeue the owning controller; and
- GC integration, where `gc.NewAction` uses registered/dynamically discovered
  owned GVKs to determine what can be cleaned up.

The Platform controller registers watches for every module CR GVK and CRD;
DSC/DSCI controllers register the source-specific status watches needed for
compatibility status propagation.

CRDs are an exception — the deploy action routes CRDs to a dedicated
`deployCRD()` path that never sets owner references (CRDs are cluster-scoped
singletons that may be shared).

Disabled modules use two-phase cleanup:

1. The DSC/DSCI source controller removes the module CR. While it is still
   deleting, the Platform controller leaves the module operator running so it
   can process finalizers and clean up operands.
2. After the CR is confirmed gone, `cleanupDisabledModules` renders and deletes
   the module operator resources.

### Component-to-module migration

Components already use `components.platform.opendatahub.io` -- the GVK stays
the same when migrating to a module. Migration is a **reconciler handoff**:
the in-tree reconciler stops and the module operator starts reconciling the
same CR. No owner-ref stripping or old-CR deletion is needed. See the
[Component to Module Migration Guide](../../../docs/modular/Component%20to%20Module%20Migration%20Guide.md)
for the full process.

## Current modules

Production registrations live in
[`builtin/handlers.go`](./builtin/handlers.go). Their order, runlevel, and
configuration source are shared by startup registration and E2E contract
discovery.

| Runlevel | Module handlers | Configuration source |
|----------|-----------------|----------------------|
| `dag.RL(20)` | [AI Pipelines](./aipipelines/), [Dashboard](./dashboard/), [MCP Lifecycle Operator](./mcplifecycleoperator/), [Model Registry](./modelregistry/), [Ray](./ray/), [Trainer](./trainer/), [Workbenches](./workbenches/) | DSC |
| `dag.RL(20)` | [Monitoring](./monitoring/) | DSCI (`spec.monitoring`) |
| `dag.RL(31)` | [KServe](./kserve/) | DSC |
| `dag.RL(32)` | [AI Gateway](./aigateway/), [Feast Operator](./feastoperator/), [MLflow Operator](./mlflowoperator/), [OGX](./ogx/), [Spark Operator](./sparkoperator/), [TrustyAI](./trustyai/) | DSC |

Kueue remains an in-tree component. Auth and Gateway remain services; Monitoring
is not a Services `TestGroup` entry.

### E2E module contract

On OpenShift, `tests/e2e/dag_ordering_test.go` resolves the same built-in
registrations through the production DAG and runs a child contract test for
each module. The suite is controlled as a whole by
`E2E_TEST_DAG_ORDERING` / `--test-dag-ordering`; legacy per-component and
platform-orchestration selectors have been removed.

Each registered module must satisfy the following contract:

1. Its module CR exists and has a controller owner reference to its source DSC
   or DSCI.
2. Its `odh-<module>-config` ConfigMap publishes the version from the Platform resource, and its CR reports `Ready=True`, `status.observedGeneration` equal to `metadata.generation`, and a `platform` release matching that ConfigMap value.
   Missing or incorrect mandatory `Degraded=False` and
   `ProvisioningSucceeded=True` conditions are emitted in the final
   `MODULE_CONTRACT_REPORT` as module-configuration findings.
3. Its operator Deployment is available. The configured target container
   receives `APPLICATIONS_NAMESPACE` and a non-empty value for every env name
   returned by `GetRelatedImages()`.
4. A DSC-backed handler propagates its ready condition to DSC status. The authoritative platform release is validated on the module CR and need not be mirrored into DSC status. Monitoring instead propagates DSCI `Ready`, `ProvisioningSucceeded`, and `MonitoringReady` conditions.

Example focused paths (the parent DAG setup still runs):

```shell
make e2e-test-single TEST="TestOdhOperator/DAG_Ordering_E2E_Tests/Validate_runlevel_gating_and_convergence/Runlevel_20_module_contracts/dashboard"
make e2e-test-single TEST="TestOdhOperator/DAG_Ordering_E2E_Tests/Validate_runlevel_gating_and_convergence/Runlevel_20_module_contracts/monitoring"
```

The contract is currently OpenShift-only. `make e2e-test-xks` sets
`E2E_TEST_DAG_ORDERING=false`; the xKS module contract is deferred.

Runlevel gating uses a 10-minute default timeout. Operators can override it
with `ODH_DAG_RUNLEVEL_TIMEOUT` or `--dag-runlevel-timeout`; the OpenShift E2E
suite temporarily sets the operator value to 5 minutes. Empty platform release
status is treated as ready for first installation, so E2E validates ordering
during a simulated platform-version upgrade.

## Adding a New Module

A module team contributes four things to this repository:

### 1. Manifest source entry (`manifests-config.yaml`)

Add the module's manifests (Helm chart **or** Kustomize overlays) to the
`ODH_COMPONENT_CHARTS` and `RHOAI_COMPONENT_CHARTS` maps:

```bash
# ODH_COMPONENT_CHARTS
["mymodule"]="opendatahub-io:mymodule-operator:main@<commit-sha>:charts/operator"

# RHOAI_COMPONENT_CHARTS
["mymodule"]="red-hat-data-services:mymodule-operator:rhoai-X.Y@<commit-sha>:charts/operator"
```

The manifests must contain the module operator's Deployment, RBAC, CRD, and
ConfigMap. They must **not** contain a CR instance; the platform creates the CR.

### 2. Handler implementation (`internal/controller/modules/<name>/handler.go`)

Embed `BaseHandler` and implement only `IsEnabled` and `BuildModuleCR`.
Set **either** `ChartDir` (Helm) or `ManifestDir` (Kustomize) in `ModuleConfig`
to select the manifest format.

**Helm example:**

```go
func NewHandler() *handler {
    return &handler{
        BaseHandler: modules.BaseHandler{
            Config: modules.ModuleConfig{
                Name:        "mymodule",
                CRName:      "default",
                ChartDir:    "mymodule",
                ReleaseName: "mymodule-operator",
                GVK: schema.GroupVersionKind{
                    Group:   "components.platform.opendatahub.io",
                    Version: "v1alpha1",
                    Kind:    "MyModule",
                },
            },
        },
    }
}
```

**Kustomize example:**

```go
func NewHandler() *handler {
    return &handler{
        BaseHandler: modules.BaseHandler{
            Config: modules.ModuleConfig{
                Name:        "mymodule",
                CRName:      "default",
                ManifestDir: "mymodule",
                ContextDir:  "operator",
                SourcePath:  "overlays/production",
                GVK: schema.GroupVersionKind{
                    Group:   "components.platform.opendatahub.io",
                    Version: "v1alpha1",
                    Kind:    "MyModule",
                },
            },
        },
    }
}
```

Both variants must provide the module-specific `IsEnabled`,
`PopulatePlatformModule`, and `BuildModuleCR` behavior. `BuildModuleCR`
receives a `*DSCContext` plus `*ModuleCRConfig`; see the
[Developer Guide](../../../docs/modular/Module%20Handler%20Developer%20Guide.md)
for complete handler examples.

`BaseHandler` supplies mechanical behavior driven by `ModuleConfig`, including:

| Method | Default behaviour |
|---|---|
| `GetName()` / `GetGVK()` | Return `Config.Name` / `Config.GVK` |
| `GetOperatorManifests()` | Return Helm and/or Kustomize descriptors from `ModuleConfig` |
| `GetRelatedImages()` | Return the module-scoped `RELATED_IMAGE_*` env names |
| `GetModuleStatus()` | Read conditions, generations, and release metadata from the singleton module CR |
| `GetModuleCRState()` / `DeleteModuleCR()` | Implement idempotent singleton lifecycle operations |
| `DeleteOperatorResources()` | Render and delete module operator resources during phase-two cleanup |
| `WriteDSCComponentStatus()` | Write management state and releases to the typed DSC component status field |

`BaseHandler.PopulatePlatformModule` is a no-op, so a production handler must
override it to project DSC/DSCI management state into the Platform CR.

### 3. DSC API stanza (`api/datasciencecluster/v2/datasciencecluster_types.go`)

Add a field to the `Components` struct so users can enable/configure the module
through the `DataScienceCluster` CR:

```go
// MyModule component configuration.
MyModule DSCMyModule `json:"mymodule,omitempty"`
```

Define the corresponding types (typically in a new file under
`api/components/v1alpha1/`):

```go
type DSCMyModule struct {
    common.ManagementSpec `json:",inline"`
    MyModuleCommonSpec    `json:",inline"`
}

type MyModuleCommonSpec struct {
    // Module-specific fields exposed in the DSC.
}
```

After modifying the API types, run `make generate` and `make manifests` to
regenerate deepcopy functions and CRD manifests.

### 4. Registration (`internal/controller/modules/builtin/handlers.go`)

Import the handler package and add one `ModuleRegistration` to the production
registration slice. DSC is the default configuration source:

```go
import mymodule "github.com/opendatahub-io/opendatahub-operator/v2/internal/controller/modules/mymodule"

{
    Handler:  mymodule.NewHandler(),
    Runlevel: dag.RL(32),
},
```

For a DSCI-backed module, also set `ConfigSource: modules.ConfigFromDSCI`.
Registration controls startup wiring, DAG ordering, cleanup ownership source,
and automatic enrollment in the OpenShift E2E module contract. Add the module
to `tests/e2e/scripts/e2e-scope-rules.yaml` so registry-completeness tests can
validate path ownership; scope rules do not filter the DAG contract.

## Package Reference

### `types.go` -- ModuleHandler interface and PlatformContext

The `ModuleHandler` contract between the platform and each module handler:

- `GetName()` and `GetGVK()` identify the registry entry and module CR type.
- `IsEnabled(modules)` reads projected Platform module state.
- `GetOperatorManifests(platform)` returns Helm and/or Kustomize descriptors.
- `PopulatePlatformModule(pm, dscCtx)` projects DSC/DSCI management state into
  the Platform CR.
- `BuildModuleCR(ctx, cli, dscCtx, cfg)` constructs the source-owned module CR.
- `GetRelatedImages()` declares module-scoped env names for operand images.
- `GetModuleStatus(ctx, cli)` returns conditions, generation metadata, and
  release metadata used by DAG readiness and status aggregation.
- `GetModuleCRState(ctx, cli)` and `DeleteModuleCR(ctx, cli)` implement the
  three-state module CR lifecycle used by two-phase cleanup.
- `DeleteOperatorResources(ctx, cli, platform)` removes rendered operator
  resources after the module CR is gone.
- `WriteDSCComponentStatus(dsc, enabled, releases)` exposes management state
  and release metadata on typed DSC status.


`PlatformContext` is built once per reconcile in `provisionModules` and passed
to every handler's `BuildModuleCR`. It exposes:

| Field | Source | Description |
|---|---|---|
| `ApplicationsNamespace` | `DSCI.Spec.ApplicationsNamespace` | Namespace where module operands deploy |
| `MonitoringNamespace` | `DSCI.Spec.Monitoring.Namespace` | Namespace for the monitoring stack (empty when monitoring is unset or in xKS mode with no DSCI) |
| `Release` | `rr.Release` | Platform identity (ODH/RHOAI) and version |
| `Modules` | `Platform.Spec.Modules` | Projected per-module management state |
| `ChartsBasePath` | reconciler settings | Base path for bundled Helm charts |
| `ManifestsBasePath` | reconciler settings | Base path for bundled Kustomize manifests |

#### Injected environment variables (platform → module operator)

After rendering and before deploy, the `injectModuleEnv` action writes these env
vars onto each module operator's Deployment container (default `manager`). Only
non-empty values are injected. **The names are a contract**: a module operator
that needs these values must read them by these exact, unprefixed names.

| Env var | Source | Notes |
|---|---|---|
| `APPLICATIONS_NAMESPACE` | `PlatformContext.ApplicationsNamespace` | Always injected when set |
| `MONITORING_NAMESPACE` | `PlatformContext.MonitoringNamespace` | Skipped when empty (e.g. monitoring unset, xKS) |
| `ODH_MODULE_OPERATOR_PLATFORM_TYPE` | active platform type | Injected when platform type is known |
| `RELATED_IMAGE_*` | operator env of the same name | Scoped per module via `GetRelatedImages()`; empty source values are skipped |
| module `ExtraEnv` | `ModuleConfig.ExtraEnv` | Fixed module-owned handoff/configuration values |

`ModuleConfig.ControllerImage` is different from `RelatedImages`: its env value
overrides the rendered operator container image (and an optional named init
container), while `RelatedImages` are injected as env vars for the module
operator to consume. `DeploymentName` and `ContainerName` must identify the
rendered target correctly. The E2E module contract requires all declared
`RelatedImages` and `APPLICATIONS_NAMESPACE`; controller-image and other
injection behavior also has focused unit coverage.

The operator's own config env (`ODH_MANAGER_DSC_MONITORING_NAMESPACE`, bound via
viper) is a separate input mechanism and is unrelated to these injected names.

### `base.go` -- BaseHandler and ModuleConfig

`ModuleConfig` holds static metadata (name, GVK, manifest paths, deployment
and container names, controller-image override, related images, fixed env, and
status extensions). Set `ChartDir` for Helm or `ManifestDir` for Kustomize (or
both). Module teams embed `BaseHandler` and implement the source-specific
`IsEnabled`, `PopulatePlatformModule`, and `BuildModuleCR` behavior.

`ModuleStatus` bundles parsed conditions, generation metadata
(`ObservedGeneration`, `Generation`), `ReleaseVersion`, and the full release
list for readiness gating and DSC status propagation.

`ParseConditions(u)` is a shared utility that extracts `[]metav1.Condition`
from an unstructured object's `.status.conditions` field, including
`ObservedGeneration` and `LastTransitionTime`.

`GetModuleCRState` reports `Absent`, `Alive`, or `Deleting`; not-found and
missing-CRD cases are treated as absent. `DeleteModuleCR` is idempotent.

`DeleteOperatorResources` renders the module's Helm chart via
`GetOperatorManifests`, then deletes each rendered resource from the cluster.
NotFound errors are silently ignored for idempotency.

### `registry.go` -- Module registry

A singleton registry that stores `ModuleHandler` instances. Built-in handlers
and their runlevels/configuration sources come from `builtin.Registrations()`
and are registered at program startup. The registry supports:

- `Add(handler, ...RegistrationOption)` -- register a handler
- `Enable(name)` / `Disable(name)` -- CLI suppression flag integration
- `ForEach(fn)` / `ForEachEnabled(fn)` -- iterate enabled handlers
- `ForAll(fn)` -- iterate all handlers, including startup-suppressed entries
- `ForConfigSource(source, fn)` -- iterate DSC- or DSCI-backed registrations
- `ResolvedBatches()` / `ReverseBatches()` -- resolve provisioning/cleanup DAG order
- `HasEntries()` -- check if any modules are registered
- `RegistrationOption` -- `WithRunlevel(...)` and `WithConfigSource(...)`

### `modules_controller.go` -- Watches and dynamic ownership

The Platform controller watches registered module CR and CRD GVKs so status
and newly installed APIs trigger reconciliation. It uses dynamic ownership for
rendered module operator resources. DSC and DSCI controllers separately watch
the module CRs sourced from their APIs and propagate compatibility status to
the corresponding user-facing resource.

## Suppression Flags

Module handlers can be disabled at startup via CLI flags. The flags package
(`pkg/utils/flags/suppression.go`) provides:

- `RegisterModuleSuppressionFlags(names)` -- registers `--disable-<name>-module` flags
- `IsModuleEnabled(name)` -- checks if the flag is set

These integrate with the registry's `Enable`/`Disable` methods in
`cmd/main.go`'s `registerModules()` function.

## Relationship to `odh-platform-utilities`

The [odh-platform-utilities](https://github.com/opendatahub-io/odh-platform-utilities)
library provides shared rendering primitives for **module operator teams**
(Helm/Kustomize/Template actions, `ReconciliationRequest`, resource helpers).
