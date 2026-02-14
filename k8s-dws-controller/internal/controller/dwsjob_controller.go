package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dwsv1alpha1 "github.com/chartotu19/humdrum/k8s-dws-controller/api/v1alpha1"
)

const (
	dwsJobFinalizer      = "dws.google.com/finalizer"
	requeueAfterDefault   = 30 * time.Second
	requeueAfterProvision = 15 * time.Second
	runtimeClassName      = "dws-gke"
)

// ProvisioningRequest GVR for creating unstructured ProvisioningRequest objects.
var provisioningRequestGVR = schema.GroupVersionResource{
	Group:    "autoscaling.x-k8s.io",
	Version:  "v1beta1",
	Resource: "provisioningrequests",
}

// DWSJobReconciler reconciles a DWSJob object. It manages the lifecycle:
// Pending -> Queued -> Provisioning -> Running -> Completed/Failed.
type DWSJobReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	QueueManager *QueueManager
}

// +kubebuilder:rbac:groups=dws.google.com,resources=dwsjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dws.google.com,resources=dwsjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dws.google.com,resources=dwsjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=provisioningrequests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=node.k8s.io,resources=runtimeclasses,verbs=get;list;watch

func (r *DWSJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var dwsJob dwsv1alpha1.DWSJob
	if err := r.Get(ctx, req.NamespacedName, &dwsJob); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion.
	if !dwsJob.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &dwsJob)
	}

	// Add finalizer if not present.
	if !controllerutil.ContainsFinalizer(&dwsJob, dwsJobFinalizer) {
		controllerutil.AddFinalizer(&dwsJob, dwsJobFinalizer)
		if err := r.Update(ctx, &dwsJob); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("Reconciling DWSJob", "phase", dwsJob.Status.Phase)

	switch dwsJob.Status.Phase {
	case "", dwsv1alpha1.DWSJobPending:
		return r.handlePending(ctx, &dwsJob)
	case dwsv1alpha1.DWSJobQueued:
		return r.handleQueued(ctx, &dwsJob)
	case dwsv1alpha1.DWSJobProvisioning:
		return r.handleProvisioning(ctx, &dwsJob)
	case dwsv1alpha1.DWSJobRunning:
		return r.handleRunning(ctx, &dwsJob)
	case dwsv1alpha1.DWSJobCompleted, dwsv1alpha1.DWSJobFailed:
		// Terminal states — clean up queue entry.
		r.QueueManager.Dequeue(&dwsJob)
		return ctrl.Result{}, nil
	default:
		logger.Info("Unknown phase", "phase", dwsJob.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// handlePending validates the job and places it in the queue.
func (r *DWSJobReconciler) handlePending(ctx context.Context, job *dwsv1alpha1.DWSJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Validate spec.
	if job.Spec.NodePoolName == "" {
		return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobFailed,
			"NodePoolName is required", 0)
	}

	if job.Spec.MaxNodes > 0 && job.Spec.MinNodes > job.Spec.MaxNodes {
		return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobFailed,
			"MinNodes cannot exceed MaxNodes", 0)
	}

	// Enqueue the job.
	position := r.QueueManager.Enqueue(job)
	logger.Info("Job enqueued", "queue", job.Spec.QueueName, "position", position)

	return r.setPhaseWithPosition(ctx, job, dwsv1alpha1.DWSJobQueued,
		fmt.Sprintf("Queued at position %d in queue %q", position, job.Spec.QueueName),
		position, requeueAfterDefault)
}

// handleQueued checks if the job can be admitted and creates a ProvisioningRequest.
func (r *DWSJobReconciler) handleQueued(ctx context.Context, job *dwsv1alpha1.DWSJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check max wait timeout.
	if job.Spec.MaxWaitDuration != "" {
		maxWait, err := time.ParseDuration(job.Spec.MaxWaitDuration)
		if err == nil {
			elapsed := time.Since(job.CreationTimestamp.Time)
			if elapsed > maxWait {
				r.QueueManager.Dequeue(job)
				return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobFailed,
					fmt.Sprintf("Exceeded max wait duration of %s", job.Spec.MaxWaitDuration), 0)
			}
		}
	}

	// Update queue position.
	position := r.QueueManager.GetPosition(job)
	if position == 0 {
		// Not in queue — re-enqueue.
		position = r.QueueManager.Enqueue(job)
	}

	// Check if this job can be admitted.
	canAdmit, err := r.QueueManager.CanAdmit(ctx, job)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !canAdmit {
		logger.V(1).Info("Job cannot be admitted yet", "position", position)
		return r.setPhaseWithPosition(ctx, job, dwsv1alpha1.DWSJobQueued,
			fmt.Sprintf("Waiting at position %d in queue %q", position, job.Spec.QueueName),
			position, requeueAfterDefault)
	}

	// Admit the job: create a ProvisioningRequest.
	logger.Info("Admitting job from queue", "queue", job.Spec.QueueName)
	r.QueueManager.Dequeue(job)

	if err := r.createProvisioningRequest(ctx, job); err != nil {
		return ctrl.Result{}, err
	}

	return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobProvisioning,
		"ProvisioningRequest created, waiting for nodes", requeueAfterProvision)
}

