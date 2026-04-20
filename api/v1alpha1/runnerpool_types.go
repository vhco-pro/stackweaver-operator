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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunnerType defines which runner binary to use.
// +kubebuilder:validation:Enum=terraform;ansible;combined
type RunnerType string

const (
	RunnerTypeTerraform RunnerType = "terraform"
	RunnerTypeAnsible   RunnerType = "ansible"
	RunnerTypeCombined  RunnerType = "combined"
)

// CloudProvider defines the cloud identity provider type.
// +kubebuilder:validation:Enum=none;aws;gcp;azure
type CloudProvider string

const (
	CloudProviderNone  CloudProvider = "none"
	CloudProviderAWS   CloudProvider = "aws"
	CloudProviderGCP   CloudProvider = "gcp"
	CloudProviderAzure CloudProvider = "azure"
)

// RunnerPoolSpec defines the desired state of a RunnerPool.
type RunnerPoolSpec struct {
	// serverUrl is the URL of the Stackweaver API that runners connect to.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^https?://`
	ServerURL string `json:"serverUrl"`

	// tokenSecretRef references a Kubernetes Secret containing the API key
	// used by runners to register with the Stackweaver API.
	// The secret must contain a key specified by tokenSecretRef.key with
	// a valid API key that has runner:register scope.
	TokenSecretRef SecretKeyRef `json:"tokenSecretRef"`

	// agentPoolId is the Stackweaver agent pool ID that runners register into.
	// +kubebuilder:validation:MinLength=1
	AgentPoolID string `json:"agentPoolId"`

	// runnerType selects the runner binary type.
	// +kubebuilder:default=terraform
	// +optional
	RunnerType RunnerType `json:"runnerType,omitempty"`

	// runnerImage overrides the default runner container image.
	// +optional
	RunnerImage string `json:"runnerImage,omitempty"`

	// labels are applied to runners during registration and used for
	// job matching by the Stackweaver orchestrator.
	// +optional
	Labels []string `json:"labels,omitempty"`

	// minReplicas is the minimum number of runner pods to maintain.
	// Set to 0 for scale-to-zero.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// maxReplicas is the maximum number of runner pods allowed.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// scaleDownDelaySeconds controls how long a runner must be idle before
	// it becomes eligible for scale-down. Maps to KEDA cooldownPeriod.
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=0
	// +optional
	ScaleDownDelaySeconds *int32 `json:"scaleDownDelaySeconds,omitempty"`

	// jobsPerRunner defines how many concurrent jobs a single runner pod handles.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	JobsPerRunner *int32 `json:"jobsPerRunner,omitempty"`

	// ephemeral, when true, causes each runner pod to execute exactly one job
	// then terminate. Provides a fresh environment per job for stronger isolation.
	// When false (default), runners persist across jobs and benefit from
	// workspace/provider caching.
	// +kubebuilder:default=false
	// +optional
	Ephemeral bool `json:"ephemeral,omitempty"`

	// cloudIdentity configures cloud provider workload identity for runner pods,
	// allowing them to assume cloud IAM roles without static credentials.
	// +optional
	CloudIdentity *CloudIdentityConfig `json:"cloudIdentity,omitempty"`

	// template defines overrides for the runner pod template.
	// +optional
	Template *RunnerPodTemplateSpec `json:"template,omitempty"`

	// terminationGracePeriodSeconds is the duration in seconds the runner pod
	// needs to finish any running job before being forcefully killed.
	// Should be set high enough to cover worst-case job duration.
	// +kubebuilder:default=3600
	// +kubebuilder:validation:Minimum=30
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
}

// SecretKeyRef references a key within a Kubernetes Secret.
type SecretKeyRef struct {
	// name is the name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// key is the key within the Secret data.
	// +kubebuilder:default=token
	// +optional
	Key string `json:"key,omitempty"`
}

// CloudIdentityConfig configures cloud workload identity for runner pods.
type CloudIdentityConfig struct {
	// provider selects which cloud identity mechanism to configure.
	// +kubebuilder:default=none
	Provider CloudProvider `json:"provider"`

	// aws configures AWS IAM Roles for Service Accounts (IRSA).
	// Required when provider is "aws".
	// +optional
	AWS *AWSIdentityConfig `json:"aws,omitempty"`

	// gcp configures GCP Workload Identity.
	// Required when provider is "gcp".
	// +optional
	GCP *GCPIdentityConfig `json:"gcp,omitempty"`

	// azure configures Azure Workload Identity.
	// Required when provider is "azure".
	// +optional
	Azure *AzureIdentityConfig `json:"azure,omitempty"`
}

// AWSIdentityConfig configures AWS IRSA for runner pods.
type AWSIdentityConfig struct {
	// roleArn is the ARN of the IAM role to assume.
	// The EKS cluster must have an OIDC provider enabled, and the IAM role
	// trust policy must allow the runner ServiceAccount to assume it.
	// +kubebuilder:validation:Pattern=`^arn:aws:iam::\d{12}:role/.+$`
	RoleARN string `json:"roleArn"`
}

