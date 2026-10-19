# Module-Focused E2E Mode — Implementation Plan

## Purpose

Add an exclusive module-focused E2E mode for validating an individual registered module when gating module image or manifest SHA updates.

The focused mode must exercise the Open Data Hub operator's integration responsibilities for the selected module without running unrelated module contracts, services, platform-wide DAG upgrade tests, or module operand-specific assertions.

This document is intended to be handed directly to an implementation agent.

## Critical compatibility invariant

Plain `make e2e-test` must remain unchanged.

When no module selector is configured:

```text
E2E_TEST_MODULE_CONFORMANCE=""
```

all existing E2E suites must run as they do today, including platform orchestration and contract checks for **every** registration returned by `modulebuiltin.Registrations()`.

The focused behavior must activate only when exactly one module name is explicitly selected.

Add regression coverage proving both modes:

1. Empty selection resolves to every registered module in the normal suite.
2. One selected module activates focused mode and resolves only that registration.

Do not change the default value of `E2E_TEST_PLATFORM_ORCHESTRATION` or make `make e2e-test` implicitly focused.

## Existing flags

Use the existing flags:

```text
E2E_TEST_MODULE_CONFORMANCE
--module-conformance

E2E_TEST_FAIL_ON_MODULE_CONFORMANCE
--fail-on-module-conformance
```

Focused SHA-gating runs will set:

```text
E2E_TEST_MODULE_CONFORMANCE=sparkoperator
E2E_TEST_FAIL_ON_MODULE_CONFORMANCE=true
```

### Selector semantics

- Empty selection: normal full E2E mode.
- Exactly one module: exclusive focused module mode.
- More than one module: reject with an actionable configuration error for now.
- Unknown module: reject and print the available built-in registration names.
- Module names are handler registration names, for example `sparkoperator`, `aihub`, `data`, `databaseservice`, and `monitoring`.

Multiple-module focused runs can be added later if a concrete use case appears.

## Non-goals

Do not add:

- Monitoring-specific operand tests. Monitoring is treated as a normal DSCI-backed module.
- xKS focused module coverage.
- module operand inspection or module-specific workload assertions.
- platform-wide DAG upgrade ordering in focused mode.
- Auth or Gateway service tests in focused mode.
- conversion webhook tests in focused mode.
- operator resilience matrix tests in focused mode.
- deprecated-resource preservation tests in focused mode.
- random module selection in focused mode.

The normal full E2E mode continues to own those applicable platform-wide suites.

## Responsibility boundary

Focused mode validates behavior owned by the Open Data Hub operator:

- source DSC/DSCI management-state projection;
- Platform module projection;
- module operator manifest deployment;
- module CR creation and owner references;
- platform ConfigMap handoff;
- environment and image injection;
- selected module status/release compatibility contract;
- DSC/DSCI compatibility status propagation;
- platform-owned resource recovery;
- selected module disable and cleanup sequencing.

Focused mode may validate module status fields required by the integration contract, but must classify those failures as module conformance failures. It must not inspect the module operator's internal operands.

## Proposed test hierarchy

Add a focused top-level subtest under `TestOdhOperator`:

```text
TestOdhOperator
└── Module_Focused_E2E_Tests
    └── sparkoperator
        ├── Prepare_clean_module_baseline
        ├── Enable_selected_module
        ├── Validate_selected_module_contract
        ├── Validate_platform-owned_resource_recovery
        └── Disable_and_cleanup_selected_module
```

Suggested source file:

```text
tests/e2e/module_focused_test.go
```

Reuse contract helpers from:

```text
tests/e2e/module_contract_test.go
```

## Phase 1: selection validation and mode routing

### Configuration validation

Add a helper that canonicalizes and validates the selector during `TestMain` setup:

```go
func selectedModuleRegistration() (modulebuiltin.ModuleRegistration, bool)
```

Requirements:

