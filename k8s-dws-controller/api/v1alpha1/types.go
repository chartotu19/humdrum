package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DWSJobPhase represents the current phase of a DWSJob.
type DWSJobPhase string

const (
	// DWSJobPending means the job has been accepted but is waiting in the queue.
	DWSJobPending DWSJobPhase = "Pending"
	// DWSJobQueued means the job has been placed in the queue and is awaiting capacity.
	DWSJobQueued DWSJobPhase = "Queued"
	// DWSJobProvisioning means DWS is provisioning nodes for the job.
	DWSJobProvisioning DWSJobPhase = "Provisioning"
	// DWSJobRunning means the job's pods are running on DWS-provisioned nodes.
	DWSJobRunning DWSJobPhase = "Running"
	// DWSJobCompleted means the job has finished successfully.
	DWSJobCompleted DWSJobPhase = "Completed"
	// DWSJobFailed means the job has failed.
	DWSJobFailed DWSJobPhase = "Failed"
)

// DWSMode specifies how DWS should schedule the workload.
type DWSMode string

const (
	// DWSModeFlexStart uses best-effort dynamic provisioning with queueing.
	DWSModeFlexStart DWSMode = "FlexStart"
	// DWSModeCalendar uses calendar-based reserved capacity.
	DWSModeCalendar DWSMode = "Calendar"
)

// DWSJobSpec defines the desired state of a DWSJob.
type DWSJobSpec struct {
	// Mode specifies the DWS scheduling mode (FlexStart or Calendar).
	// FlexStart: best-effort, queued until capacity is available.
	// Calendar: reserved time window with guaranteed capacity.
	// +kubebuilder:validation:Enum=FlexStart;Calendar
	// +kubebuilder:default=FlexStart
	Mode DWSMode `json:"mode,omitempty"`

	// NodePoolName is the name of the GKE node pool managed by DWS.
	// +kubebuilder:validation:Required
	NodePoolName string `json:"nodePoolName"`

	// MinNodes is the minimum number of nodes to provision for this job.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MinNodes int32 `json:"minNodes,omitempty"`

	// MaxNodes is the maximum number of nodes allowed for this job.
	// +kubebuilder:validation:Minimum=1
	MaxNodes int32 `json:"maxNodes,omitempty"`

	// Priority determines the job's position in the queue.
	// Higher values indicate higher priority. Default is 0.
	// +kubebuilder:default=0
	Priority int32 `json:"priority,omitempty"`

	// QueueName is the name of the queue to submit this job to.
	// If empty, the "default" queue is used.
	// +kubebuilder:default=default
	QueueName string `json:"queueName,omitempty"`

	// Resources specifies the per-node resource requirements.
	Resources DWSResourceRequirements `json:"resources"`

	// JobTemplate defines the Kubernetes Job to create once capacity is provisioned.
	JobTemplate JobTemplateSpec `json:"jobTemplate"`

	// MaxWaitDuration is the maximum time to wait in the queue before timing out.
	// Format: duration string (e.g., "1h", "30m", "2h30m").
	// +optional
	MaxWaitDuration string `json:"maxWaitDuration,omitempty"`

	// StartTime is the requested start time for Calendar mode.
	// Ignored for FlexStart mode.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// EndTime is the requested end time for Calendar mode.
	// Ignored for FlexStart mode.
	// +optional
	EndTime *metav1.Time `json:"endTime,omitempty"`
}

// DWSResourceRequirements specifies resource requirements per node.
type DWSResourceRequirements struct {
	// GPU type requested (e.g., "nvidia-tesla-a100", "nvidia-h100-80gb").
	// +optional
	GPUType string `json:"gpuType,omitempty"`

	// GPUCount is the number of GPUs per node.
	// +optional
	GPUCount int32 `json:"gpuCount,omitempty"`

	// TPUTopology specifies the TPU topology (e.g., "2x2x2").
	// +optional
	TPUTopology string `json:"tpuTopology,omitempty"`

	// TPUType specifies the TPU accelerator type (e.g., "tpu-v5-lite-podslice").
	// +optional
	TPUType string `json:"tpuType,omitempty"`

	// CPU resource request per node.
	// +optional
	CPU resource.Quantity `json:"cpu,omitempty"`

	// Memory resource request per node.
	// +optional
	Memory resource.Quantity `json:"memory,omitempty"`
}

// JobTemplateSpec defines the template for the Kubernetes Job to be created.
type JobTemplateSpec struct {
	// Parallelism specifies the maximum desired number of pods the job should
	// run at any given time.
	// +optional
	Parallelism *int32 `json:"parallelism,omitempty"`

	// Completions specifies the desired number of successfully finished pods.
	// +optional
	Completions *int32 `json:"completions,omitempty"`

	// Template describes the pod that will be created for this job.
	Template corev1.PodTemplateSpec `json:"template"`
}

// DWSJobStatus defines the observed state of a DWSJob.
type DWSJobStatus struct {
	// Phase is the current phase of the DWSJob.
	Phase DWSJobPhase `json:"phase,omitempty"`

	// QueuePosition is the current position of the job in the queue.
	// 0 means not queued or actively running.
	QueuePosition int32 `json:"queuePosition,omitempty"`

	// ProvisioningRequestName is the name of the ProvisioningRequest created for this job.
	ProvisioningRequestName string `json:"provisioningRequestName,omitempty"`

	// JobName is the name of the Kubernetes Job created for this DWSJob.
	JobName string `json:"jobName,omitempty"`

	// Conditions represent the latest available observations of the DWSJob's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ProvisionedNodes lists the nodes that were provisioned for this job.
	ProvisionedNodes []string `json:"provisionedNodes,omitempty"`

	// StartTime is when the job started running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the job completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides human-readable information about the current state.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Queue",type=string,JSONPath=`.spec.queueName`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Queue Position",type=integer,JSONPath=`.status.queuePosition`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DWSJob is the Schema for the dwsjobs API. It represents a job submission
// to Google DWS (Dynamic Workload Scheduler) on a GKE node pool, with
// built-in queueing and lifecycle management.
type DWSJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DWSJobSpec   `json:"spec,omitempty"`
	Status DWSJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DWSJobList contains a list of DWSJob.
type DWSJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DWSJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DWSJob{}, &DWSJobList{})
}
