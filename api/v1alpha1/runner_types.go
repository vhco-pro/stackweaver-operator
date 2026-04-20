/*
Copyright 2026.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunnerSpec defines the desired state of a Runner.
// Runner resources are managed by the RunnerPool controller — users should
// not create them directly.
type RunnerSpec struct {
	// poolRef is the name of the RunnerPool that owns this Runner.
	// +kubebuilder:validation:MinLength=1
	PoolRef string `json:"poolRef"`
}

// RunnerPhase describes the lifecycle phase of an individual Runner pod.
// +kubebuilder:validation:Enum=Pending;Registering;Idle;Busy;Draining;Terminated;Error
type RunnerPhase string

const (
	// RunnerPhasePending means the runner pod has been created but has not yet started.
	RunnerPhasePending RunnerPhase = "Pending"

	// RunnerPhaseRegistering means the runner pod is starting and registering
	// with the Stackweaver API.
	RunnerPhaseRegistering RunnerPhase = "Registering"

	// RunnerPhaseIdle means the runner has registered and is polling for jobs.
	RunnerPhaseIdle RunnerPhase = "Idle"

	// RunnerPhaseBusy means the runner is currently executing one or more jobs.
	RunnerPhaseBusy RunnerPhase = "Busy"

	// RunnerPhaseDraining means the runner has been marked for removal
	// and is finishing its current job before deregistering.
	RunnerPhaseDraining RunnerPhase = "Draining"

	// RunnerPhaseTerminated means the runner has deregistered and the pod
	// is being or has been deleted.
	RunnerPhaseTerminated RunnerPhase = "Terminated"

	// RunnerPhaseError means the runner encountered an unrecoverable error.
	RunnerPhaseError RunnerPhase = "Error"
)

// RunnerStatus defines the observed state of a Runner.
type RunnerStatus struct {
	// phase is the current lifecycle phase of this Runner.
	// +optional
	Phase RunnerPhase `json:"phase,omitempty"`

	// runnerId is the UUID assigned by the Stackweaver API after successful
	// runner registration.
	// +optional
	RunnerID string `json:"runnerId,omitempty"`

	// podName is the name of the underlying Kubernetes pod.
	// +optional
	PodName string `json:"podName,omitempty"`

	// currentJobs is the number of jobs the runner is currently executing.
	// +optional
	CurrentJobs int32 `json:"currentJobs,omitempty"`

	// lastJobFinished records the time when the last job completed on this runner.
	// +optional
	LastJobFinished *metav1.Time `json:"lastJobFinished,omitempty"`

	// lastHeartbeat records the last time a heartbeat was received from
	// the runner agent.
	// +optional
	LastHeartbeat *metav1.Time `json:"lastHeartbeat,omitempty"`

	// message provides a human-readable message with details about the
	// runner's current state (e.g. error details).
	// +optional
	Message string `json:"message,omitempty"`

	// conditions represent the current state of the Runner.
	// Standard condition types:
	//   - "PodReady": the underlying pod is running and ready
	//   - "Registered": the runner has registered with the Stackweaver API
	//   - "Healthy": heartbeats are being received within expected intervals
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition type constants for Runner.
const (
	ConditionPodReady   = "PodReady"
	ConditionRegistered = "Registered"
	ConditionHealthy    = "Healthy"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Pool",type=string,JSONPath=`.spec.poolRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Runner ID",type=string,JSONPath=`.status.runnerId`,priority=1
// +kubebuilder:printcolumn:name="Jobs",type=integer,JSONPath=`.status.currentJobs`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.podName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Runner is the Schema for the runners API.
// It represents a single Stackweaver runner pod managed by a RunnerPool.
// Runner resources are created and deleted by the RunnerPool controller —
// users should not create them directly.
type Runner struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Runner
	// +required
	Spec RunnerSpec `json:"spec"`

	// status defines the observed state of Runner
	// +optional
	Status RunnerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RunnerList contains a list of Runner.
type RunnerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Runner `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Runner{}, &RunnerList{})
}
