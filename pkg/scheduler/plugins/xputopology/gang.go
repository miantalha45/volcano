package xputopology

import (
	"errors"
	"fmt"
	"sort"
)

var ErrGangPlanIncomplete = errors.New("incomplete xpu gang plan")

type TaskRequest struct {
	ID             string
	CandidateNodes []string
	Resource       string
	Count          int
}

type TaskPlacement struct {
	TaskID string
	Placement
}

type GangPlan struct {
	TaskPlacements []TaskPlacement
	Reservation    Reservation
}

func PlanGang(snapshot *Snapshot, ledger *Ledger, reservationID string, requests []TaskRequest) (GangPlan, error) {
	if snapshot == nil || ledger == nil || reservationID == "" || len(requests) == 0 {
		return GangPlan{}, fmt.Errorf("%w: snapshot, ledger, reservation id, and task requests are required", ErrGangPlanIncomplete)
	}

	placements := make([]TaskPlacement, 0, len(requests))
	selected := make(map[DeviceID]struct{})
	tasks := make(map[string]struct{}, len(requests))

	for _, request := range requests {
		if request.ID == "" || request.Resource == "" || request.Count < 1 || len(request.CandidateNodes) == 0 {
			return GangPlan{}, fmt.Errorf("%w: task id, resource, positive count, and candidate nodes are required", ErrGangPlanIncomplete)
		}
		if _, found := tasks[request.ID]; found {
			return GangPlan{}, fmt.Errorf("%w: duplicate task %q", ErrGangPlanIncomplete, request.ID)
		}
		tasks[request.ID] = struct{}{}

		candidateNodes := append([]string(nil), request.CandidateNodes...)
		sort.Strings(candidateNodes)
		placement, err := findTaskPlacement(snapshot, ledger, candidateNodes, request.Resource, request.Count, selected)
		if err != nil {
			return GangPlan{}, fmt.Errorf("%w: task %q: %w", ErrGangPlanIncomplete, request.ID, err)
		}
		placements = append(placements, TaskPlacement{TaskID: request.ID, Placement: placement})
		for _, deviceID := range placement.DeviceIDs {
			selected[deviceID] = struct{}{}
		}
	}

	deviceIDs := make([]DeviceID, 0, len(selected))
	for _, placement := range placements {
		deviceIDs = append(deviceIDs, placement.DeviceIDs...)
	}
	reservation, err := ledger.TryReserve(reservationID, deviceIDs)
	if err != nil {
		return GangPlan{}, err
	}

	return GangPlan{TaskPlacements: placements, Reservation: reservation}, nil
}

func findTaskPlacement(snapshot *Snapshot, ledger *Ledger, candidateNodes []string, resource string, count int, selected map[DeviceID]struct{}) (Placement, error) {
	for _, node := range candidateNodes {
		placement, err := snapshot.selectLocalDomain(ledger, node, resource, count, selected)
		if err == nil {
			return placement, nil
		}
		if !errors.Is(err, ErrNoEligibleLocalDomain) {
			return Placement{}, err
		}
	}
	return Placement{}, ErrNoEligibleLocalDomain
}
