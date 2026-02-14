"""Priority-based queue manager with per-queue concurrency limits.

Jobs are ordered by priority (descending) then by name (ascending, as a
stable FIFO proxy).  Each named queue enforces an independent concurrency
cap so that one busy queue cannot starve another.
"""

from __future__ import annotations

import logging
import threading
from dataclasses import dataclass, field

import kubernetes.client as k8s_client
from kubernetes.client.rest import ApiException

from .models import API_GROUP, API_VERSION, PLURAL, Phase

logger = logging.getLogger(__name__)


@dataclass(order=False)
class _QueueEntry:
    name: str
    namespace: str
    priority: int


class QueueManager:
    """In-memory priority queue backed by the cluster for concurrency checks."""

    def __init__(self, max_concurrent_per_queue: int = 5) -> None:
        self._lock = threading.Lock()
        self._queues: dict[str, list[_QueueEntry]] = {}
        self.max_concurrent_per_queue = max_concurrent_per_queue

    # ── helpers ──────────────────────────────────────────────────────────

    @staticmethod
    def _sort_key(entry: _QueueEntry) -> tuple[int, str]:
        """Higher priority first, then alphabetical name (FIFO proxy)."""
        return (-entry.priority, entry.name)

    def _sort_queue(self, queue_name: str) -> None:
        self._queues.setdefault(queue_name, []).sort(key=self._sort_key)

    def _find(self, queue_name: str, name: str, namespace: str) -> int | None:
        """Return index in the named queue, or None."""
        for i, e in enumerate(self._queues.get(queue_name, [])):
            if e.name == name and e.namespace == namespace:
                return i
        return None

    # ── public API ───────────────────────────────────────────────────────

    def enqueue(self, name: str, namespace: str, priority: int, queue_name: str) -> int:
        """Add a job to its queue.  Returns 1-based position."""
        queue_name = queue_name or "default"
        with self._lock:
            idx = self._find(queue_name, name, namespace)
            if idx is not None:
                return idx + 1

            self._queues.setdefault(queue_name, []).append(
                _QueueEntry(name=name, namespace=namespace, priority=priority)
            )
            self._sort_queue(queue_name)

            pos = self._find(queue_name, name, namespace)
            return (pos + 1) if pos is not None else len(self._queues[queue_name])

    def dequeue(self, name: str, namespace: str, queue_name: str) -> None:
        """Remove a job from its queue."""
        queue_name = queue_name or "default"
        with self._lock:
            idx = self._find(queue_name, name, namespace)
            if idx is not None:
                self._queues[queue_name].pop(idx)

    def get_position(self, name: str, namespace: str, queue_name: str) -> int:
        """Return 1-based queue position, or 0 if not queued."""
        queue_name = queue_name or "default"
        with self._lock:
            idx = self._find(queue_name, name, namespace)
            return (idx + 1) if idx is not None else 0

    def can_admit(self, name: str, namespace: str, queue_name: str) -> bool:
        """Check whether a job may be admitted from its queue.

        Admission requires:
          1. The job is at position 1 (front of queue).
          2. Active (Provisioning | Running) jobs in this queue are below
             the concurrency cap.
        """
        queue_name = queue_name or "default"

        with self._lock:
            idx = self._find(queue_name, name, namespace)
            if idx is None or idx != 0:
                return False

        # Count active jobs in this queue via the K8s API.
        active = self._count_active_in_queue(queue_name)
        if active >= self.max_concurrent_per_queue:
            logger.info(
                "Queue %r at concurrency limit (%d/%d)",
                queue_name,
                active,
                self.max_concurrent_per_queue,
            )
            return False

        return True

    # ── cluster interaction ──────────────────────────────────────────────

    def _count_active_in_queue(self, queue_name: str) -> int:
        """Count Provisioning/Running DWSJobs in *queue_name*."""
        api = k8s_client.CustomObjectsApi()
        try:
            resp = api.list_cluster_custom_object(
                group=API_GROUP,
                version=API_VERSION,
                plural=PLURAL,
            )
        except ApiException as exc:
            logger.error("Failed to list DWSJobs: %s", exc)
            return 0

        active = 0
        for item in resp.get("items", []):
            spec = item.get("spec", {})
            status = item.get("status", {})
            item_queue = spec.get("queueName") or "default"
            if item_queue != queue_name:
                continue
            if status.get("phase") in (Phase.PROVISIONING, Phase.RUNNING):
                active += 1
        return active

    def rebuild_from_cluster(self) -> None:
        """Rebuild in-memory state from the cluster (called on startup)."""
        api = k8s_client.CustomObjectsApi()
        try:
            resp = api.list_cluster_custom_object(
                group=API_GROUP,
                version=API_VERSION,
                plural=PLURAL,
            )
        except ApiException as exc:
            logger.error("Failed to list DWSJobs for queue rebuild: %s", exc)
            return

        with self._lock:
            self._queues.clear()

            for item in resp.get("items", []):
                spec = item.get("spec", {})
                status = item.get("status", {})
                phase = status.get("phase", Phase.PENDING)

                if phase not in (Phase.PENDING, Phase.QUEUED):
                    continue

                queue_name = spec.get("queueName") or "default"
                entry = _QueueEntry(
                    name=item["metadata"]["name"],
                    namespace=item["metadata"]["namespace"],
                    priority=spec.get("priority", 0),
                )
                self._queues.setdefault(queue_name, []).append(entry)

            for qn in self._queues:
                self._sort_queue(qn)
                logger.info("Rebuilt queue %r  (%d entries)", qn, len(self._queues[qn]))
