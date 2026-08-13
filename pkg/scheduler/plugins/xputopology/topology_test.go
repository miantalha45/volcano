package xputopology

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

const accelerator = "example.com/npu"

func TestSelectLocalDomainRejectsFragmentedCapacity(t *testing.T) {
	snapshot := newSnapshot(t, map[string]int{
		"hccs-a": 6,
		"hccs-b": 2,
	})

	_, err := snapshot.SelectLocalDomain(NewLedger(snapshot), "worker-0", accelerator, 8)
	if !errors.Is(err, ErrNoEligibleLocalDomain) {
		t.Fatalf("expected fragmented topology to fail with %v, got %v", ErrNoEligibleLocalDomain, err)
	}
}

func TestSelectLocalDomainRejectsHAMiShapedNetworkIDFragmentation(t *testing.T) {
	// This fixture mirrors the legacy HAMi Ascend910 NetworkID input shape:
	// ten physical devices split evenly across two independent local groups.
	// The generic model receives the provider-normalized result, not the
	// vendor-specific CustomInfo["NetworkID"] field itself.
	devices := make([]Device, 0, 10)
	domains := make([]LocalDomain, 0, 2)
	for networkID := 0; networkID < 2; networkID++ {
		domainID := fmt.Sprintf("node-ascend-0/network-%d", networkID)
		deviceIDs := make([]DeviceID, 0, 5)
		for index := 0; index < 5; index++ {
			deviceID := DeviceID(fmt.Sprintf("Ascend910-network-%d-device-%d", networkID, index))
			devices = append(devices, Device{
				ID:       deviceID,
				Resource: "huawei.com/Ascend910",
				Node:     "node-ascend-0",
				Domain:   domainID,
				Healthy:  true,
			})
			deviceIDs = append(deviceIDs, deviceID)
		}
		domains = append(domains, LocalDomain{
			ID:        domainID,
			Node:      "node-ascend-0",
			DeviceIDs: deviceIDs,
		})
	}

	snapshot, err := NewSnapshot(devices, domains)
	if err != nil {
		t.Fatalf("create HAMi-shaped topology snapshot: %v", err)
	}

	_, err = snapshot.SelectLocalDomain(NewLedger(snapshot), "node-ascend-0", "huawei.com/Ascend910", 8)
	if !errors.Is(err, ErrNoEligibleLocalDomain) {
		t.Fatalf("expected 5/5 NetworkID fragmentation to reject an 8-device same-domain request, got %v", err)
	}
}

func TestSelectLocalDomainSelectsDeterministicDeviceIDs(t *testing.T) {
	snapshot := newSnapshot(t, map[string]int{
		"hccs-a": 8,
		"hccs-b": 2,
	})

	placement, err := snapshot.SelectLocalDomain(NewLedger(snapshot), "worker-0", accelerator, 8)
	if err != nil {
		t.Fatalf("select local domain: %v", err)
	}

	want := Placement{
		Node:   "worker-0",
		Domain: "hccs-a",
		DeviceIDs: []DeviceID{
			"hccs-a-device-0",
			"hccs-a-device-1",
			"hccs-a-device-2",
			"hccs-a-device-3",
			"hccs-a-device-4",
			"hccs-a-device-5",
			"hccs-a-device-6",
			"hccs-a-device-7",
		},
	}
	if !reflect.DeepEqual(placement, want) {
		t.Fatalf("unexpected placement: got %#v, want %#v", placement, want)
	}
}

func TestTryReserveIsAtomic(t *testing.T) {
	snapshot := newSnapshot(t, map[string]int{"hccs-a": 3})
	ledger := NewLedger(snapshot)

	if _, err := ledger.TryReserve("first", []DeviceID{"hccs-a-device-0"}); err != nil {
		t.Fatalf("reserve first device: %v", err)
	}

	_, err := ledger.TryReserve("second", []DeviceID{"hccs-a-device-1", "hccs-a-device-0"})
	if !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if got := ledger.State("hccs-a-device-1"); got != DeviceFree {
		t.Fatalf("expected unreserved device to remain free, got %q", got)
	}

	if _, err := ledger.TryReserve("third", []DeviceID{"hccs-a-device-1"}); err != nil {
		t.Fatalf("reserve unaffected device: %v", err)
	}
}