// handleProvisioning checks the ProvisioningRequest status and creates the Job when ready.
func (r *DWSJobReconciler) handleProvisioning(ctx context.Context, job *dwsv1alpha1.DWSJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	prName := job.Status.ProvisioningRequestName
	if prName == "" {
		prName = provisioningRequestName(job)
	}

	// Get the ProvisioningRequest.
	pr := &unstructured.Unstructured{}
	pr.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "autoscaling.x-k8s.io",
		Version: "v1beta1",
		Kind:    "ProvisioningRequest",
	})

	err := r.Get(ctx, types.NamespacedName{Name: prName, Namespace: job.Namespace}, pr)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// ProvisioningRequest was deleted — recreate it.
			logger.Info("ProvisioningRequest not found, recreating")
			if err := r.createProvisioningRequest(ctx, job); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: requeueAfterProvision}, nil
		}
		return ctrl.Result{}, err
	}

	// Check ProvisioningRequest conditions.
	conditions, found, err := unstructured.NestedSlice(pr.Object, "status", "conditions")
	if err != nil || !found {
		logger.V(1).Info("ProvisioningRequest has no conditions yet")
		return ctrl.Result{RequeueAfter: requeueAfterProvision}, nil
	}

	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(cond, "type")
		condStatus, _, _ := unstructured.NestedString(cond, "status")

		switch condType {
		case "Provisioned":
			if condStatus == "True" {
				logger.Info("Nodes provisioned, creating Kubernetes Job")
				if err := r.createBatchJob(ctx, job); err != nil {
					return ctrl.Result{}, err
				}
				now := metav1.Now()
				job.Status.StartTime = &now
				return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobRunning,
					"Job is running on DWS-provisioned nodes", requeueAfterDefault)
			}
		case "Failed":
			if condStatus == "True" {
				msg, _, _ := unstructured.NestedString(cond, "message")
				return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobFailed,
					fmt.Sprintf("Provisioning failed: %s", msg), 0)
			}
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfterProvision}, nil
}

// handleRunning monitors the Kubernetes Job and updates status.
func (r *DWSJobReconciler) handleRunning(ctx context.Context, job *dwsv1alpha1.DWSJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if job.Status.JobName == "" {
		logger.Info("No batch job name recorded, cannot monitor")
		return ctrl.Result{RequeueAfter: requeueAfterDefault}, nil
	}

	var batchJob batchv1.Job
	err := r.Get(ctx, types.NamespacedName{
		Name:      job.Status.JobName,
		Namespace: job.Namespace,
	}, &batchJob)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobFailed,
				"Batch Job was deleted", 0)
		}
		return ctrl.Result{}, err
	}

	// Check batch job conditions.
	for _, cond := range batchJob.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			now := metav1.Now()
			job.Status.CompletionTime = &now
			return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobCompleted,
				"Job completed successfully", 0)
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			now := metav1.Now()
			job.Status.CompletionTime = &now
			return r.setPhaseAndRequeue(ctx, job, dwsv1alpha1.DWSJobFailed,
				fmt.Sprintf("Job failed: %s", cond.Message), 0)
		}
	}

	logger.V(1).Info("Job still running",
		"active", batchJob.Status.Active,
		"succeeded", batchJob.Status.Succeeded,
		"failed", batchJob.Status.Failed,
	)

	return ctrl.Result{RequeueAfter: requeueAfterDefault}, nil
}

