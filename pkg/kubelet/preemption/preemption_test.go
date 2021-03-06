/*
Copyright 2017 The Kubernetes Authors.

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

package preemption

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	kubeapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/v1"
	kubetypes "k8s.io/kubernetes/pkg/kubelet/types"
)

const (
	critical              = "critical"
	bestEffort            = "bestEffort"
	burstable             = "burstable"
	highRequestBurstable  = "high-request-burstable"
	guaranteed            = "guaranteed"
	highRequestGuaranteed = "high-request-guaranteed"
	tinyBurstable         = "tiny"
	maxPods               = 110
)

type fakePodKiller struct {
	killedPods []*v1.Pod
}

func newFakePodKiller() *fakePodKiller {
	return &fakePodKiller{killedPods: []*v1.Pod{}}
}

func (f *fakePodKiller) clear() {
	f.killedPods = []*v1.Pod{}
}

func (f *fakePodKiller) getKilledPods() []*v1.Pod {
	return f.killedPods
}

func (f *fakePodKiller) killPodNow(pod *v1.Pod, status v1.PodStatus, gracePeriodOverride *int64) error {
	f.killedPods = append(f.killedPods, pod)
	return nil
}

type fakePodProvider struct {
	pods []*v1.Pod
}

func newFakePodProvider() *fakePodProvider {
	return &fakePodProvider{pods: []*v1.Pod{}}
}

func (f *fakePodProvider) setPods(pods []*v1.Pod) {
	f.pods = pods
}

func (f *fakePodProvider) getPods() []*v1.Pod {
	return f.pods
}

func getTestCriticalPodAdmissionHandler(podProvider *fakePodProvider, podKiller *fakePodKiller) *CriticalPodAdmissionHandler {
	return &CriticalPodAdmissionHandler{
		getPodsFunc: podProvider.getPods,
		killPodFunc: podKiller.killPodNow,
		recorder:    &record.FakeRecorder{},
	}
}

func TestEvictPodsToFreeRequests(t *testing.T) {
	type testRun struct {
		testName              string
		inputPods             []*v1.Pod
		insufficientResources admissionRequirementList
		expectErr             bool
		expectedOutput        []*v1.Pod
	}
	podProvider := newFakePodProvider()
	podKiller := newFakePodKiller()
	criticalPodAdmissionHandler := getTestCriticalPodAdmissionHandler(podProvider, podKiller)
	allPods := getTestPods()
	runs := []testRun{
		{
			testName:              "critical pods cannot be preempted",
			inputPods:             []*v1.Pod{allPods[critical]},
			insufficientResources: getAdmissionRequirementList(0, 0, 1),
			expectErr:             true,
			expectedOutput:        nil,
		},
		{
			testName:              "best effort pods are not preempted when attempting to free resources",
			inputPods:             []*v1.Pod{allPods[bestEffort]},
			insufficientResources: getAdmissionRequirementList(0, 1, 0),
			expectErr:             true,
			expectedOutput:        nil,
		},
		{
			testName: "multiple pods evicted",
			inputPods: []*v1.Pod{
				allPods[critical], allPods[bestEffort], allPods[burstable], allPods[highRequestBurstable],
				allPods[guaranteed], allPods[highRequestGuaranteed]},
			insufficientResources: getAdmissionRequirementList(0, 550, 0),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[highRequestBurstable], allPods[highRequestGuaranteed]},
		},
	}
	for _, r := range runs {
		podProvider.setPods(r.inputPods)
		outErr := criticalPodAdmissionHandler.evictPodsToFreeRequests(r.insufficientResources)
		outputPods := podKiller.getKilledPods()
		if !r.expectErr && outErr != nil {
			t.Errorf("evictPodsToFreeRequests returned an unexpected error during the %s test.  Err: %v", r.testName, outErr)
		} else if r.expectErr && outErr == nil {
			t.Errorf("evictPodsToFreeRequests expected an error but returned a successful output=%v during the %s test.", outputPods, r.testName)
		} else if !podListEqual(r.expectedOutput, outputPods) {
			t.Errorf("evictPodsToFreeRequests expected %v but got %v during the %s test.", r.expectedOutput, outputPods, r.testName)
		}
		podKiller.clear()
	}
}

func BenchmarkGetPodsToPreempt(t *testing.B) {
	allPods := getTestPods()
	inputPods := []*v1.Pod{}
	for i := 0; i < maxPods; i++ {
		inputPods = append(inputPods, allPods[tinyBurstable])
	}
	for n := 0; n < t.N; n++ {
		getPodsToPreempt(inputPods, admissionRequirementList([]*admissionRequirement{
			{
				resourceName: v1.ResourceCPU,
				quantity:     parseCPUToInt64("110m"),
			}}))
	}
}

func TestGetPodsToPreempt(t *testing.T) {
	type testRun struct {
		testName              string
		inputPods             []*v1.Pod
		insufficientResources admissionRequirementList
		expectErr             bool
		expectedOutput        []*v1.Pod
	}
	allPods := getTestPods()
	runs := []testRun{
		{
			testName:              "no requirements",
			inputPods:             []*v1.Pod{},
			insufficientResources: getAdmissionRequirementList(0, 0, 0),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{},
		},
		{
			testName:              "no pods",
			inputPods:             []*v1.Pod{},
			insufficientResources: getAdmissionRequirementList(0, 0, 1),
			expectErr:             true,
			expectedOutput:        nil,
		},
		{
			testName:              "equal pods and resources requirements",
			inputPods:             []*v1.Pod{allPods[burstable]},
			insufficientResources: getAdmissionRequirementList(100, 100, 1),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[burstable]},
		},
		{
			testName:              "higer requirements than pod requests",
			inputPods:             []*v1.Pod{allPods[burstable]},
			insufficientResources: getAdmissionRequirementList(200, 200, 2),
			expectErr:             true,
			expectedOutput:        nil,
		},
		{
			testName:              "choose between bestEffort and burstable",
			inputPods:             []*v1.Pod{allPods[burstable], allPods[bestEffort]},
			insufficientResources: getAdmissionRequirementList(0, 0, 1),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[bestEffort]},
		},
		{
			testName:              "choose between burstable and guaranteed",
			inputPods:             []*v1.Pod{allPods[burstable], allPods[guaranteed]},
			insufficientResources: getAdmissionRequirementList(0, 0, 1),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[burstable]},
		},
		{
			testName:              "choose lower request burstable if it meets requirements",
			inputPods:             []*v1.Pod{allPods[bestEffort], allPods[highRequestBurstable], allPods[burstable]},
			insufficientResources: getAdmissionRequirementList(100, 100, 0),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[burstable]},
		},
		{
			testName:              "choose higher request burstable if lower does not meet requirements",
			inputPods:             []*v1.Pod{allPods[bestEffort], allPods[burstable], allPods[highRequestBurstable]},
			insufficientResources: getAdmissionRequirementList(150, 150, 0),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[highRequestBurstable]},
		},
		{
			testName:              "multiple pods required",
			inputPods:             []*v1.Pod{allPods[bestEffort], allPods[burstable], allPods[highRequestBurstable], allPods[guaranteed], allPods[highRequestGuaranteed]},
			insufficientResources: getAdmissionRequirementList(350, 350, 0),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[burstable], allPods[highRequestBurstable]},
		},
		{
			testName:              "evict guaranteed when we have to, and dont evict the extra burstable",
			inputPods:             []*v1.Pod{allPods[bestEffort], allPods[burstable], allPods[highRequestBurstable], allPods[guaranteed], allPods[highRequestGuaranteed]},
			insufficientResources: getAdmissionRequirementList(0, 550, 0),
			expectErr:             false,
			expectedOutput:        []*v1.Pod{allPods[highRequestBurstable], allPods[highRequestGuaranteed]},
		},
	}
	for _, r := range runs {
		outputPods, outErr := getPodsToPreempt(r.inputPods, r.insufficientResources)
		if !r.expectErr && outErr != nil {
			t.Errorf("getPodsToPreempt returned an unexpected error during the %s test.  Err: %v", r.testName, outErr)
		} else if r.expectErr && outErr == nil {
			t.Errorf("getPodsToPreempt expected an error but returned a successful output=%v during the %s test.", outputPods, r.testName)
		} else if !podListEqual(r.expectedOutput, outputPods) {
			t.Errorf("getPodsToPreempt expected %v but got %v during the %s test.", r.expectedOutput, outputPods, r.testName)
		}
	}
}

func TestAdmissionRequirementsDistance(t *testing.T) {
	type testRun struct {
		testName       string
		requirements   admissionRequirementList
		inputPod       *v1.Pod
		expectedOutput float64
	}
	allPods := getTestPods()
	runs := []testRun{
		{
			testName:       "no requirements",
			requirements:   getAdmissionRequirementList(0, 0, 0),
			inputPod:       allPods[burstable],
			expectedOutput: 0,
		},
		{
			testName:       "no requests, some requirements",
			requirements:   getAdmissionRequirementList(100, 100, 1),
			inputPod:       allPods[bestEffort],
			expectedOutput: 2,
		},
		{
			testName:       "equal requests and requirements",
			requirements:   getAdmissionRequirementList(100, 100, 1),
			inputPod:       allPods[burstable],
			expectedOutput: 0,
		},
		{
			testName:       "higher requests than requirements",
			requirements:   getAdmissionRequirementList(50, 50, 0),
			inputPod:       allPods[burstable],
			expectedOutput: 0,
		},
	}
	for _, run := range runs {
		output := run.requirements.distance(run.inputPod)
		if output != run.expectedOutput {
			t.Errorf("expected: %f, got: %f for %s test", run.expectedOutput, output, run.testName)
		}
	}
}

func TestAdmissionRequirementsSubtract(t *testing.T) {
	type testRun struct {
		testName       string
		initial        admissionRequirementList
		inputPod       *v1.Pod
		expectedOutput admissionRequirementList
	}
	allPods := getTestPods()
	runs := []testRun{
		{
			testName:       "subtract a pod from no requirements",
			initial:        getAdmissionRequirementList(0, 0, 0),
			inputPod:       allPods[burstable],
			expectedOutput: getAdmissionRequirementList(0, 0, 0),
		},
		{
			testName:       "subtract no requests from some requirements",
			initial:        getAdmissionRequirementList(100, 100, 1),
			inputPod:       allPods[bestEffort],
			expectedOutput: getAdmissionRequirementList(100, 100, 0),
		},
		{
			testName:       "equal requests and requirements",
			initial:        getAdmissionRequirementList(100, 100, 1),
			inputPod:       allPods[burstable],
			expectedOutput: getAdmissionRequirementList(0, 0, 0),
		},
		{
			testName:       "subtract higher requests than requirements",
			initial:        getAdmissionRequirementList(50, 50, 0),
			inputPod:       allPods[burstable],
			expectedOutput: getAdmissionRequirementList(0, 0, 0),
		},
		{
			testName:       "subtract lower requests than requirements",
			initial:        getAdmissionRequirementList(200, 200, 1),
			inputPod:       allPods[burstable],
			expectedOutput: getAdmissionRequirementList(100, 100, 0),
		},
	}
	for _, run := range runs {
		output := run.initial.subtract(run.inputPod)
		if !admissionRequirementListEqual(output, run.expectedOutput) {
			t.Errorf("expected: %s, got: %s for %s test", run.expectedOutput.toString(), output.toString(), run.testName)
		}
	}
}

func getTestPods() map[string]*v1.Pod {
	allPods := map[string]*v1.Pod{
		tinyBurstable: getPodWithResources(tinyBurstable, v1.ResourceRequirements{
			Requests: v1.ResourceList{
				"cpu":    resource.MustParse("1m"),
				"memory": resource.MustParse("1Mi"),
			},
		}),
		bestEffort: getPodWithResources(bestEffort, v1.ResourceRequirements{}),
		critical: getPodWithResources(critical, v1.ResourceRequirements{
			Requests: v1.ResourceList{
				"cpu":    resource.MustParse("100m"),
				"memory": resource.MustParse("100Mi"),
			},
		}),
		burstable: getPodWithResources(burstable, v1.ResourceRequirements{
			Requests: v1.ResourceList{
				"cpu":    resource.MustParse("100m"),
				"memory": resource.MustParse("100Mi"),
			},
		}),
		guaranteed: getPodWithResources(guaranteed, v1.ResourceRequirements{
			Requests: v1.ResourceList{
				"cpu":    resource.MustParse("100m"),
				"memory": resource.MustParse("100Mi"),
			},
			Limits: v1.ResourceList{
				"cpu":    resource.MustParse("100m"),
				"memory": resource.MustParse("100Mi"),
			},
		}),
		highRequestBurstable: getPodWithResources(highRequestBurstable, v1.ResourceRequirements{
			Requests: v1.ResourceList{
				"cpu":    resource.MustParse("300m"),
				"memory": resource.MustParse("300Mi"),
			},
		}),
		highRequestGuaranteed: getPodWithResources(highRequestGuaranteed, v1.ResourceRequirements{
			Requests: v1.ResourceList{
				"cpu":    resource.MustParse("300m"),
				"memory": resource.MustParse("300Mi"),
			},
			Limits: v1.ResourceList{
				"cpu":    resource.MustParse("300m"),
				"memory": resource.MustParse("300Mi"),
			},
		}),
	}
	allPods[critical].Namespace = kubeapi.NamespaceSystem
	allPods[critical].Annotations[kubetypes.CriticalPodAnnotationKey] = ""
	return allPods
}

func getPodWithResources(name string, requests v1.ResourceRequirements) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name,
			Annotations:  map[string]string{},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:      fmt.Sprintf("%s-container", name),
					Resources: requests,
				},
			},
		},
	}
}

func parseCPUToInt64(res string) int64 {
	r := resource.MustParse(res)
	return (&r).MilliValue()
}

func parseNonCpuResourceToInt64(res string) int64 {
	r := resource.MustParse(res)
	return (&r).Value()
}

func getAdmissionRequirementList(cpu, memory, pods int) admissionRequirementList {
	reqs := []*admissionRequirement{}
	if cpu > 0 {
		reqs = append(reqs, &admissionRequirement{
			resourceName: v1.ResourceCPU,
			quantity:     parseCPUToInt64(fmt.Sprintf("%dm", cpu)),
		})
	}
	if memory > 0 {
		reqs = append(reqs, &admissionRequirement{
			resourceName: v1.ResourceMemory,
			quantity:     parseNonCpuResourceToInt64(fmt.Sprintf("%dMi", memory)),
		})
	}
	if pods > 0 {
		reqs = append(reqs, &admissionRequirement{
			resourceName: v1.ResourcePods,
			quantity:     int64(pods),
		})
	}
	return admissionRequirementList(reqs)
}

// this checks if the lists contents contain all of the same elements.
// this is not correct if there are duplicate pods in the list.
// for example: podListEqual([a, a, b], [a, b, b]) will return true
func admissionRequirementListEqual(list1 admissionRequirementList, list2 admissionRequirementList) bool {
	if len(list1) != len(list2) {
		return false
	}
	for _, a := range list1 {
		contains := false
		for _, b := range list2 {
			if a.resourceName == b.resourceName && a.quantity == b.quantity {
				contains = true
			}
		}
		if !contains {
			return false
		}
	}
	return true
}

// this checks if the lists contents contain all of the same elements.
// this is not correct if there are duplicate pods in the list.
// for example: podListEqual([a, a, b], [a, b, b]) will return true
func podListEqual(list1 []*v1.Pod, list2 []*v1.Pod) bool {
	if len(list1) != len(list2) {
		return false
	}
	for _, a := range list1 {
		contains := false
		for _, b := range list2 {
			if a == b {
				contains = true
			}
		}
		if !contains {
			return false
		}
	}
	return true
}
