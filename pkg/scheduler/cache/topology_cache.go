/*
Copyright 2026 The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

import (
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/types"

	schedulingapi "volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/topology/adapter"
	"volcano.sh/volcano/pkg/scheduler/topology/provider"
)

// deviceTopologyState is the mutable, cache-owned source of immutable device
// topology snapshots. SchedulerCache.Mutex protects every field.
type deviceTopologyState struct {
	revision          schedulingapi.TopologyRevision
	facts             map[schedulingapi.ProviderNodeKey]schedulingapi.NodeTopologyFacts
	readiness         map[schedulingapi.ProviderNodeKey]schedulingapi.ProviderNodeSyncState
	ledger            *deviceTopologyAvailabilityLedger
	reservationOwners map[schedulingapi.DeviceID]deviceTopologyReservationOwner
	snapshot          *schedulingapi.TopologySnapshot
}

// WithDeviceTopologyProviderRegistry configures the immutable authoritative
// provider ownership map used to accept device topology updates. A nil
// registry disables provider updates while preserving an empty snapshot.
func WithDeviceTopologyProviderRegistry(registry *provider.Registry) Option {
	return func(cache *SchedulerCache) {
		cache.deviceTopologyProviderRegistry = registry
	}
}

// ConfigureDeviceTopologyProviders installs the immutable provider ownership
// registry and source instances before SchedulerCache begins processing Nodes.
// A nil registry with no sources explicitly disables topology source sync.
func (sc *SchedulerCache) ConfigureDeviceTopologyProviders(registry *provider.Registry, sources []provider.NodeTopologyProvider) error {
	if err := validateDeviceTopologyProviderConfiguration(registry, sources); err != nil {
		return err
	}

	sc.Mutex.Lock()
	defer sc.Mutex.Unlock()
	if sc.deviceTopologyProvidersFrozen {
		return fmt.Errorf("device topology providers cannot be configured after SchedulerCache starts")
	}
	if sc.deviceTopologyProvidersConfigured {
		return fmt.Errorf("device topology providers are already configured")
	}

	sc.deviceTopologyProviderRegistry = registry
	sc.deviceTopologyProviders = append([]provider.NodeTopologyProvider(nil), sources...)
	sc.deviceTopologyProvidersConfigured = true
	return nil
}

// freezeDeviceTopologyProviders prevents changes to source ownership once the
// cache begins informer processing. The caller does not need to hold the lock.
func (sc *SchedulerCache) freezeDeviceTopologyProviders() {
	sc.Mutex.Lock()
	defer sc.Mutex.Unlock()
	sc.deviceTopologyProvidersFrozen = true
}

// deviceTopologyProviderSources returns a stable copy of the configured
// sources so Node synchronization can call providers without holding the cache
// mutex.
func (sc *SchedulerCache) deviceTopologyProviderSources() []provider.NodeTopologyProvider {
	sc.Mutex.Lock()
	defer sc.Mutex.Unlock()
	return append([]provider.NodeTopologyProvider(nil), sc.deviceTopologyProviders...)
}

// validateDeviceTopologyProviderConfiguration ensures every configured source
// corresponds to exactly one entry in the authoritative ownership registry.
func validateDeviceTopologyProviderConfiguration(registry *provider.Registry, sources []provider.NodeTopologyProvider) error {
	if len(sources) == 0 {
		if registry != nil && len(registry.ProviderIDs()) > 0 {
			return fmt.Errorf("device topology provider registry has no configured sources")
		}
		return nil
	}
	if registry == nil {
		return fmt.Errorf("device topology providers require an ownership registry")
	}

	registeredIDs := registry.ProviderIDs()
	if len(sources) != len(registeredIDs) {
		return fmt.Errorf("device topology source count %d does not match registry provider count %d", len(sources), len(registeredIDs))
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if source == nil {
			return fmt.Errorf("device topology provider source is required")
		}
		providerID := source.ID()
		if providerID == "" {
			return fmt.Errorf("device topology provider source ID is required")
		}
		if _, exists := seen[providerID]; exists {
			return fmt.Errorf("device topology provider source %q is configured more than once", providerID)
		}
		seen[providerID] = struct{}{}
	}
	for _, providerID := range registeredIDs {
		if _, exists := seen[providerID]; !exists {
			return fmt.Errorf("device topology registry provider %q has no configured source", providerID)
		}
	}
	return nil
}

// WithDeviceTopologyAdapterRegistry configures the immutable allocation
// adapter ownership map used to reserve exact device IDs for hard topology.
// A nil registry leaves topology facts observable but prevents reservations.
func WithDeviceTopologyAdapterRegistry(registry *adapter.Registry) Option {
	return func(cache *SchedulerCache) {
		cache.deviceTopologyAdapterRegistry = registry
	}
}

// newDeviceTopologyState creates an empty topology view. Unknown provider and
// Node pairs are pending by the TopologySnapshot contract.
func newDeviceTopologyState() *deviceTopologyState {
	state := &deviceTopologyState{
		facts:             make(map[schedulingapi.ProviderNodeKey]schedulingapi.NodeTopologyFacts),
		readiness:         make(map[schedulingapi.ProviderNodeKey]schedulingapi.ProviderNodeSyncState),
		ledger:            newDeviceTopologyAvailabilityLedger(),
		reservationOwners: make(map[schedulingapi.DeviceID]deviceTopologyReservationOwner),
	}
	state.snapshot = schedulingapi.NewTopologySnapshotWithAvailability(state.revision, state.facts, state.readiness, state.ledger.snapshot())
	return state
}

// ApplyProviderNodeUpdate validates and atomically applies a provider's
// current facts for one cached Node. The update must match the cache's current
// Node UID and opaque resource version.
func (sc *SchedulerCache) ApplyProviderNodeUpdate(update provider.ProviderNodeUpdate) error {
	if err := update.Validate(); err != nil {
		return provider.NewSourceError(provider.SourceErrorInvalidTopology, err)
	}

	sc.Mutex.Lock()
	defer sc.Mutex.Unlock()

	if err := sc.validateTopologyProviderOwnership(update); err != nil {
		return provider.NewSourceError(provider.SourceErrorUnauthorizedProvider, err)
	}
	if err := sc.validateCurrentTopologyNode(update); err != nil {
		return err
	}
	return sc.ensureDeviceTopologyState().apply(update)
}

// DeviceTopologySnapshot returns the current immutable topology view. The
// caller must treat the returned snapshot as read-only.
func (sc *SchedulerCache) DeviceTopologySnapshot() *schedulingapi.TopologySnapshot {
	sc.Mutex.Lock()
	defer sc.Mutex.Unlock()
	return sc.ensureDeviceTopologyState().snapshot
}

// ProviderNodeReadiness returns the current provider synchronization state for
// one exact Node UID. Unknown pairs are pending.
func (sc *SchedulerCache) ProviderNodeReadiness(providerID string, nodeUID types.UID) schedulingapi.ProviderNodeSyncState {
	sc.Mutex.Lock()
	defer sc.Mutex.Unlock()
	return sc.ensureDeviceTopologyState().snapshot.ProviderNodeReadiness(providerID, nodeUID)
}

// ensureDeviceTopologyState creates the lazily allocated topology state. The
// caller must hold SchedulerCache.Mutex.
func (sc *SchedulerCache) ensureDeviceTopologyState() *deviceTopologyState {
	if sc.deviceTopology == nil {
		sc.deviceTopology = newDeviceTopologyState()
	}
	return sc.deviceTopology
}

// validateCurrentTopologyNode verifies that a provider did not race a Node
// replacement or newer informer update.
func (sc *SchedulerCache) validateCurrentTopologyNode(update provider.ProviderNodeUpdate) error {
	nodeInfo, exists := sc.Nodes[update.NodeName]
	if !exists || nodeInfo == nil || nodeInfo.Node == nil {
		return fmt.Errorf("provider update references Node %q that is not in SchedulerCache", update.NodeName)
	}
	node := nodeInfo.Node
	if node.Name != update.NodeName || node.UID != update.NodeUID || node.ResourceVersion != update.NodeResourceVersion {
		return fmt.Errorf("provider update for Node %q does not match the current Node UID and resource version", update.NodeName)
	}
	return nil
}

// validateTopologyProviderOwnership verifies that configuration selected the
// update provider for every resource it reports. The caller must hold
// SchedulerCache.Mutex.
func (sc *SchedulerCache) validateTopologyProviderOwnership(update provider.ProviderNodeUpdate) error {
	if sc.deviceTopologyProviderRegistry == nil {
		return fmt.Errorf("no device topology provider registry is configured")
	}
	return sc.deviceTopologyProviderRegistry.ValidateOwnership(update)
}

// apply updates complete source facts and publishes a new immutable view only
// when the externally visible topology or readiness state changes.
func (state *deviceTopologyState) apply(update provider.ProviderNodeUpdate) error {
	key := schedulingapi.ProviderNodeKey{ProviderID: update.ProviderID, NodeUID: update.NodeUID}
	if update.Operation == provider.ClearFacts {
		changed := false
		if _, exists := state.facts[key]; exists {
			delete(state.facts, key)
			changed = true
		}
		if state.readiness[key] != schedulingapi.ProviderNodeSynced {
			state.readiness[key] = schedulingapi.ProviderNodeSynced
			changed = true
		}
		if changed {
			state.publish()
		}
		return nil
	}

	facts := update.Facts.Clone()
	previous, exists := state.facts[key]
	if exists {
		if update.SourceGeneration < previous.SourceGeneration {
			return provider.NewSourceError(provider.SourceErrorStaleGeneration, fmt.Errorf("source generation %d is older than accepted generation %d", update.SourceGeneration, previous.SourceGeneration))
		}
		if update.SourceGeneration == previous.SourceGeneration && !schedulingapi.SameTopologyContent(previous, facts) {
			return provider.NewSourceError(provider.SourceErrorGenerationReuseWithDifferentFacts, fmt.Errorf("source generation %d describes different topology content", update.SourceGeneration))
		}
	}
	if err := state.validateNoTopologyConflicts(key, facts); err != nil {
		return provider.NewSourceError(provider.SourceErrorInvalidTopology, err)
	}

	changed := !exists || !reflect.DeepEqual(previous, facts) || state.readiness[key] != schedulingapi.ProviderNodeSynced
	if !changed {
		return nil
	}
	state.facts[key] = facts
	state.readiness[key] = schedulingapi.ProviderNodeSynced
	state.publish()
	return nil
}

// removeNode discards every provider fact and readiness result tied to a Node
// UID, invalidating any fabric that depended on those facts in the next view.
func (state *deviceTopologyState) removeNode(nodeUID types.UID) {
	changed := false
	for key := range state.facts {
		if key.NodeUID == nodeUID {
			delete(state.facts, key)
			changed = true
		}
	}
	for key := range state.readiness {
		if key.NodeUID == nodeUID {
			delete(state.readiness, key)
			changed = true
		}
	}
	if changed {
		state.publish()
	}
}

// validateNoTopologyConflicts rejects provider-owned IDs that another accepted
// fact set already claims. The candidate replaces only its own fact set.
func (state *deviceTopologyState) validateNoTopologyConflicts(candidateKey schedulingapi.ProviderNodeKey, candidate schedulingapi.NodeTopologyFacts) error {
	candidateDevices := make(map[schedulingapi.DeviceID]struct{}, len(candidate.Devices))
	for _, device := range candidate.Devices {
		candidateDevices[device.ID] = struct{}{}
	}
	candidateFabrics := make(map[schedulingapi.FabricDomainID]struct{}, len(candidate.Fabrics))
	for _, fabric := range candidate.Fabrics {
		candidateFabrics[fabric.ID] = struct{}{}
	}

	for key, facts := range state.facts {
		if key == candidateKey {
			continue
		}
		for _, device := range facts.Devices {
			if _, exists := candidateDevices[device.ID]; exists {
				return fmt.Errorf("device %q is already claimed by provider %q on Node UID %q", device.ID.Value, key.ProviderID, key.NodeUID)
			}
		}
		for _, fabric := range facts.Fabrics {
			if _, exists := candidateFabrics[fabric.ID]; exists {
				return fmt.Errorf("fabric %q is already owned by provider %q on Node UID %q", fabric.ID.Value, key.ProviderID, key.NodeUID)
			}
		}
	}
	return nil
}

// publish increments the topology revision and rebuilds its immutable indexed
// view from the accepted fact sets.
func (state *deviceTopologyState) publish() {
	state.ledger.reconcile(state.facts)
	state.revision++
	state.snapshot = schedulingapi.NewTopologySnapshotWithAvailability(state.revision, state.facts, state.readiness, state.ledger.snapshot())
}
