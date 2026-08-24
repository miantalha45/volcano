# xPU Local-Domain POC

This package is a narrow proof of concept for [issue #5751](https://github.com/volcano-sh/volcano/issues/5751), Support Generic xPU Topology-Aware Scheduling.

It demonstrates the scheduler-side logic needed to place accelerator workloads in one valid local device domain. It is not registered as a Volcano scheduler plugin. It does not add a workload API, informer, DRA integration, DeviceShare integration, or Gang framework extension.

## What Problem It Proves

A Node-level device count does not show whether devices belong to one connected domain.

For example, a Node has eight free NPUs, but they are split across two independent HCCS domains. An eight-NPU workload that requires one connected domain must not be placed on that Node.

```mermaid
flowchart LR
    Request[Request: 8 NPUs in one local domain]
    Node[Node reports: 8 free NPUs]
    A[HCCS A: 6 free NPUs]
    B[HCCS B: 2 free NPUs]
    Reject[Reject: no local domain has 8 devices]

    Request --> Node
    Node --> A
    Node --> B
    A --> Reject
    B --> Reject
```

The POC checks each local domain separately. It does not treat the total number of devices on a Node as proof that one connected device group exists.

## POC Flow

The POC separates provider facts, read-only topology, and changing reservation state.

```mermaid
flowchart LR
    Update[ProviderUpdate\nNode, generation, devices, domains]
    Cache[TopologyCache]
    Snapshot[Read-only Snapshot\ndevices, local domains, indexes]
    Summary[Domain feasibility summary\nfilter invalid Nodes or groups]
    Select[SelectLocalDomain\nchoose one eligible domain]
    Plan[PlanGang\nselect every task placement]
    Ledger[Ledger\nfree, reserved, unhealthy]
    Reserve[TryReserve\nall IDs or none]

    Update --> Cache
    Cache --> Snapshot
    Snapshot --> Summary
    Ledger --> Summary
    Summary --> Select
    Snapshot --> Select
    Snapshot --> Plan
    Select --> Plan
    Ledger --> Select
    Plan --> Reserve
    Reserve --> Ledger
```

1. `TopologyCache.Apply` accepts a Node update only when its generation is newer than the stored generation.
2. The cache validates the update and creates a new `Snapshot`.
3. `BuildDomainFeasibilitySummary` can remove Nodes or candidate groups that have no valid local domain before later placement stages run.
4. `SelectLocalDomain` looks for enough healthy and free devices inside one domain.
5. `PlanGang` finds a placement for every requested task before changing the ledger.
6. `TryReserve` reserves every selected device ID together or leaves all IDs unchanged.

## Main Types

| Type | Purpose in this POC |
| --- | --- |
| `Device` | One physical accelerator with an ID, resource name, Node, local-domain ID, and health state. |
| `LocalDomain` | One Node-local connected device group and its member device IDs. |
| `Snapshot` | A validated, read-only view of devices and local domains. It indexes local domains by Node and resource name. |
| `TopologyCache` | Stores the latest snapshot and rejects stale provider updates for a Node. |
| `Ledger` | Stores changing device availability. A device is free, reserved, allocated, or unhealthy. |
| `Placement` | The selected Node, local domain, and concrete device IDs for one task. |
| `GangPlan` | Every task placement and the single reservation that covers all selected IDs. |

The `Snapshot` answers, “Which domains and devices exist?” The `Ledger` answers, “Which of those devices are safe to select now?”

## Local-Domain Selection

`SelectLocalDomain` receives a Node, resource name, and requested device count. It checks local domains in sorted ID order. Within an eligible domain, it sorts device IDs before choosing them.

This gives two useful properties:

- A fragmented Node fails when no one domain has enough healthy and free devices.
- The same input produces the same selected device IDs.

The HAMi-shaped test uses ten Ascend910 devices split into two independent five-device `NetworkID` groups. An eight-device request fails even though the Node has ten devices in total. The POC receives generic local-domain facts. It does not depend on HAMi fields or vendor-specific selection logic.

## Candidate-Domain Filtering

`BuildDomainFeasibilitySummary` prepares a read-only summary before a later Node or HyperNode selection step. It removes a Node only when no local domain has enough healthy, free devices for the request. It keeps the input order for the remaining Nodes.

The POC represents an upstream candidate scope with `CandidateGroup`. A group stays eligible when at least one member Node has a fitting local domain. This is intentionally not a new HyperNode gradient or a registered scheduler hook. It demonstrates the required direction: domain facts should narrow candidate Nodes before normal Node placement decides among them.

## Gang Planning and Atomic Reservation

The POC plans the whole Gang before it reserves a device.

```mermaid
sequenceDiagram
    participant Planner as PlanGang
    participant Snapshot as Snapshot
    participant Ledger as Ledger

    Planner->>Snapshot: Select placement for task 1
    Snapshot-->>Planner: Node, domain, device IDs
    Planner->>Snapshot: Select placement for every remaining task
    Snapshot-->>Planner: Complete plan or error
    alt Every task has a placement
        Planner->>Ledger: TryReserve(all selected IDs)
        Ledger-->>Planner: One reservation or conflict
    else A task has no placement
        Planner-->>Planner: Return error, change no ledger state
    end
```

`PlanGang` prevents one task from using device IDs that another task in the same plan already selected. It tries candidate Nodes in deterministic order. If an early placement blocks a later Task, it tries another earlier placement. The POC limits this search to 32 placement alternatives, so a difficult input cannot search without a bound.

If the POC cannot complete the Gang plan within that bound, it returns `ErrGangPlanIncomplete` before reserving any ID. It does not yet define the production task ordering or the final Gang framework hook.

`TryReserve` holds a mutex while it checks the complete request. It rejects duplicate IDs, an existing reservation ID, and every device that is not free. On failure, it does not reserve the earlier IDs from the request.

```text
selected IDs: [device-1, device-2]

device-1 free, device-2 free        -> reserve both
device-1 free, device-2 reserved    -> reserve neither
```

`Release` returns devices from one reservation to the free state. It leaves an allocated or unhealthy device unchanged.

## Test Coverage

| Test | Behaviour covered |
| --- | --- |
| `TestSelectLocalDomainRejectsFragmentedCapacity` | Rejects six-plus-two capacity for an eight-device request. |
| `TestSelectLocalDomainRejectsHAMiShapedNetworkIDFragmentation` | Maps a legacy HAMi-shaped five-plus-five `NetworkID` layout into generic local domains and rejects an eight-device request. |
| `TestSelectLocalDomainSelectsDeterministicDeviceIDs` | Selects sorted IDs from one eligible domain. |
| `TestTryReserveIsAtomic` | A conflict leaves unrelated free IDs unchanged. |
| `TestReleaseMakesDevicesAvailableAgain` | Releasing a reservation returns reserved devices to free state. |
| `TestNewSnapshotRejectsUnknownDomainMember` | Rejects invalid device-to-domain membership. |
| `TestTopologyCacheUpdatesOnlyAffectedNode` | Accepts a newer Node generation, preserves other Node facts, and rejects a stale update. |
| `TestDomainFeasibilitySummaryFiltersFragmentedCandidateGroups` | Removes a fragmented candidate group before Node selection while retaining a group with one valid local domain. |
| `TestPlanGangReservesEveryTaskOrNothing` | Reserves all task IDs for a complete Gang plan and leaves the ledger unchanged for an incomplete plan. |
| `TestPlanGangBacktracksToCompletePlacement` | Retries an earlier Task on another Node when the first deterministic choice blocks a later Task. |
| `TestPlanGangAlternativeLimitLeavesLedgerUnchanged` | Stops bounded search without reserving IDs. |

Run the focused tests:

```bash
go test ./pkg/scheduler/plugins/xputopology
```

## What This POC Does Not Do

This POC deliberately does not:

- register a production scheduler plugin or workload API
- watch Kubernetes Nodes, topology CRDs, Device Plugins, or DRA ResourceSlices
- allocate a device through kubelet, DeviceShare, or a DRA driver
- create cross-Node fabric domains
- schedule fractional devices such as vGPU, vNPU, or MIG slices
- change normal `NodeInfo` resource accounting
- change existing DeviceShare or DRA behaviour
- add a Gang framework hook or bind Pods

The next production step needs maintainer agreement on the workload API, provider contract, exact-allocation adapter contract, and transaction integration. The POC is evidence for the local-domain planning and atomic-reservation rules. It is not a competing device allocator.
