package xputopology

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	ErrInvalidTopology       = errors.New("invalid xpu topology")
	ErrNoEligibleLocalDomain = errors.New("no eligible local domain")
	ErrReservationConflict   = errors.New("xpu reservation conflict")
	ErrUnknownReservation    = errors.New("unknown xpu reservation")
)

type DeviceID string

type Device struct {
	ID       DeviceID
	Resource string
	Node     string
	Domain   string
	Healthy  bool
}

type LocalDomain struct {
	ID        string
	Node      string
	DeviceIDs []DeviceID
}

type Placement struct {
	Node      string
	Domain    string
	DeviceIDs []DeviceID
}

type Snapshot struct {
	devices               map[DeviceID]Device
	domains               map[string]LocalDomain
	domainsByNodeResource map[nodeResourceKey][]string
}

type nodeResourceKey struct {
	node     string
	resource string
}

func NewSnapshot(devices []Device, domains []LocalDomain) (*Snapshot, error) {
	snapshot := &Snapshot{
		devices:               make(map[DeviceID]Device, len(devices)),
		domains:               make(map[string]LocalDomain, len(domains)),
		domainsByNodeResource: make(map[nodeResourceKey][]string),
	}

	for _, device := range devices {
		if device.ID == "" || device.Resource == "" || device.Node == "" || device.Domain == "" {
			return nil, fmt.Errorf("%w: device id, resource, node, and domain are required", ErrInvalidTopology)
		}
		if _, found := snapshot.devices[device.ID]; found {
			return nil, fmt.Errorf("%w: duplicate device %q", ErrInvalidTopology, device.ID)
		}
		snapshot.devices[device.ID] = device
	}

	for _, domain := range domains {
		if domain.ID == "" || domain.Node == "" || len(domain.DeviceIDs) == 0 {
			return nil, fmt.Errorf("%w: domain id, node, and device ids are required", ErrInvalidTopology)
		}
		if _, found := snapshot.domains[domain.ID]; found {
			return nil, fmt.Errorf("%w: duplicate local domain %q", ErrInvalidTopology, domain.ID)
		}

		resources := make(map[string]struct{})
		members := make(map[DeviceID]struct{}, len(domain.DeviceIDs))
		for _, deviceID := range domain.DeviceIDs {
			device, found := snapshot.devices[deviceID]
			if !found {
				return nil, fmt.Errorf("%w: domain %q references unknown device %q", ErrInvalidTopology, domain.ID, deviceID)
			}
			if device.Node != domain.Node || device.Domain != domain.ID {
				return nil, fmt.Errorf("%w: device %q does not belong to domain %q on node %q", ErrInvalidTopology, deviceID, domain.ID, domain.Node)
			}
			if _, found := members[deviceID]; found {
				return nil, fmt.Errorf("%w: duplicate device %q in domain %q", ErrInvalidTopology, deviceID, domain.ID)
			}
			members[deviceID] = struct{}{}
			resources[device.Resource] = struct{}{}
		}

		snapshot.domains[domain.ID] = LocalDomain{
			ID:        domain.ID,
			Node:      domain.Node,
			DeviceIDs: append([]DeviceID(nil), domain.DeviceIDs...),
		}
		for resource := range resources {
			key := nodeResourceKey{node: domain.Node, resource: resource}
			snapshot.domainsByNodeResource[key] = append(snapshot.domainsByNodeResource[key], domain.ID)
		}
	}

	for key := range snapshot.domainsByNodeResource {
		sort.Strings(snapshot.domainsByNodeResource[key])
	}

	return snapshot, nil
}

func NewLedger(snapshot *Snapshot) *Ledger {
	states := make(map[DeviceID]DeviceState, len(snapshot.devices))
	for deviceID, device := range snapshot.devices {
		if device.Healthy {
			states[deviceID] = DeviceFree
			continue
		}
		states[deviceID] = DeviceUnhealthy
	}

	return &Ledger{
		states:       states,
		reservations: make(map[string]Reservation),
	}
}

