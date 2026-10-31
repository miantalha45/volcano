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
	"context"
	"errors"
	"fmt"
	"sort"

	schedulingapi "volcano.sh/volcano/pkg/scheduler/api"
)

type preparedBatchBinder struct {
	name        string
	preparation BatchBindPreparation
}

type preparedBatchBinders struct {
	preparations []preparedBatchBinder
	committed    bool
	rolledBack   bool
}

type batchBindTaskState struct {
	bindContext         *BindContext
	job                 *schedulingapi.JobInfo
	task                *schedulingapi.TaskInfo
	node                *schedulingapi.NodeInfo
	originalStatus      schedulingapi.TaskStatus
	originalNodeName    string
	originalNumaInfo    *schedulingapi.TopologyInfo
	originalAnnotations map[string]string
	previouslyOccupied  bool
	resourceDecisionSet bool
	statusUpdated       bool
	addedToNode         bool
}

// PrepareBindBatch asks every registered batch pre-binder to prepare one
// all-or-nothing bind transaction. A nil result means no registered component
// needs transactional preparation for these contexts, so callers may use the
// legacy per-Pod bind path unchanged.
func (sc *SchedulerCache) PrepareBindBatch(ctx context.Context, bindContexts []*BindContext) (BatchBindPreparation, error) {
	if sc == nil {
		return nil, fmt.Errorf("batch bind preparation requires a SchedulerCache")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if sc.binderRegistry == nil {
		return nil, nil
	}

	preBinders := sc.binderRegistry.getRegisteredBatchPreBinders()
	names := make([]string, 0, len(preBinders))
	for name := range preBinders {
		names = append(names, name)
	}
	sort.Strings(names)

	prepared := &preparedBatchBinders{}
	for _, name := range names {
		preBinder := preBinders[name]
		if preBinder == nil {
			continue
		}

		preparation, err := preBinder.PrepareBindBatch(ctx, bindContexts)
		if err != nil {
			if preparation != nil {
				prepared.preparations = append(prepared.preparations, preparedBatchBinder{name: name, preparation: preparation})
			}
			rollbackErr := prepared.Rollback(ctx)
			if rollbackErr != nil {
				return nil, errors.Join(fmt.Errorf("prepare batch bind with %q: %w", name, err), rollbackErr)
			}
			return nil, fmt.Errorf("prepare batch bind with %q: %w", name, err)
		}
		if preparation != nil {
			prepared.preparations = append(prepared.preparations, preparedBatchBinder{name: name, preparation: preparation})
		}
	}

	if len(prepared.preparations) == 0 {
		return nil, nil
	}
	return prepared, nil
}

// Commit commits every prepared external bind participant in registration
// order. The caller must invoke Rollback if any participant rejects commit.
func (prepared *preparedBatchBinders) Commit(ctx context.Context) error {
	if prepared == nil {
		return nil
	}
	if prepared.rolledBack {
		return fmt.Errorf("batch bind preparation has already been rolled back")
	}
	if prepared.committed {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, participant := range prepared.preparations {
		if err := participant.preparation.Commit(ctx); err != nil {
			return fmt.Errorf("commit batch bind preparation %q: %w", participant.name, err)
		}
	}
	prepared.committed = true
	return nil
}

// Rollback compensates prepared batch participants in reverse registration
// order. It is idempotent so both a failing participant and its caller can
// safely request cleanup.
func (prepared *preparedBatchBinders) Rollback(ctx context.Context) error {
	if prepared == nil || prepared.rolledBack {
		return nil
	}
	prepared.rolledBack = true
	if ctx == nil {
		ctx = context.Background()
	}

	var rollbackErrors []error
	for index := len(prepared.preparations) - 1; index >= 0; index-- {
		participant := prepared.preparations[index]
		if err := participant.preparation.Rollback(ctx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback batch bind preparation %q: %w", participant.name, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

// AddBindBatch atomically changes every accepted cache Task to Binding and
// queues the contexts as one bind item. If any context cannot be admitted,
// it restores all earlier cache mutations and submits no Pod to the bind path.
func (sc *SchedulerCache) AddBindBatch(bindContexts []*BindContext) error {
	if sc == nil {
		return fmt.Errorf("batch bind requires a SchedulerCache")
	}
	if len(bindContexts) == 0 {
		return nil
	}
	if sc.BindBatchFlowChannel == nil {
		return fmt.Errorf("batch bind channel is not configured")
	}

	sc.Mutex.Lock()
	defer sc.Mutex.Unlock()

	states := make([]batchBindTaskState, 0, len(bindContexts))
	seenTasks := make(map[schedulingapi.TaskID]struct{}, len(bindContexts))
	for _, bindContext := range bindContexts {
		if bindContext == nil || bindContext.TaskInfo == nil {
			return fmt.Errorf("batch bind contains an empty Task context")
		}
		job, task, err := sc.findJobAndTask(bindContext.TaskInfo)
		if err != nil {
			return err
		}
		if _, exists := seenTasks[task.UID]; exists {
			return fmt.Errorf("batch bind repeats Task %q", task.UID)
		}
		seenTasks[task.UID] = struct{}{}

		node, found := sc.Nodes[bindContext.TaskInfo.NodeName]
		if !found || node == nil {
			return fmt.Errorf("failed to bind Task %v to host %v, host does not exist", task.UID, bindContext.TaskInfo.NodeName)
		}

		states = append(states, batchBindTaskState{
			bindContext:         bindContext,
			job:                 job,
			task:                task,
			node:                node,
			originalStatus:      task.Status,
			originalNodeName:    task.NodeName,
			originalNumaInfo:    cloneTopologyInfo(task.NumaInfo),
			originalAnnotations: cloneTaskAnnotations(bindContext.TaskInfo.PodAnnotations),
			previouslyOccupied:  task.OccupiesTopologyDomain(),
		})
	}

	for index := range states {
		state := &states[index]
		state.job.UpdateTaskStatus(state.task, schedulingapi.Binding)
		state.statusUpdated = true

		state.resourceDecisionSet = true
		if err := state.bindContext.TaskInfo.SetPodResourceDecision(); err != nil {
			sc.rollbackBindBatch(states[:index+1])
			return fmt.Errorf("set task %v/%v resource decision failed: %w", state.task.Namespace, state.task.Name, err)
		}
		state.task.NumaInfo = cloneTopologyInfo(state.bindContext.TaskInfo.NumaInfo)

		if err := state.node.AddTask(state.task); err != nil {
			sc.rollbackBindBatch(states[:index+1])
			return err
		}
		state.addedToNode = true
	}

	sc.BindBatchFlowChannel <- append([]*BindContext(nil), bindContexts...)
	for _, state := range states {
		if state.originalNodeName != state.task.NodeName || state.previouslyOccupied != state.task.OccupiesTopologyDomain() {
			state.job.TaskPlacementGeneration++
		}
	}
	return nil
}

// rollbackBindBatch restores the cache mutations made by AddBindBatch. The
// caller holds SchedulerCache.Mutex.
func (sc *SchedulerCache) rollbackBindBatch(states []batchBindTaskState) {
	for index := len(states) - 1; index >= 0; index-- {
		state := &states[index]
		if state.addedToNode {
			state.node.RemoveTask(state.task)
		}
		state.task.NodeName = state.originalNodeName
		state.task.NumaInfo = cloneTopologyInfo(state.originalNumaInfo)
		if state.resourceDecisionSet {
			state.bindContext.TaskInfo.PodAnnotations = cloneTaskAnnotations(state.originalAnnotations)
		}
		if state.statusUpdated {
			state.job.UpdateTaskStatus(state.task, state.originalStatus)
		}
	}
}

// cloneTopologyInfo copies optional NUMA data so an atomic batch rollback does
// not share mutable topology state with a bind context.
func cloneTopologyInfo(info *schedulingapi.TopologyInfo) *schedulingapi.TopologyInfo {
	if info == nil {
		return nil
	}
	return info.Clone()
}

// cloneTaskAnnotations preserves the nil-versus-empty distinction used by Pod
// binding while isolating batch rollback from later annotation mutations.
func cloneTaskAnnotations(annotations map[string]string) map[string]string {
	if annotations == nil {
		return nil
	}
	result := make(map[string]string, len(annotations))
	for key, value := range annotations {
		result[key] = value
	}
	return result
}
