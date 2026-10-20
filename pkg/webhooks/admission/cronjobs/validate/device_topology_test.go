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

package validate

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	batchv1alpha1 "volcano.sh/apis/pkg/apis/batch/v1alpha1"
	"volcano.sh/volcano/pkg/features"
)

func TestValidateCronJobDeviceTopology(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.DeviceTopologyAwareScheduling, true)

	cronJob := validDeviceTopologyCronJob()
	if got := validateCronJob(cronJob); got != "" {
		t.Fatalf("expected valid CronJob device topology, got %q", got)
	}

	updated := cronJob.DeepCopy()
	updated.Spec.JobTemplate.Spec.DeviceTopology.Requirements[0].Mode = batchv1alpha1.SoftDeviceTopologyMode
	err := validateCronJobUpdate(cronJob, updated)
	if err == nil || !strings.Contains(err.Error(), "deviceTopology policies are immutable") {
		t.Fatalf("expected immutable device topology error, got %v", err)
	}
}

func validDeviceTopologyCronJob() *batchv1alpha1.CronJob {
	return &batchv1alpha1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "device-topology", Namespace: "default"},
		Spec: batchv1alpha1.CronJobSpec{
			Schedule: "* * * * *",
			JobTemplate: batchv1alpha1.JobTemplateSpec{
				Spec: batchv1alpha1.JobSpec{
					DeviceTopology: &batchv1alpha1.DeviceTopologySpec{
						Requirements: []batchv1alpha1.DeviceTopologyRequirement{{
							Resource: batchv1alpha1.DeviceResourceSelector{ExtendedResourceName: "nvidia.com/gpu"},
							Mode:     batchv1alpha1.HardDeviceTopologyMode,
						}},
					},
					Tasks: []batchv1alpha1.TaskSpec{{
						Name: "worker",
						Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name: "worker",
							Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
								"nvidia.com/gpu": resource.MustParse("1"),
							}},
						}}}},
					}},
				},
			},
		},
	}
}