// handleDeletion cleans up resources when a DWSJob is deleted.
func (r *DWSJobReconciler) handleDeletion(ctx context.Context, job *dwsv1alpha1.DWSJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(job, dwsJobFinalizer) {
		// Clean up the ProvisioningRequest.
		prName := job.Status.ProvisioningRequestName
		if prName != "" {
			pr := &unstructured.Unstructured{}
			pr.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   "autoscaling.x-k8s.io",
				Version: "v1beta1",
				Kind:    "ProvisioningRequest",
			})
			pr.SetName(prName)
			pr.SetNamespace(job.Namespace)

			if err := r.Delete(ctx, pr); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			logger.Info("Deleted ProvisioningRequest", "name", prName)
		}

		// Remove from queue.
		r.QueueManager.Dequeue(job)

		// Remove finalizer.
		controllerutil.RemoveFinalizer(job, dwsJobFinalizer)
		if err := r.Update(ctx, job); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// createProvisioningRequest creates a ProvisioningRequest for the DWSJob.
func (r *DWSJobReconciler) createProvisioningRequest(ctx context.Context, job *dwsv1alpha1.DWSJob) error {
	prName := provisioningRequestName(job)

	pr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "autoscaling.x-k8s.io/v1beta1",
			"kind":       "ProvisioningRequest",
			"metadata": map[string]interface{}{
				"name":      prName,
				"namespace": job.Namespace,
				"labels": map[string]interface{}{
					"dws.google.com/dwsjob": job.Name,
				},
			},
			"spec": r.buildProvisioningRequestSpec(job),
		},
	}

	// Set owner reference so the PR is garbage collected with the DWSJob.
	if err := ctrl.SetControllerReference(job, pr, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference on ProvisioningRequest: %w", err)
	}

	if err := r.Create(ctx, pr); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Already exists — update status with the name.
			job.Status.ProvisioningRequestName = prName
			return nil
		}
		return fmt.Errorf("creating ProvisioningRequest: %w", err)
	}

	job.Status.ProvisioningRequestName = prName
	return nil
}

// buildProvisioningRequestSpec builds the spec for a DWS ProvisioningRequest.
func (r *DWSJobReconciler) buildProvisioningRequestSpec(job *dwsv1alpha1.DWSJob) map[string]interface{} {
	// Build resource requests for the node claim.
	resourceRequests := map[string]interface{}{}

	if job.Spec.Resources.GPUType != "" && job.Spec.Resources.GPUCount > 0 {
		resourceRequests["nvidia.com/gpu"] = fmt.Sprintf("%d", job.Spec.Resources.GPUCount)
	}
	if job.Spec.Resources.TPUType != "" {
		resourceRequests["google.com/tpu"] = "1"
	}
	if !job.Spec.Resources.CPU.IsZero() {
		resourceRequests["cpu"] = job.Spec.Resources.CPU.String()
	}
	if !job.Spec.Resources.Memory.IsZero() {
		resourceRequests["memory"] = job.Spec.Resources.Memory.String()
	}

	// Determine the provisioning class based on DWS mode.
	provisioningClass := "queued-provisioning.gke.io"
	if job.Spec.Mode == dwsv1alpha1.DWSModeCalendar {
		provisioningClass = "calendar-provisioning.gke.io"
	}

	spec := map[string]interface{}{
		"provisioningClassName": provisioningClass,
		"parameters": map[string]interface{}{
			"nodePoolName": job.Spec.NodePoolName,
		},
		"podSets": []interface{}{
			map[string]interface{}{
				"name":  "worker",
				"count": int64(job.Spec.MinNodes),
				"podTemplateRef": map[string]interface{}{
					"name": fmt.Sprintf("%s-pod-template", job.Name),
				},
			},
		},
	}

	// Add calendar-specific parameters.
	if job.Spec.Mode == dwsv1alpha1.DWSModeCalendar {
		params := spec["parameters"].(map[string]interface{})
		if job.Spec.StartTime != nil {
			params["startTime"] = job.Spec.StartTime.Format(time.RFC3339)
		}
		if job.Spec.EndTime != nil {
			params["endTime"] = job.Spec.EndTime.Format(time.RFC3339)
		}
	}

	return spec
}

