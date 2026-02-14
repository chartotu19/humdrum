package controller

import (
	"context"
	"sort"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dwsv1alpha1 "github.com/chartotu19/humdrum/k8s-dws-controller/api/v1alpha1"
)

// QueueManager manages job queues with priority-based ordering and
// per-queue concurrency limits to provide fair scheduling across queues.
type QueueManager struct {
	client client.Client
	mu     sync.RWMutex

	// maxConcurrentPerQueue limits how many jobs can be in Provisioning or Running
	// state per queue at the same time.
	maxConcurrentPerQueue int

	// queues tracks queued DWSJob names per queue, ordered by priority (descending)
	// then by creation timestamp (ascending, FIFO within same priority).
	queues map[string][]queueEntry
}

type queueEntry struct {
	name      string
	namespace string
	priority  int32
}

// NewQueueManager creates a new QueueManager.
func NewQueueManager(c client.Client, maxConcurrentPerQueue int) *QueueManager {
	if maxConcurrentPerQueue <= 0 {
		maxConcurrentPerQueue = 5
	}
	return &QueueManager{
		client:                c,
		maxConcurrentPerQueue: maxConcurrentPerQueue,
		queues:                make(map[string][]queueEntry),
	}
}

// Enqueue adds a job to its queue. Returns the queue position (1-based).
func (qm *QueueManager) Enqueue(job *dwsv1alpha1.DWSJob) int32 {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	queueName := job.Spec.QueueName
	if queueName == "" {
		queueName = "default"
	}

	entry := queueEntry{
		name:      job.Name,
		namespace: job.Namespace,
		priority:  job.Spec.Priority,
	}

	// Check if already in queue.
	for i, e := range qm.queues[queueName] {
		if e.name == job.Name && e.namespace == job.Namespace {
			return int32(i + 1)
		}
	}

	qm.queues[queueName] = append(qm.queues[queueName], entry)

	// Sort by priority descending, then by name (stable proxy for creation time).
	sort.SliceStable(qm.queues[queueName], func(i, j int) bool {
		if qm.queues[queueName][i].priority != qm.queues[queueName][j].priority {
			return qm.queues[queueName][i].priority > qm.queues[queueName][j].priority
		}
		return qm.queues[queueName][i].name < qm.queues[queueName][j].name
	})

	// Return position.
	for i, e := range qm.queues[queueName] {
		if e.name == job.Name && e.namespace == job.Namespace {
			return int32(i + 1)
		}
	}

	return int32(len(qm.queues[queueName]))
}

// Dequeue removes a job from its queue.
func (qm *QueueManager) Dequeue(job *dwsv1alpha1.DWSJob) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	queueName := job.Spec.QueueName
	if queueName == "" {
		queueName = "default"
	}

	entries := qm.queues[queueName]
	for i, e := range entries {
		if e.name == job.Name && e.namespace == job.Namespace {
			qm.queues[queueName] = append(entries[:i], entries[i+1:]...)
			return
		}
	}
}

// GetPosition returns the current queue position of a job (1-based). Returns 0 if not queued.
func (qm *QueueManager) GetPosition(job *dwsv1alpha1.DWSJob) int32 {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	queueName := job.Spec.QueueName
	if queueName == "" {
		queueName = "default"
	}

	for i, e := range qm.queues[queueName] {
		if e.name == job.Name && e.namespace == job.Namespace {
			return int32(i + 1)
		}
	}
	return 0
}

// CanAdmit checks whether a job can be admitted from its queue based on
// the per-queue concurrency limit. It counts how many jobs in this queue
// are currently in Provisioning or Running phase.
func (qm *QueueManager) CanAdmit(ctx context.Context, job *dwsv1alpha1.DWSJob) (bool, error) {
	logger := log.FromContext(ctx)

	queueName := job.Spec.QueueName
	if queueName == "" {
		queueName = "default"
	}

	// Check if this job is at the front of its priority group.
	qm.mu.RLock()
	position := int32(0)
	for i, e := range qm.queues[queueName] {
		if e.name == job.Name && e.namespace == job.Namespace {
			position = int32(i + 1)
			break
		}
	}
	qm.mu.RUnlock()

	// Only the first job in the queue can be admitted.
	if position != 1 {
		logger.V(1).Info("Job not at front of queue", "position", position, "queue", queueName)
		return false, nil
	}

	// Count active jobs in this queue.
	var allJobs dwsv1alpha1.DWSJobList
	if err := qm.client.List(ctx, &allJobs); err != nil {
		return false, err
	}

	activeCount := 0
	for _, j := range allJobs.Items {
		jQueue := j.Spec.QueueName
		if jQueue == "" {
			jQueue = "default"
		}
		if jQueue != queueName {
			continue
		}
		if j.Status.Phase == dwsv1alpha1.DWSJobProvisioning || j.Status.Phase == dwsv1alpha1.DWSJobRunning {
			activeCount++
		}
	}

	if activeCount >= qm.maxConcurrentPerQueue {
		logger.V(1).Info("Queue at concurrency limit",
			"queue", queueName,
			"active", activeCount,
			"limit", qm.maxConcurrentPerQueue,
		)
		return false, nil
	}

	return true, nil
}

// RebuildQueues rebuilds the in-memory queue state from the cluster.
// This is called on controller startup to recover state.
func (qm *QueueManager) RebuildQueues(ctx context.Context) error {
	logger := log.FromContext(ctx)

	var allJobs dwsv1alpha1.DWSJobList
	if err := qm.client.List(ctx, &allJobs); err != nil {
		return err
	}

	qm.mu.Lock()
	defer qm.mu.Unlock()

	qm.queues = make(map[string][]queueEntry)

	for _, job := range allJobs.Items {
		if job.Status.Phase != dwsv1alpha1.DWSJobQueued && job.Status.Phase != dwsv1alpha1.DWSJobPending {
			continue
		}

		queueName := job.Spec.QueueName
		if queueName == "" {
			queueName = "default"
		}

		entry := queueEntry{
			name:      job.Name,
			namespace: job.Namespace,
			priority:  job.Spec.Priority,
		}
		qm.queues[queueName] = append(qm.queues[queueName], entry)
	}

	// Sort each queue.
	for queueName := range qm.queues {
		sort.SliceStable(qm.queues[queueName], func(i, j int) bool {
			if qm.queues[queueName][i].priority != qm.queues[queueName][j].priority {
				return qm.queues[queueName][i].priority > qm.queues[queueName][j].priority
			}
			return qm.queues[queueName][i].name < qm.queues[queueName][j].name
		})
		logger.Info("Rebuilt queue", "queue", queueName, "size", len(qm.queues[queueName]))
	}

	return nil
}
