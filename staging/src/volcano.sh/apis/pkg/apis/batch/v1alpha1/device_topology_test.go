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

package v1alpha1

import (
	"encoding/json"
	"testing"
)

func TestJobSpecDeviceTopologyDeepCopy(t *testing.T) {
	original := JobSpec{
		DeviceTopology: testDeviceTopology(HardDeviceTopologyMode, SoftDeviceTopologyMode),
		Tasks: []TaskSpec{{
			Name: "workers",
			PartitionPolicy: &PartitionPolicySpec{
				TotalPartitions: 2,
				PartitionSize:   4,
				DeviceTopology:  testDeviceTopology(SoftDeviceTopologyMode, HardDeviceTopologyMode),
			},
		}},
	}

	copy := original.DeepCopy()
	if copy.DeviceTopology == original.DeviceTopology {
		t.Fatal("DeepCopy reused the Job device topology pointer")
	}
	if copy.Tasks[0].PartitionPolicy.DeviceTopology == original.Tasks[0].PartitionPolicy.DeviceTopology {
		t.Fatal("DeepCopy reused the partition device topology pointer")
	}

	copy.DeviceTopology.Requirements[0].Resource.ExtendedResourceName = "amd.com/gpu"
	copy.Tasks[0].PartitionPolicy.DeviceTopology.Fabric.Mode = SoftDeviceTopologyMode

	if got := original.DeviceTopology.Requirements[0].Resource.ExtendedResourceName; got != "nvidia.com/gpu" {
		t.Fatalf("Job requirement changed through copy: %q", got)
	}
	if got := original.Tasks[0].PartitionPolicy.DeviceTopology.Fabric.Mode; got != HardDeviceTopologyMode {
		t.Fatalf("partition fabric mode changed through copy: %q", got)
	}
}

func TestJobSpecDeviceTopologyJSON(t *testing.T) {
	original := JobSpec{
		DeviceTopology: testDeviceTopology(HardDeviceTopologyMode, SoftDeviceTopologyMode),
		Tasks: []TaskSpec{{
			Name: "workers",
			PartitionPolicy: &PartitionPolicySpec{
				TotalPartitions: 2,
				PartitionSize:   4,
				DeviceTopology:  testDeviceTopology(SoftDeviceTopologyMode, HardDeviceTopologyMode),
			},
		}},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal JobSpec: %v", err)
	}

	var decoded JobSpec
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal JobSpec: %v", err)
	}
	if got := decoded.DeviceTopology.Requirements[0].Resource.ExtendedResourceName; got != "nvidia.com/gpu" {
		t.Fatalf("decoded Job resource = %q, want nvidia.com/gpu", got)
	}
	if got := decoded.Tasks[0].PartitionPolicy.DeviceTopology.Fabric.Mode; got != HardDeviceTopologyMode {
		t.Fatalf("decoded partition fabric mode = %q, want hard", got)
	}
}

func testDeviceTopology(requirementMode, fabricMode DeviceTopologyMode) *DeviceTopologySpec {
	return &DeviceTopologySpec{
		Requirements: []DeviceTopologyRequirement{{
			Resource: DeviceResourceSelector{ExtendedResourceName: "nvidia.com/gpu"},
			Mode:     requirementMode,
		}},
		Fabric: &DeviceFabricAffinity{Mode: fabricMode},
	}
}