- ignore empty string entries produced by environment parsing;
- require at most one non-empty name;
- validate against `modulebuiltin.Registrations()`;
- return the exact production registration, including `ConfigSource` and runlevel metadata;
- produce a stable, sorted list of available names in configuration errors.

### Exclusive route in `TestOdhOperator`

After common logger, scheme, watchdog, and cluster-health initialization, but before normal cleanup and normal suites:

```go
if registration, focused := selectedModuleRegistration(); focused {
    mustRun(t, "Module Focused E2E Tests", func(t *testing.T) {
        moduleFocusedTestSuite(t, registration)
    })
    return
}
```

The focused branch must occur before:

- `CleanupPreviousTestResources`;
- DSC management and validation suites;
- platform orchestration;
- services;
- resilience;
- conversion tests;
- deprecated-resource preservation;
- normal deletion-policy handling.

Reason: focused mode assumes DSC/DSCI already exist and must not let global cleanup delete them before the focused suite starts.

### Full-suite compatibility

When no module is selected, execution must follow the existing code path exactly. Do not introduce additional returns, skips, or changed defaults in the normal path.

## Phase 2: focused suite context

Create a dedicated context:

```go
type ModuleFocusedTestCtx struct {
    *TestContext

    registration modulebuiltin.ModuleRegistration
    report       *moduleContractReport
}
```

Create the suite entry point:

```go
func moduleFocusedTestSuite(
    t *testing.T,
    registration modulebuiltin.ModuleRegistration,
)
```

Suite requirements:

- `skipUnless(t, Tier2)` or the current platform-orchestration tier;
- initialize `TestContext`;
- skip xKS explicitly;
- verify DSC and DSCI exist;
- verify the operator Deployment is available;
- use the selected handler name as the module child-test name;
- register cleanup before mutating module state.

Do not modify `RHAI_VERSION`, `CI`, `ODH_DAG_RUNLEVEL_TIMEOUT`, upgrade acknowledgements, or DAG quotas.

## Phase 3: clean module baseline

Focused mode assumes DSC and DSCI exist, but it must establish isolated module state.

Before enabling the selected module:

1. Set all DSC module fields to `Removed` using `allComponentsRemovedTransform()`.
2. Set DSCI Monitoring to `Removed`.
3. Wait for the DSC and DSCI changes to be observed.
4. Wait for existing module CRs to disappear.
5. Wait for platform-owned module operator Deployments to disappear.

Reuse existing helpers where possible, but move DAG-independent helpers away from `DAGOrderingTestCtx` if needed. Candidate helpers:

- `setDSCIMonitoringState`;
- `setAllRemoved`;
- `ensureAllRemovedComponentsGone`;
- `ensureAllRemovedOperatorDeploymentsGone`;
- `selectComponentsTransform`;
- `dscComponentField`.

Prefer standalone helper functions or a small shared module lifecycle context rather than making focused mode semantically depend on DAG test types.

## Phase 4: enable only the selected registration

### DSC-backed registration

Use the selected production registration and current DSC field mapping:

```go
field := moduleManagementField(registration, modules.ConfigFromDSC)

mutate := selectComponentsTransform(
    string(operatorv1.Managed),
    []string{field},
)
```

This must preserve special paths:

- Dashboard → `spec.components.dashboard.standard.managementState`;
- Data → both `featureStore` and `dataRegistry`;
- AIHub → `spec.components.aiHub.managementState`.

### DSCI-backed registration

For Monitoring:

```go
setDSCIMonitoringState(t, operatorv1.Managed)
```

All DSC-backed modules remain `Removed`.

### Selected projection wait

Wait only for selected-module platform behavior:

- source DSC/DSCI generation observed;
- selected `Platform.spec.modules` management state projected;
- selected module operator resources begin deployment;
- selected module CRD becomes served;
- selected canonical module CR appears.

Do not require platform-wide `ModulesReady=True` as a prerequisite for contract diagnostics.

## Phase 5: selected module contract

