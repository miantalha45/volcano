# xPU Local-Domain POC

This package is an intentionally narrow proof of concept for [issue #5751](https://github.com/volcano-sh/volcano/issues/5751). It is not registered as a scheduler plugin and does not introduce a workload API, provider informer, DRA integration, DeviceShare integration, or Gang framework extension.

The POC demonstrates five scheduler-side primitives:

1. A provider-style cache applies generation-checked Node updates and publishes immutable topology revisions.
2. A canonical in-memory snapshot validates stable device and local-domain membership.
3. A local-domain selector rejects aggregate capacity fragmented across independent domains and deterministically selects IDs from one eligible domain.
4. A GangPlan selects every task placement before attempting one atomic reservation for the complete plan.
5. A mutex-protected ledger reserves every requested ID or none, then releases the reservation as one unit.

Run the focused tests with:

    go test ./pkg/scheduler/plugins/xputopology

The next implementation step, after maintainer agreement, is to connect this model to a feature-gated provider and scheduler predicate without making this POC a competing device allocator.