func TestReleaseMakesDevicesAvailableAgain(t *testing.T) {
	snapshot := newSnapshot(t, map[string]int{"hccs-a": 2})
	ledger := NewLedger(snapshot)

	if _, err := ledger.TryReserve("plan", []DeviceID{"hccs-a-device-0", "hccs-a-device-1"}); err != nil {
		t.Fatalf("reserve devices: %v", err)
	}
	if err := ledger.Release("plan"); err != nil {
		t.Fatalf("release devices: %v", err)
	}
	if got := ledger.State("hccs-a-device-0"); got != DeviceFree {
		t.Fatalf("expected released device to be free, got %q", got)
	}
	if got := ledger.State("hccs-a-device-1"); got != DeviceFree {
		t.Fatalf("expected released device to be free, got %q", got)
	}
}

func TestNewSnapshotRejectsUnknownDomainMember(t *testing.T) {
	_, err := NewSnapshot([]Device{{
		ID:       "hccs-a-device-0",
		Resource: accelerator,
		Node:     "worker-0",
		Domain:   "hccs-a",
		Healthy:  true,
	}}, []LocalDomain{{
		ID:        "hccs-a",
		Node:      "worker-0",
		DeviceIDs: []DeviceID{"missing-device"},
	}})
	if !errors.Is(err, ErrInvalidTopology) {
		t.Fatalf("expected invalid topology, got %v", err)
	}
}

func TestTopologyCacheUpdatesOnlyAffectedNode(t *testing.T) {
	cache := NewTopologyCache()
	workerADevices, workerADomains := nodeTopology("worker-a", "hccs-a", 2)
	workerBDevices, workerBDomains := nodeTopology("worker-b", "hccs-b", 2)

	if revision, err := cache.Apply(ProviderUpdate{Node: "worker-a", Generation: 1, Devices: workerADevices, Domains: workerADomains}); err != nil || revision != 1 {
		t.Fatalf("apply worker-a revision: got revision %d, err %v", revision, err)
	}
	if revision, err := cache.Apply(ProviderUpdate{Node: "worker-b", Generation: 1, Devices: workerBDevices, Domains: workerBDomains}); err != nil || revision != 2 {
		t.Fatalf("apply worker-b revision: got revision %d, err %v", revision, err)
	}

	updatedWorkerADevices, updatedWorkerADomains := nodeTopology("worker-a", "hccs-a", 1)
	if revision, err := cache.Apply(ProviderUpdate{Node: "worker-a", Generation: 2, Devices: updatedWorkerADevices, Domains: updatedWorkerADomains}); err != nil || revision != 3 {
		t.Fatalf("update worker-a revision: got revision %d, err %v", revision, err)
	}

	snapshot, revision := cache.Snapshot()
	if revision != 3 {
		t.Fatalf("expected revision 3, got %d", revision)
	}
	ledger := NewLedger(snapshot)
	if _, err := snapshot.SelectLocalDomain(ledger, "worker-a", accelerator, 2); !errors.Is(err, ErrNoEligibleLocalDomain) {
		t.Fatalf("expected updated worker-a to reject two devices, got %v", err)
	}
	if _, err := snapshot.SelectLocalDomain(ledger, "worker-b", accelerator, 2); err != nil {
		t.Fatalf("expected unaffected worker-b domain to remain selectable: %v", err)
	}
	if _, err := cache.Apply(ProviderUpdate{Node: "worker-b", Generation: 1, Devices: workerBDevices, Domains: workerBDomains}); !errors.Is(err, ErrStaleProviderUpdate) {
		t.Fatalf("expected stale worker-b update to fail, got %v", err)
	}
}