// createBatchJob creates the actual Kubernetes batch/v1 Job for the DWSJob.
func (r *DWSJobReconciler) createBatchJob(ctx context.Context, job *dwsv1alpha1.DWSJob) error {
	batchJobName := fmt.Sprintf("%s-job", job.Name)

	// Build the pod template with DWS nodepool scheduling constraints.
	podTemplate := job.Spec.JobTemplate.Template.DeepCopy()

	// Add RuntimeClassName for DWS.
	rcName := runtimeClassName
	podTemplate.Spec.RuntimeClassName = &rcName

	// Add node selector for the DWS node pool.
	if podTemplate.Spec.NodeSelector == nil {
		podTemplate.Spec.NodeSelector = make(map[string]string)
	}
	podTemplate.Spec.NodeSelector["cloud.google.com/gke-nodepool"] = job.Spec.NodePoolName

	// Add tolerations for DWS-provisioned nodes.
	podTemplate.Spec.Tolerations = append(podTemplate.Spec.Tolerations, corev1.Toleration{
		Key:      "cloud.google.com/gke-queued",
		Operator: corev1.TolerationOpEqual,
		Value:    "true",
		Effect:   corev1.TaintEffectNoSchedule,
	})

	// Add GPU/TPU resource requests to containers if specified.
	for i := range podTemplate.Spec.Containers {
		if podTemplate.Spec.Containers[i].Resources.Requests == nil {
			podTemplate.Spec.Containers[i].Resources.Requests = corev1.ResourceList{}
		}
		if podTemplate.Spec.Containers[i].Resources.Limits == nil {
			podTemplate.Spec.Containers[i].Resources.Limits = corev1.ResourceList{}
		}
		if job.Spec.Resources.GPUType != "" && job.Spec.Resources.GPUCount > 0 {
			gpuQty := resource.MustParse(fmt.Sprintf("%d", job.Spec.Resources.GPUCount))
			podTemplate.Spec.Containers[i].Resources.Requests["nvidia.com/gpu"] = gpuQty
			podTemplate.Spec.Containers[i].Resources.Limits["nvidia.com/gpu"] = gpuQty
		}
		if job.Spec.Resources.TPUType != "" {
			podTemplate.Spec.Containers[i].Resources.Requests["google.com/tpu"] = resource.MustParse("1")
			podTemplate.Spec.Containers[i].Resources.Limits["google.com/tpu"] = resource.MustParse("1")
		}
	}

	batchJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      batchJobName,
			Namespace: job.Namespace,
			Labels: map[string]string{
				"dws.google.com/dwsjob":    job.Name,
				"dws.google.com/node-pool": job.Spec.NodePoolName,
				"dws.google.com/mode":      string(job.Spec.Mode),
			},
		},
		Spec: batchv1.JobSpec{
			Parallelism: job.Spec.JobTemplate.Parallelism,
			Completions: job.Spec.JobTemplate.Completions,
			Template:    *podTemplate,
		},
	}

	// Set owner reference.
	if err := ctrl.SetControllerReference(job, batchJob, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference on Job: %w", err)
	}

	if err := r.Create(ctx, batchJob); err != nil {
		if apierrors.IsAlreadyExists(err) {
			job.Status.JobName = batchJobName
			return nil
		}
		return fmt.Errorf("creating batch Job: %w", err)
	}

	job.Status.JobName = batchJobName
	return nil
}

// setPhaseAndRequeue updates the DWSJob status phase and message, then requeues.
func (r *DWSJobReconciler) setPhaseAndRequeue(
	ctx context.Context,
	job *dwsv1alpha1.DWSJob,
	phase dwsv1alpha1.DWSJobPhase,
	message string,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	return r.setPhaseWithPosition(ctx, job, phase, message, 0, requeueAfter)
}

// setPhaseWithPosition updates phase, message, and queue position.
func (r *DWSJobReconciler) setPhaseWithPosition(
	ctx context.Context,
	job *dwsv1alpha1.DWSJob,
	phase dwsv1alpha1.DWSJobPhase,
	message string,
	position int32,
	requeueAfter time.Duration,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	origStatus := job.Status.DeepCopy()

	job.Status.Phase = phase
	job.Status.Message = message
	job.Status.QueuePosition = position

	// Set condition.
	condType := string(phase)
	meta.SetStatusCondition(&job.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionTrue,
		Reason:             condType,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})

	if !equality.Semantic.DeepEqual(origStatus, &job.Status) {
		if err := r.Status().Update(ctx, job); err != nil {
			logger.Error(err, "Failed to update DWSJob status")
			return ctrl.Result{}, err
		}
		logger.Info("Updated DWSJob status", "phase", phase, "message", message)
	}

	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

func provisioningRequestName(job *dwsv1alpha1.DWSJob) string {
	return fmt.Sprintf("%s-pr", job.Name)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DWSJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dwsv1alpha1.DWSJob{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