func (snapshot *Snapshot) SelectLocalDomain(ledger *Ledger, node, resource string, count int) (Placement, error) {
	if ledger == nil || node == "" || resource == "" || count < 1 {
		return Placement{}, fmt.Errorf("%w: node, resource, ledger, and a positive count are required", ErrNoEligibleLocalDomain)
	}
	return snapshot.selectLocalDomain(ledger, node, resource, count, nil)
}

func (snapshot *Snapshot) selectLocalDomain(ledger *Ledger, node, resource string, count int, excluded map[DeviceID]struct{}) (Placement, error) {
	for _, domainID := range snapshot.domainsByNodeResource[nodeResourceKey{node: node, resource: resource}] {
		domain := snapshot.domains[domainID]
		deviceIDs := make([]DeviceID, 0, len(domain.DeviceIDs))
		for _, deviceID := range domain.DeviceIDs {
			device := snapshot.devices[deviceID]
			if _, found := excluded[deviceID]; found {
				continue
			}
			if device.Resource == resource && device.Healthy && ledger.IsFree(deviceID) {
				deviceIDs = append(deviceIDs, deviceID)
			}
		}
		sort.Slice(deviceIDs, func(left, right int) bool {
			return deviceIDs[left] < deviceIDs[right]
		})
		if len(deviceIDs) >= count {
			return Placement{
				Node:      node,
				Domain:    domainID,
				DeviceIDs: append([]DeviceID(nil), deviceIDs[:count]...),
			}, nil
		}
	}

	return Placement{}, fmt.Errorf("%w: node %q has no %q domain with %d free healthy devices", ErrNoEligibleLocalDomain, node, resource, count)
}

type DeviceState string

const (
	DeviceFree      DeviceState = "free"
	DeviceReserved  DeviceState = "reserved"
	DeviceAllocated DeviceState = "allocated"
	DeviceUnhealthy DeviceState = "unhealthy"
)

type Reservation struct {
	ID        string
	DeviceIDs []DeviceID
}

type Ledger struct {
	mu           sync.Mutex
	states       map[DeviceID]DeviceState
	reservations map[string]Reservation
}

func (ledger *Ledger) IsFree(deviceID DeviceID) bool {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.states[deviceID] == DeviceFree
}

func (ledger *Ledger) State(deviceID DeviceID) DeviceState {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.states[deviceID]
}

func (ledger *Ledger) TryReserve(reservationID string, deviceIDs []DeviceID) (Reservation, error) {
	if reservationID == "" || len(deviceIDs) == 0 {
		return Reservation{}, fmt.Errorf("%w: reservation id and device ids are required", ErrReservationConflict)
	}

	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	if _, found := ledger.reservations[reservationID]; found {
		return Reservation{}, fmt.Errorf("%w: reservation %q already exists", ErrReservationConflict, reservationID)
	}

	seen := make(map[DeviceID]struct{}, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		if _, found := seen[deviceID]; found {
			return Reservation{}, fmt.Errorf("%w: duplicate device %q", ErrReservationConflict, deviceID)
		}
		seen[deviceID] = struct{}{}
		if ledger.states[deviceID] != DeviceFree {
			return Reservation{}, fmt.Errorf("%w: device %q is %q", ErrReservationConflict, deviceID, ledger.states[deviceID])
		}
	}

	reservation := Reservation{
		ID:        reservationID,
		DeviceIDs: append([]DeviceID(nil), deviceIDs...),
	}
	for _, deviceID := range reservation.DeviceIDs {
		ledger.states[deviceID] = DeviceReserved
	}
	ledger.reservations[reservationID] = reservation

	return reservation, nil
}

func (ledger *Ledger) Release(reservationID string) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	reservation, found := ledger.reservations[reservationID]
	if !found {
		return fmt.Errorf("%w: %q", ErrUnknownReservation, reservationID)
	}

	for _, deviceID := range reservation.DeviceIDs {
		if ledger.states[deviceID] == DeviceReserved {
			ledger.states[deviceID] = DeviceFree
		}
	}
	delete(ledger.reservations, reservationID)

	return nil
}