// GCPIdentityConfig configures GCP Workload Identity for runner pods.
type GCPIdentityConfig struct {
	// serviceAccount is the Google Cloud service account email to bind to.
	// The GKE cluster must have Workload Identity enabled, and an IAM binding
	// (roles/iam.workloadIdentityUser) must exist between the Kubernetes SA
	// and the Google Cloud SA.
	// +kubebuilder:validation:Pattern=`.+@.+\.iam\.gserviceaccount\.com$`
	ServiceAccount string `json:"serviceAccount"`
}

// AzureIdentityConfig configures Azure Workload Identity for runner pods.
type AzureIdentityConfig struct {
	// clientId is the Azure AD application client ID.
	// +kubebuilder:validation:MinLength=1
	ClientID string `json:"clientId"`

	// tenantId is the Azure AD tenant ID.
	// +kubebuilder:validation:MinLength=1
	TenantID string `json:"tenantId"`
}

// RunnerPodTemplateSpec defines overrides for the runner pod template.
type RunnerPodTemplateSpec struct {
	// metadata contains labels and annotations to apply to runner pods.
	// +optional
	Metadata *RunnerPodMetadata `json:"metadata,omitempty"`

	// spec contains pod spec overrides.
	// +optional
	Spec *RunnerPodSpec `json:"spec,omitempty"`
}

// RunnerPodMetadata defines metadata overrides for runner pods.
type RunnerPodMetadata struct {
	// labels are additional labels to apply to runner pods.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations are additional annotations to apply to runner pods.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// RunnerPodSpec defines pod spec overrides for runner pods.
type RunnerPodSpec struct {
	// nodeSelector constrains runner pods to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations allow runner pods to schedule onto tainted nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// affinity defines scheduling constraints for runner pods.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// resources defines compute resource requirements for the runner container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// volumes are additional volumes to mount into runner pods.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// volumeMounts are additional volume mounts for the runner container.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// serviceAccountName overrides the ServiceAccount used by runner pods.
	// If cloudIdentity is configured, the controller creates a dedicated SA
	// and this field is ignored.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// env defines additional environment variables for the runner container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// imagePullSecrets references secrets for pulling the runner image
	// from private registries.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// RunnerPoolPhase describes the lifecycle phase of the RunnerPool.
// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Scaling;Error
type RunnerPoolPhase string

const (
	RunnerPoolPhasePending      RunnerPoolPhase = "Pending"
	RunnerPoolPhaseProvisioning RunnerPoolPhase = "Provisioning"
	RunnerPoolPhaseReady        RunnerPoolPhase = "Ready"
	RunnerPoolPhaseScaling      RunnerPoolPhase = "Scaling"
	RunnerPoolPhaseError        RunnerPoolPhase = "Error"
)

// RunnerPoolStatus defines the observed state of RunnerPool.
type RunnerPoolStatus struct {
	// phase is the high-level lifecycle phase of the RunnerPool.
	// +optional
	Phase RunnerPoolPhase `json:"phase,omitempty"`

	// readyReplicas is the number of runner pods that have registered
	// with the Stackweaver API and are ready to accept jobs.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// currentReplicas is the total number of runner pods managed by this pool.
	// +optional
	CurrentReplicas int32 `json:"currentReplicas,omitempty"`

	// busyReplicas is the number of runner pods currently executing jobs.
	// +optional
	BusyReplicas int32 `json:"busyReplicas,omitempty"`

	// pendingJobs is the last observed number of pending jobs for this agent pool.
	// +optional
	PendingJobs int32 `json:"pendingJobs,omitempty"`

	// lastScaleTime records the last time the controller scaled the pool.
	// +optional
	LastScaleTime *metav1.Time `json:"lastScaleTime,omitempty"`

	// conditions represent the current state of the RunnerPool.
	// Standard condition types:
	//   - "Available": the pool has at least minReplicas ready runners
	//   - "ScalingActive": KEDA ScaledObject is active and scaling is operational
	//   - "APIReachable": the controller can reach the Stackweaver API
	//   - "Degraded": one or more runners have failed
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition type constants for RunnerPool.
const (
	ConditionAvailable     = "Available"
	ConditionScalingActive = "ScalingActive"
	ConditionAPIReachable  = "APIReachable"
	ConditionDegraded      = "Degraded"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Runner Type",type=string,JSONPath=`.spec.runnerType`
// +kubebuilder:printcolumn:name="Min",type=integer,JSONPath=`.spec.minReplicas`
// +kubebuilder:printcolumn:name="Max",type=integer,JSONPath=`.spec.maxReplicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Current",type=integer,JSONPath=`.status.currentReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RunnerPool is the Schema for the runnerpools API.
// It defines a pool of Stackweaver runner pods that auto-scale based on
// pending job demand for a specific agent pool.
type RunnerPool struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RunnerPool
	// +required
	Spec RunnerPoolSpec `json:"spec"`

	// status defines the observed state of RunnerPool
	// +optional
	Status RunnerPoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RunnerPoolList contains a list of RunnerPool.
type RunnerPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RunnerPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RunnerPool{}, &RunnerPoolList{})
}
