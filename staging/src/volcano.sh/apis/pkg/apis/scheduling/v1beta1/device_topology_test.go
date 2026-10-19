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

package v1beta1

import (
	"encoding/json"
	"testing"

	scheduling "volcano.sh/apis/pkg/apis/scheduling"
)

func TestPodGroupSpecDeviceTopologyDeepCopy(t *testing.T) {
	original := PodGroupSpec{
		DeviceTopology: testDeviceTopology(HardDeviceTopologyMode, SoftDeviceTopologyMode),
		SubGroupPolicy: []SubGroupPolicySpec{{
			Name:           "workers",
			DeviceTopology: testDeviceTopology(SoftDeviceTopologyMode, HardDeviceTopologyMode),
		}},
	}

	copy := original.DeepCopy()
	if copy.DeviceTopology == original.DeviceTopology {
		t.Fatal("DeepCopy reused the parent device topology pointer")
	}
	if copy.SubGroupPolicy[0].DeviceTopology == original.SubGroupPolicy[0].DeviceTopology {
		t.Fatal("DeepCopy reused the subgroup device topology pointer")
	}

	copy.DeviceTopology.Requirements[0].Resource.ExtendedResourceName = "amd.com/gpu"
	copy.SubGroupPolicy[0].DeviceTopology.Fabric.Mode = SoftDeviceTopologyMode

	if got := original.DeviceTopology.Requirements[0].Resource.ExtendedResourceName; got != "nvidia.com/gpu" {
		t.Fatalf("parent requirement changed through copy: %q", got)
	}
	if got := original.SubGroupPolicy[0].DeviceTopology.Fabric.Mode; got != HardDeviceTopologyMode {
		t.Fatalf("subgroup fabric mode changed through copy: %q", got)
	}
}

func TestPodGroupSpecDeviceTopologyJSONAndConversion(t *testing.T) {
	original := PodGroupSpec{
		DeviceTopology: testDeviceTopology(HardDeviceTopologyMode, SoftDeviceTopologyMode),
		SubGroupPolicy: []SubGroupPolicySpec{{
			Name:           "workers",
			DeviceTopology: testDeviceTopology(SoftDeviceTopologyMode, HardDeviceTopologyMode),
		}},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal PodGroupSpec: %v", err)
	}

	var decoded PodGroupSpec
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal PodGroupSpec: %v", err)
	}
	if got := decoded.DeviceTopology.Requirements[0].Resource.ExtendedResourceName; got != "nvidia.com/gpu" {
		t.Fatalf("decoded parent resource = %q, want nvidia.com/gpu", got)
	}
	if got := decoded.SubGroupPolicy[0].DeviceTopology.Fabric.Mode; got != HardDeviceTopologyMode {
		t.Fatalf("decoded subgroup fabric mode = %q, want hard", got)
	}

	var internal scheduling.PodGroupSpec
	if err := Convert_v1beta1_PodGroupSpec_To_scheduling_PodGroupSpec(&original, &internal, nil); err != nil {
		t.Fatalf("convert PodGroupSpec to internal version: %v", err)
	}
	if internal.DeviceTopology == nil || internal.DeviceTopology.Requirements[0].Resource.ExtendedResourceName != "nvidia.com/gpu" {
		t.Fatalf("converted parent device topology = %#v, want nvidia.com/gpu requirement", internal.DeviceTopology)
	}
	if len(internal.SubGroupPolicy) != 1 || internal.SubGroupPolicy[0].DeviceTopology == nil || internal.SubGroupPolicy[0].DeviceTopology.Fabric.Mode != scheduling.HardDeviceTopologyMode {
		t.Fatalf("converted subgroup device topology = %#v, want hard fabric affinity", internal.SubGroupPolicy)
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
