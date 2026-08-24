package xputopology

import (
	"errors"
	"fmt"
	"sort"
)

var ErrGangPlanIncomplete = errors.New("incomplete xpu gang plan")

const defaultGangPlanAlternatives = 32

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
	return planGangWithLimit(snapshot, ledger, reservationID, requests, defaultGangPlanAlternatives)
}

func planGangWithLimit(snapshot *Snapshot, ledger *Ledger, reservationID string, requests []TaskRequest, alternatives int) (GangPlan, error) {
	if snapshot == nil || ledger == nil || reservationID == "" || len(requests) == 0 {
		return GangPlan{}, fmt.Errorf("%w: snapshot, ledger, reservation id, and task requests are required", ErrGangPlanIncomplete)
	}
	if alternatives < 1 {
		return GangPlan{}, fmt.Errorf("%w: positive alternative limit is required", ErrGangPlanIncomplete)
	}

	tasks := make(map[string]struct{}, len(requests))
	normalizedRequests := make([]TaskRequest, 0, len(requests))

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
		request.CandidateNodes = candidateNodes
		normalizedRequests = append(normalizedRequests, request)
	}

	planner := gangPlanner{
		snapshot:     snapshot,
		ledger:       ledger,
		alternatives: alternatives,
		selected:     make(map[DeviceID]struct{}),
	}
	placements, err := planner.plan(normalizedRequests, 0)
	if err != nil {
		return GangPlan{}, err
	}

	deviceIDs := make([]DeviceID, 0)
	for _, placement := range placements {
		deviceIDs = append(deviceIDs, placement.DeviceIDs...)
	}
	reservation, err := ledger.TryReserve(reservationID, deviceIDs)
	if err != nil {
		return GangPlan{}, err
	}

	return GangPlan{TaskPlacements: placements, Reservation: reservation}, nil
}

type gangPlanner struct {
	snapshot     *Snapshot
	ledger       *Ledger
	alternatives int
	selected     map[DeviceID]struct{}
}

func (planner *gangPlanner) plan(requests []TaskRequest, index int) ([]TaskPlacement, error) {
	if index == len(requests) {
		return nil, nil
	}

	request := requests[index]
	placements, err := planner.taskPlacements(request)
	if err != nil {
		return nil, fmt.Errorf("%w: task %q: %w", ErrGangPlanIncomplete, request.ID, err)
	}

	for _, placement := range placements {
		if planner.alternatives == 0 {
			return nil, fmt.Errorf("%w: search reached the alternative limit", ErrGangPlanIncomplete)
		}
		planner.alternatives--

		for _, deviceID := range placement.DeviceIDs {
			planner.selected[deviceID] = struct{}{}
		}
		remainder, err := planner.plan(requests, index+1)
		for _, deviceID := range placement.DeviceIDs {
			delete(planner.selected, deviceID)
		}
		if err == nil {
			return append([]TaskPlacement{{TaskID: request.ID, Placement: placement}}, remainder...), nil
		}
	}

	return nil, fmt.Errorf("%w: task %q has no complete placement", ErrGangPlanIncomplete, request.ID)
}

func (planner *gangPlanner) taskPlacements(request TaskRequest) ([]Placement, error) {
	placements := make([]Placement, 0, len(request.CandidateNodes))
	for _, node := range request.CandidateNodes {
		placement, err := planner.snapshot.selectLocalDomain(planner.ledger, node, request.Resource, request.Count, planner.selected)
		if err == nil {
			placements = append(placements, placement)
			continue
		}
		if !errors.Is(err, ErrNoEligibleLocalDomain) {
			return nil, err
		}
	}
	if len(placements) == 0 {
		return nil, ErrNoEligibleLocalDomain
	}

	return placements, nil
}
