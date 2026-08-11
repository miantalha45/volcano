package xputopology

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidProviderUpdate = errors.New("invalid xpu provider update")
	ErrStaleProviderUpdate   = errors.New("stale xpu provider update")
)

type ProviderUpdate struct {
	Node       string
	Generation uint64
	Devices    []Device
	Domains    []LocalDomain
}

type TopologyCache struct {
	mu          sync.RWMutex
	revision    uint64
	generations map[string]uint64
	snapshot    *Snapshot
}

func NewTopologyCache() *TopologyCache {
	snapshot, _ := NewSnapshot(nil, nil)
	return &TopologyCache{
		generations: make(map[string]uint64),
		snapshot:    snapshot,
	}
}

func (cache *TopologyCache) Apply(update ProviderUpdate) (uint64, error) {
	if update.Node == "" || update.Generation == 0 {
		return 0, fmt.Errorf("%w: node and generation are required", ErrInvalidProviderUpdate)
	}
	for _, device := range update.Devices {
		if device.Node != update.Node {
			return 0, fmt.Errorf("%w: device %q belongs to node %q, not %q", ErrInvalidProviderUpdate, device.ID, device.Node, update.Node)
		}
	}
	for _, domain := range update.Domains {
		if domain.Node != update.Node {
			return 0, fmt.Errorf("%w: domain %q belongs to node %q, not %q", ErrInvalidProviderUpdate, domain.ID, domain.Node, update.Node)
		}
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if generation := cache.generations[update.Node]; generation >= update.Generation {
		return cache.revision, fmt.Errorf("%w: node %q generation %d is not newer than %d", ErrStaleProviderUpdate, update.Node, update.Generation, generation)
	}

	devices := make([]Device, 0, len(cache.snapshot.devices)+len(update.Devices))
	for _, device := range cache.snapshot.devices {
		if device.Node != update.Node {
			devices = append(devices, device)
		}
	}
	devices = append(devices, update.Devices...)

	domains := make([]LocalDomain, 0, len(cache.snapshot.domains)+len(update.Domains))
	for _, domain := range cache.snapshot.domains {
		if domain.Node != update.Node {
			domains = append(domains, domain)
		}
	}
	domains = append(domains, update.Domains...)

	snapshot, err := NewSnapshot(devices, domains)
	if err != nil {
		return cache.revision, err
	}

	cache.revision++
	cache.generations[update.Node] = update.Generation
	cache.snapshot = snapshot

	return cache.revision, nil
}

func (cache *TopologyCache) Snapshot() (*Snapshot, uint64) {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.snapshot, cache.revision
}
