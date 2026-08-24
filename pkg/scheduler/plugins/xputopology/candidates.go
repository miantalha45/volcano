package xputopology

import "fmt"

// CandidateGroup is a candidate scope supplied by an existing placement
// component. In production this could represent a HyperNode, rack, or another
// already-resolved Node group. This POC keeps it generic and does not create
// HyperNode gradients.
type CandidateGroup struct {
	ID    string
	Nodes []string
}

// DomainFeasibilitySummary is a read-only view of Nodes that currently have a
// local domain capable of satisfying one resource request. It contains no
// reservation and no final device assignment.
type DomainFeasibilitySummary struct {
	eligibleDomainsByNode map[string][]string
}

// BuildDomainFeasibilitySummary prepares device-domain information before
// Node or candidate-group selection. Required placement can use it to remove
// Nodes without a fitting local domain. Preferred placement can use it only as
// scoring input.
func BuildDomainFeasibilitySummary(snapshot *Snapshot, ledger *Ledger, resource string, count int) (*DomainFeasibilitySummary, error) {
	if snapshot == nil || ledger == nil || resource == "" || count < 1 {
		return nil, fmt.Errorf("%w: snapshot, ledger, resource, and a positive count are required", ErrNoEligibleLocalDomain)
	}

	summary := &DomainFeasibilitySummary{
		eligibleDomainsByNode: make(map[string][]string),
	}
	for key, domainIDs := range snapshot.domainsByNodeResource {
		if key.resource != resource {
			continue
		}
		for _, domainID := range domainIDs {
			domain := snapshot.domains[domainID]
			freeDevices := 0
			for _, deviceID := range domain.DeviceIDs {
				device := snapshot.devices[deviceID]
				if device.Healthy && device.Resource == resource && ledger.IsFree(deviceID) {
					freeDevices++
				}
			}
			if freeDevices >= count {
				summary.eligibleDomainsByNode[key.node] = append(summary.eligibleDomainsByNode[key.node], domainID)
			}
		}
	}

	return summary, nil
}

// FilterNodes retains the input order while removing Nodes with no eligible
// local domain for this summary.
func (summary *DomainFeasibilitySummary) FilterNodes(nodes []string) []string {
	if summary == nil {
		return nil
	}

	filtered := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if len(summary.eligibleDomainsByNode[node]) > 0 {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

// FilterCandidateGroups retains a group when at least one of its member Nodes
// has an eligible local domain. It is the POC representation of passing
// domain-related information to an existing HyperNode or Node selector.
func (summary *DomainFeasibilitySummary) FilterCandidateGroups(groups []CandidateGroup) []CandidateGroup {
	if summary == nil {
		return nil
	}

	filtered := make([]CandidateGroup, 0, len(groups))
	for _, group := range groups {
		nodes := summary.FilterNodes(group.Nodes)
		if len(nodes) == 0 {
			continue
		}
		filtered = append(filtered, CandidateGroup{ID: group.ID, Nodes: nodes})
	}
	return filtered
}