Invoke the existing generic contract directly for the selected registration:

```go
contractCtx := newModuleContractTestCtx(
    t,
    focusedCtx.TestContext,
    registration,
    focusedCtx.report,
)
contractCtx.ValidateModuleContract(t)
focusedCtx.report.log(t)
```

The focused automation target sets:

```text
E2E_TEST_FAIL_ON_MODULE_CONFORMANCE=true
```

The selected module child test must fail when that module has findings. Do not depend only on a sibling report test because Go `-run` filtering may skip the report stage.

Retain the existing distinction:

- platform-owned integration failures fail normally;
- module status/release findings are reported as module-configuration failures;
- default full E2E mode remains non-fatal for module findings unless the fail flag is explicitly set.

## Phase 6: selected CR spec projection

Add a platform-owned desired-state comparison for the selected module.

Use the production handler to build the expected module CR from current platform inputs:

```go
expected, err := registration.Handler.BuildModuleCR(
    ctx,
    client,
    dscContext,
    moduleCRConfig,
)
```

Compare expected and actual platform-owned desired state:

```text
expected.spec
actual.spec
```

Normalize:

- omitted versus empty maps;
- API-server defaults;
- metadata and status;
- fields not owned by the platform.

If generic ownership cannot be inferred safely, add an optional handler interface that returns platform-owned spec paths. Do not compare module-owned status or operands.

This phase may be implemented after the initial focused routing if it requires broader handler API work.

## Phase 7: selected platform-owned recovery

Refactor platform recovery so it accepts an explicit registration.

Focused mode must not call `randomModuleRegistration`.

For the selected module only:

1. Corrupt its platform ConfigMap version.
2. Add a simulated module-owned key.
3. Verify the platform restores its version.
4. Verify the module-owned key remains.
5. Delete the selected module operator Deployment.
6. Verify the platform recreates it.
7. Verify the new rollout is current and available.

The broad platform suite may continue using randomized module selection.

## Phase 8: disable and cleanup selected module

Set only the selected source field to `Removed`.

Verify:

- selected module CR eventually disappears;
- selected platform-owned operator Deployment disappears;
- selected platform handoff resources are removed or returned to expected disabled state;
- DSC/DSCI selected-module management state reports `Removed`;
- unrelated modules remain `Removed`.

Do not require observing the transient `Deleting` state in real E2E; module finalizers may complete too quickly. Deterministic two-phase finalizer behavior remains covered by `internal/controller/modules/lifecycle_integration_test.go`.

## Phase 9: optional re-enable cycle

After the initial focused mode is stable, add an optional second lifecycle:

1. Re-enable the selected module.
2. Verify a new module CR UID.
3. Verify operator Deployment and ConfigMap recreation.
4. Rerun selected contract checks.
5. Disable again during cleanup.

This phase is valuable for detecting stale ownership and incomplete cleanup but is not required for the first implementation.

## Phase 10: dependencies

Start with strict isolation:

```text
selected module Managed
all other modules Removed
```

If a module cannot become ready independently, add explicit dependency metadata rather than enabling all modules.

Preferred direction:

```go
type ModuleRegistration struct {
    Handler      modules.ModuleHandler
    Runlevel     dag.Runlevel
    ConfigSource modules.ConfigSource
    Dependencies []string
}
```

Focused setup may enable the dependency closure, but:

- only the selected module is a test target;
- dependency contracts do not run;
- dependency failures are reported as setup/dependency failures;
- JUnit and triage remain attributed to the selected module.

Do not create a second, divergent E2E-only dependency graph if production dependency metadata already exists or can be added.

## Phase 11: Makefile target

Add:

```make
.PHONY: e2e-test-module
e2e-test-module:
ifndef MODULE
	$(error MODULE is required. Example: make e2e-test-module MODULE=sparkoperator)
endif
	E2E_TEST_MODULE_CONFORMANCE=$(MODULE) \
	E2E_TEST_FAIL_ON_MODULE_CONFORMANCE=true \
	$(MAKE) e2e-test
```

