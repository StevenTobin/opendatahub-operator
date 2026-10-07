# RHOAI 3.5.1 → latest main MLflow upgrade baseline

Captured before upgrade on 2026-10-07.

## Operator installation

- Subscription: `redhat-ods-operator/rhods-operator`
- Channel/source: `stable-3.x` / `redhat-operators`
- Installed CSV: `rhods-operator.3.5.1`
- CSV UID: `786d8a91-6e0b-42af-aad8-bdb7be928958`
- CSV phase: `Succeeded`
- Operator Deployment: `redhat-ods-operator/rhods-operator`, 3/3 replicas available

## DataScienceCluster

- API: `datasciencecluster.opendatahub.io/v2`
- Name: `default-dsc`
- UID: `90f0508f-ebc0-4eaf-ba41-7b4690ffd3aa`
- Creation: `2026-10-07T10:40:49Z`
- Generation: `1`
- Deletion timestamp: absent
- Finalizers: none
- Phase/release: `Ready` / `3.5.1`
- Desired components: only `mlflowoperator: Managed`; all others normalize to Removed

This UID is the key post-upgrade comparison. A normal v2→v3 conversion should preserve it.

## DSCInitialization

- API: `dscinitialization.opendatahub.io/v2`
- Name: `default-dsci`
- UID: `cb23c4a8-4989-4676-a35d-4d0717ac0608`
- Creation: `2026-10-07T10:34:12Z`
- Generation: `1`
- Deletion timestamp: absent
- Finalizer: `dscinitialization.opendatahub.io/finalizer`
- Phase/release: `Ready` / `3.5.1`

## Platform

- `Platform/default` is absent before the upgrade.

The latest controller is therefore expected to create this singleton during the upgrade; there is no pre-existing Platform UID or owner-reference set to preserve from 3.5.

## MLflowOperator

- API: `components.platform.opendatahub.io/v1alpha1`
- Name: `default-mlflowoperator`
- UID: `3de79450-7a73-4d8d-9829-123347b3cdce`
- Creation: `2026-10-07T10:40:51Z`
- Generation / observed generation: `1` / `1`
- Deletion timestamp: absent
- Finalizer: `mlflow.opendatahub.io/mlflow-operator-protection`
- Ready: `True`
- Platform release: `3.5.1`
- MLflow release: `3.14.0`
- Controller owner: v2 `DataScienceCluster/default-dsc`, UID `90f0508f-ebc0-4eaf-ba41-7b4690ffd3aa`

The MLflowOperator UID should remain unchanged during an in-place adoption. A deletion timestamp after upgrade is the ticket failure signal.

## MLflow instance

- API: `mlflow.opendatahub.io/v1`
- Name: `mlflow`
- UID: `de789e65-449f-41fd-97f3-42edd8e787e9`
- Creation: `2026-10-07T10:41:34Z`
- Generation: `1`
- Deletion timestamp: absent
- Owner references: none
- Finalizers: none
- Available: `True` (`DeploymentReady`)
- Migration: `True` (`MigrationSucceeded`)
- Version: `3.14.0`
- Pod: `redhat-ods-applications/mlflow-6946db94c6-vcmtp`, 2/2 Running
- Operator pod: `redhat-ods-applications/mlflow-operator-controller-manager-6776ff4689-9xrd8`, 1/1 Running

## Events

Two `AdminAckRequired` warnings were emitted as the DSC was first reconciled, but it subsequently reached `Ready`, `ModulesReady=True`, and `ProvisioningProgress=True`. The current upgrade-ack ConfigMap is captured in `upgrade-acks.yaml`.

## Post-upgrade comparisons

Check these invariants after upgrading:

1. DSC UID remains `90f0508f-ebc0-4eaf-ba41-7b4690ffd3aa`.
2. DSCI UID remains `cb23c4a8-4989-4676-a35d-4d0717ac0608`.
3. MLflowOperator UID remains `3de79450-7a73-4d8d-9829-123347b3cdce`.
4. MLflow UID remains `de789e65-449f-41fd-97f3-42edd8e787e9`.
5. Platform is created without entering deletion and has valid live owner references.
6. MLflowOperator never receives a deletion timestamp.
7. MLflow remains `Available=True` and is observed by the upgraded operator.
8. Operator logs do not repeatedly report `waiting for module CRs to be deleted`.