func TestPlanGangReservesEveryTaskOrNothing(t *testing.T) {
	workerADevices, workerADomains := nodeTopology("worker-a", "hccs-a", 4)
	workerBDevices, workerBDomains := nodeTopology("worker-b", "hccs-b", 4)
	snapshot, err := NewSnapshot(append(workerADevices, workerBDevices...), append(workerADomains, workerBDomains...))
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	ledger := NewLedger(snapshot)
	plan, err := PlanGang(snapshot, ledger, "training", []TaskRequest{
		{ID: "worker-0", CandidateNodes: []string{"worker-a"}, Resource: accelerator, Count: 4},
		{ID: "worker-1", CandidateNodes: []string{"worker-b"}, Resource: accelerator, Count: 4},
	})
	if err != nil {
		t.Fatalf("plan gang: %v", err)
	}
	if len(plan.TaskPlacements) != 2 || len(plan.Reservation.DeviceIDs) != 8 {
		t.Fatalf("unexpected gang plan: %#v", plan)
	}
	for _, deviceID := range plan.Reservation.DeviceIDs {
		if got := ledger.State(deviceID); got != DeviceReserved {
			t.Fatalf("expected device %q to be reserved, got %q", deviceID, got)
		}
	}

	workerCDevices, workerCDomains := nodeTopology("worker-c", "hccs-c", 2)
	limitedSnapshot, err := NewSnapshot(append(workerADevices, workerCDevices...), append(workerADomains, workerCDomains...))
	if err != nil {
		t.Fatalf("create limited snapshot: %v", err)
	}
	limitedLedger := NewLedger(limitedSnapshot)
	_, err = PlanGang(limitedSnapshot, limitedLedger, "incomplete", []TaskRequest{
		{ID: "worker-0", CandidateNodes: []string{"worker-a"}, Resource: accelerator, Count: 4},
		{ID: "worker-1", CandidateNodes: []string{"worker-c"}, Resource: accelerator, Count: 3},
	})
	if !errors.Is(err, ErrGangPlanIncomplete) {
		t.Fatalf("expected incomplete gang plan, got %v", err)
	}
	if got := limitedLedger.State("hccs-a-device-0"); got != DeviceFree {
		t.Fatalf("expected incomplete plan to leave first task device free, got %q", got)
	}
}

func newSnapshot(t *testing.T, domainSizes map[string]int) *Snapshot {
	t.Helper()

	devices := make([]Device, 0)
	domains := make([]LocalDomain, 0, len(domainSizes))
	for domainID, size := range domainSizes {
		deviceIDs := make([]DeviceID, 0, size)
		for index := 0; index < size; index++ {
			deviceID := DeviceID(fmt.Sprintf("%s-device-%d", domainID, index))
			devices = append(devices, Device{
				ID:       deviceID,
				Resource: accelerator,
				Node:     "worker-0",
				Domain:   domainID,
				Healthy:  true,
			})
			deviceIDs = append(deviceIDs, deviceID)
		}
		domains = append(domains, LocalDomain{
			ID:        domainID,
			Node:      "worker-0",
			DeviceIDs: deviceIDs,
		})
	}

	snapshot, err := NewSnapshot(devices, domains)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	return snapshot
}

func nodeTopology(node, domainID string, size int) ([]Device, []LocalDomain) {
	devices := make([]Device, 0, size)
	deviceIDs := make([]DeviceID, 0, size)
	for index := 0; index < size; index++ {
		deviceID := DeviceID(fmt.Sprintf("%s-device-%d", domainID, index))
		devices = append(devices, Device{
			ID:       deviceID,
			Resource: accelerator,
			Node:     node,
			Domain:   domainID,
			Healthy:  true,
		})
		deviceIDs = append(deviceIDs, deviceID)
	}

	return devices, []LocalDomain{{
		ID:        domainID,
		Node:      node,
		DeviceIDs: deviceIDs,
	}}
}