Because the Go runner enters exclusive mode when a module is selected, the target should not need to disable every other suite manually.

Examples:

```bash
make e2e-test-module MODULE=sparkoperator
make e2e-test-module MODULE=monitoring
```

## Phase 12: shell runner, retry, and triage

### Shell runner

Ensure `tests/e2e/scripts/run_e2e_tests.sh` passes:

```text
--module-conformance
--fail-on-module-conformance
```

This wiring already exists; preserve it during refactoring.

### Retry tooling

Teach `cmd/test-retry` to treat this as a module-level retry unit:

```text
TestOdhOperator/Module_Focused_E2E_Tests/<module>
```

A retry must rerun the complete selected module lifecycle, not one low-level assertion.

### Failure triage

Update module extraction to support both paths:

```text
TestOdhOperator/Module_Focused_E2E_Tests/<module>/...
TestOdhOperator/Platform_Orchestration_E2E_Tests/.../<module>/...
```

Classify selected module contract findings as:

```text
category=test
subcategory=module-configuration
module=<selected module>
```

Platform-owned failures remain platform/test-logic failures.

## Phase 13: CI SHA-gating matrix

Map changed module SHAs or manifest refs to production handler names using existing scope aliases.

Examples:

```text
modelregistry → aihub
feastoperator → data
odh-observability → monitoring
spark module operator → sparkoperator
```

Create a matrix job:

```yaml
strategy:
  matrix:
    module:
      - sparkoperator
      - aihub
```

Each matrix entry runs:

```bash
make e2e-test-module MODULE="${{ matrix.module }}"
```

Prefer one ephemeral cluster per module matrix job. If one cluster is reused sequentially, focused cleanup must be validated first and each run must begin from all modules `Removed`.

## Required tests for the implementation

Add no-cluster tests for selection and routing:

1. Empty selector returns normal/full mode.
2. Empty selector preserves all registrations for the normal contract loop.
3. One valid selector returns focused mode and the correct production registration.
4. Unknown selector fails with available names.
5. Multiple selectors fail clearly.
6. Focused mode does not call normal service, conversion, resilience, or DAG upgrade suites.
7. Full mode still invokes platform orchestration and all registered module contracts.
8. Random single-module helpers return the selected module in focused mode and remain random in broad mode.

Add focused E2E acceptance coverage for at least:

- one DSC-backed module;
- Monitoring as the DSCI-backed module.

## Validation commands

Run mandatory project gates:

```bash
make generate manifests api-docs
make fmt
make lint
```

Run focused compilation/tests:

```bash
go test ./tests/e2e -run '^$' -count=0
make unit-test-e2e-scope-completeness
go -C cmd/test-retry test ./...
```

Run cluster acceptance:

```bash
make e2e-test-module MODULE=<one DSC-backed module>
make e2e-test-module MODULE=monitoring
```

Finally run the compatibility invariant:

```bash
make e2e-test
```

Confirm from JUnit/log output that plain `make e2e-test` still runs contract children for every enabled registration and does not enter `Module_Focused_E2E_Tests`.

## Completion criteria

The work is complete only when all of the following are true:

- `make e2e-test` without a selector retains existing full-suite behavior and tests all registered modules.
- `make e2e-test-module MODULE=sparkoperator` runs only Spark Operator as the module target.
- Focused mode does not run platform-wide DAG upgrade, services, conversions, resilience, or unrelated contracts.
- Focused setup enables only the selected module plus explicit dependencies.
- Focused cleanup returns the cluster to all modules `Removed`.
- Module findings fail focused SHA-gating runs.
- Module findings remain non-fatal in normal full E2E unless the fail flag is explicitly enabled.
- Monitoring uses the same generic focused module flow.
- xKS remains skipped.
- No module operand-specific assertions are introduced.
