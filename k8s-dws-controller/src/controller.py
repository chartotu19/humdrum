"""DWSJob kopf handlers – full lifecycle management.

Lifecycle:  Pending ➜ Queued ➜ Provisioning ➜ Running ➜ Completed / Failed

Each phase transition is driven by a kopf timer that re-evaluates state
every REQUEUE_DEFAULT (or REQUEUE_PROVISION) seconds.
"""

from __future__ import annotations

import datetime as dt
import logging
from typing import Any

import kopf
import kubernetes.client as k8s_client
from kubernetes.client.rest import ApiException

from .models import (
    API_GROUP,
    API_VERSION,
    FINALIZER,
    LABEL_DWSJOB,
    LABEL_MODE,
    LABEL_NODE_POOL,
    PLURAL,
    PROVISIONING_CLASS,
    REQUEUE_DEFAULT,
    REQUEUE_PROVISION,
    RUNTIME_CLASS_NAME,
    DWSMode,
    Phase,
)
from .queue_manager import QueueManager

logger = logging.getLogger(__name__)

# Module-level queue manager – wired up in main.py before kopf starts.
queue_manager: QueueManager | None = None


def _get_qm() -> QueueManager:
    assert queue_manager is not None, "queue_manager not initialised"
    return queue_manager


# ═══════════════════════════════════════════════════════════════════════════
# Status helpers
# ═══════════════════════════════════════════════════════════════════════════


def _patch_status(
    name: str,
    namespace: str,
    phase: Phase,
    message: str,
    *,
    queue_position: int = 0,
    extra: dict[str, Any] | None = None,
) -> None:
    """Patch the DWSJob status sub-resource."""
    api = k8s_client.CustomObjectsApi()
    now = dt.datetime.now(dt.timezone.utc).isoformat()

    status: dict[str, Any] = {
        "phase": phase.value,
        "message": message,
        "queuePosition": queue_position,
        "conditions": [
            {
                "type": phase.value,
                "status": "True",
                "reason": phase.value,
                "message": message,
                "lastTransitionTime": now,
            }
        ],
    }
    if extra:
        status.update(extra)

    api.patch_namespaced_custom_object_status(
        group=API_GROUP,
        version=API_VERSION,
        plural=PLURAL,
        namespace=namespace,
        name=name,
        body={"status": status},
    )
    logger.info("DWSJob %s/%s → %s: %s", namespace, name, phase.value, message)


# ═══════════════════════════════════════════════════════════════════════════
# Create / Resume – initial admission
# ═══════════════════════════════════════════════════════════════════════════


@kopf.on.create(API_GROUP, API_VERSION, PLURAL)
@kopf.on.resume(API_GROUP, API_VERSION, PLURAL)
def on_create_or_resume(
    name: str,
    namespace: str,
    spec: dict[str, Any],
    status: dict[str, Any],
    patch: kopf.Patch,
    **_: Any,
) -> None:
    """Validate and enqueue a new (or restarted) DWSJob."""
    phase = status.get("phase", "")

    # Terminal – nothing to do.
    if phase in (Phase.COMPLETED, Phase.FAILED):
        return

    # Add our finalizer for cleanup.
    patch.setdefault("metadata", {}).setdefault("finalizers", [])
    if FINALIZER not in patch["metadata"]["finalizers"]:
        patch["metadata"]["finalizers"] = list(
            set(patch["metadata"].get("finalizers", []) + [FINALIZER])
        )

    node_pool = spec.get("nodePoolName", "")
    if not node_pool:
        _patch_status(name, namespace, Phase.FAILED, "nodePoolName is required")
        return

    min_nodes = spec.get("minNodes", 1)
    max_nodes = spec.get("maxNodes", 0)
    if max_nodes and min_nodes > max_nodes:
        _patch_status(name, namespace, Phase.FAILED, "minNodes cannot exceed maxNodes")
        return

    # If already past Queued (e.g. controller restart), hand off to the timer.
    if phase in (Phase.PROVISIONING, Phase.RUNNING):
        return

    qm = _get_qm()
    position = qm.enqueue(
        name=name,
        namespace=namespace,
        priority=spec.get("priority", 0),
        queue_name=spec.get("queueName", "default"),
    )
    _patch_status(
        name,
        namespace,
        Phase.QUEUED,
        f"Queued at position {position} in queue {spec.get('queueName', 'default')!r}",
        queue_position=position,
    )


# ═══════════════════════════════════════════════════════════════════════════
# Timer – periodic reconciliation
# ═══════════════════════════════════════════════════════════════════════════


@kopf.timer(API_GROUP, API_VERSION, PLURAL, interval=REQUEUE_DEFAULT, initial_delay=5)
def reconcile_timer(
    name: str,
    namespace: str,
    spec: dict[str, Any],
    status: dict[str, Any],
    **_: Any,
) -> None:
    """Periodic reconciler that drives the state machine forward."""
    phase = status.get("phase", Phase.PENDING)

    if phase in (Phase.COMPLETED, Phase.FAILED):
        return

    if phase in ("", Phase.PENDING):
        _handle_pending(name, namespace, spec)
    elif phase == Phase.QUEUED:
        _handle_queued(name, namespace, spec)
    elif phase == Phase.PROVISIONING:
        _handle_provisioning(name, namespace, spec, status)
    elif phase == Phase.RUNNING:
        _handle_running(name, namespace, status)


# ═══════════════════════════════════════════════════════════════════════════
# Delete – cleanup
# ═══════════════════════════════════════════════════════════════════════════


@kopf.on.delete(API_GROUP, API_VERSION, PLURAL)
def on_delete(
    name: str,
    namespace: str,
    spec: dict[str, Any],
    status: dict[str, Any],
    **_: Any,
) -> None:
    """Clean up ProvisioningRequest and dequeue on deletion."""
    qm = _get_qm()
    qm.dequeue(name, namespace, spec.get("queueName", "default"))

    pr_name = status.get("provisioningRequestName", "")
    if pr_name:
        _delete_provisioning_request(pr_name, namespace)
        logger.info("Deleted ProvisioningRequest %s/%s", namespace, pr_name)


# ═══════════════════════════════════════════════════════════════════════════
# Phase handlers
# ═══════════════════════════════════════════════════════════════════════════


def _handle_pending(name: str, namespace: str, spec: dict[str, Any]) -> None:
    qm = _get_qm()
    position = qm.enqueue(
        name=name,
        namespace=namespace,
        priority=spec.get("priority", 0),
        queue_name=spec.get("queueName", "default"),
    )
    _patch_status(
        name,
        namespace,
        Phase.QUEUED,
        f"Queued at position {position}",
        queue_position=position,
    )


def _handle_queued(name: str, namespace: str, spec: dict[str, Any]) -> None:
    qm = _get_qm()
    queue_name = spec.get("queueName", "default")

    # Check timeout.
    max_wait = spec.get("maxWaitDuration", "")
    if max_wait:
        _check_queue_timeout(name, namespace, max_wait, queue_name)

    # Update position.
    position = qm.get_position(name, namespace, queue_name)
    if position == 0:
        position = qm.enqueue(
            name=name,
            namespace=namespace,
            priority=spec.get("priority", 0),
            queue_name=queue_name,
        )

    if not qm.can_admit(name, namespace, queue_name):
        _patch_status(
            name,
            namespace,
            Phase.QUEUED,
            f"Waiting at position {position} in queue {queue_name!r}",
            queue_position=position,
        )
        return

    # Admit: create ProvisioningRequest.
    logger.info("Admitting %s/%s from queue %r", namespace, name, queue_name)
    qm.dequeue(name, namespace, queue_name)

    pr_name = _create_provisioning_request(name, namespace, spec)
    _patch_status(
        name,
        namespace,
        Phase.PROVISIONING,
        "ProvisioningRequest created, waiting for nodes",
        extra={"provisioningRequestName": pr_name},
    )


def _handle_provisioning(
    name: str,
    namespace: str,
    spec: dict[str, Any],
    status: dict[str, Any],
) -> None:
    pr_name = status.get("provisioningRequestName") or f"{name}-pr"

    pr = _get_provisioning_request(pr_name, namespace)
    if pr is None:
        # Recreate if missing.
        pr_name = _create_provisioning_request(name, namespace, spec)
        _patch_status(
            name,
            namespace,
            Phase.PROVISIONING,
            "ProvisioningRequest recreated, waiting for nodes",
            extra={"provisioningRequestName": pr_name},
        )
        return

    for cond in pr.get("status", {}).get("conditions", []):
        cond_type = cond.get("type", "")
        cond_status = cond.get("status", "")

        if cond_type == "Provisioned" and cond_status == "True":
            logger.info("Nodes provisioned for %s/%s – creating Job", namespace, name)
            job_name = _create_batch_job(name, namespace, spec)
            now = dt.datetime.now(dt.timezone.utc).isoformat()
            _patch_status(
                name,
                namespace,
                Phase.RUNNING,
                "Job is running on DWS-provisioned nodes",
                extra={"jobName": job_name, "startTime": now},
            )
            return

        if cond_type == "Failed" and cond_status == "True":
            msg = cond.get("message", "unknown error")
            _patch_status(name, namespace, Phase.FAILED, f"Provisioning failed: {msg}")
            return


def _handle_running(name: str, namespace: str, status: dict[str, Any]) -> None:
    job_name = status.get("jobName", "")
    if not job_name:
        return

    batch_api = k8s_client.BatchV1Api()
    try:
        job = batch_api.read_namespaced_job(name=job_name, namespace=namespace)
    except ApiException as exc:
        if exc.status == 404:
            _patch_status(name, namespace, Phase.FAILED, "Batch Job was deleted")
        return

    for cond in job.status.conditions or []:
        now = dt.datetime.now(dt.timezone.utc).isoformat()
        if cond.type == "Complete" and cond.status == "True":
            _patch_status(
                name,
                namespace,
                Phase.COMPLETED,
                "Job completed successfully",
                extra={"completionTime": now},
            )
            return
        if cond.type == "Failed" and cond.status == "True":
            _patch_status(
                name,
                namespace,
                Phase.FAILED,
                f"Job failed: {cond.message}",
                extra={"completionTime": now},
            )
            return


# ═══════════════════════════════════════════════════════════════════════════
# ProvisioningRequest helpers
# ═══════════════════════════════════════════════════════════════════════════


def _create_provisioning_request(
    name: str, namespace: str, spec: dict[str, Any]
) -> str:
    """Create a ProvisioningRequest CR for the DWSJob and return its name."""
    pr_name = f"{name}-pr"
    mode = spec.get("mode", DWSMode.FLEX_START)
    prov_class = PROVISIONING_CLASS.get(mode, PROVISIONING_CLASS[DWSMode.FLEX_START])

    parameters: dict[str, Any] = {"nodePoolName": spec["nodePoolName"]}
    if mode == DWSMode.CALENDAR:
        if spec.get("startTime"):
            parameters["startTime"] = spec["startTime"]
        if spec.get("endTime"):
            parameters["endTime"] = spec["endTime"]

    body: dict[str, Any] = {
        "apiVersion": "autoscaling.x-k8s.io/v1beta1",
        "kind": "ProvisioningRequest",
        "metadata": {
            "name": pr_name,
            "namespace": namespace,
            "labels": {LABEL_DWSJOB: name},
            "ownerReferences": [
                {
                    "apiVersion": f"{API_GROUP}/{API_VERSION}",
                    "kind": "DWSJob",
                    "name": name,
                    "uid": _get_dwsjob_uid(name, namespace),
                    "controller": True,
                    "blockOwnerDeletion": True,
                }
            ],
        },
        "spec": {
            "provisioningClassName": prov_class,
            "parameters": parameters,
            "podSets": [
                {
                    "name": "worker",
                    "count": spec.get("minNodes", 1),
                    "podTemplateRef": {"name": f"{name}-pod-template"},
                }
            ],
        },
    }

    api = k8s_client.CustomObjectsApi()
    try:
        api.create_namespaced_custom_object(
            group="autoscaling.x-k8s.io",
            version="v1beta1",
            namespace=namespace,
            plural="provisioningrequests",
            body=body,
        )
    except ApiException as exc:
        if exc.status == 409:  # AlreadyExists
            logger.info("ProvisioningRequest %s already exists", pr_name)
        else:
            raise

    return pr_name


def _get_provisioning_request(
    pr_name: str, namespace: str
) -> dict[str, Any] | None:
    api = k8s_client.CustomObjectsApi()
    try:
        return api.get_namespaced_custom_object(
            group="autoscaling.x-k8s.io",
            version="v1beta1",
            namespace=namespace,
            plural="provisioningrequests",
            name=pr_name,
        )
    except ApiException as exc:
        if exc.status == 404:
            return None
        raise


def _delete_provisioning_request(pr_name: str, namespace: str) -> None:
    api = k8s_client.CustomObjectsApi()
    try:
        api.delete_namespaced_custom_object(
            group="autoscaling.x-k8s.io",
            version="v1beta1",
            namespace=namespace,
            plural="provisioningrequests",
            name=pr_name,
        )
    except ApiException as exc:
        if exc.status == 404:
            pass
        else:
            raise


# ═══════════════════════════════════════════════════════════════════════════
# Batch Job helpers
# ═══════════════════════════════════════════════════════════════════════════


def _create_batch_job(name: str, namespace: str, spec: dict[str, Any]) -> str:
    """Create a batch/v1 Job targeting DWS-provisioned nodes."""
    job_name = f"{name}-job"
    template = spec.get("jobTemplate", {})
    pod_spec = template.get("template", {}).get("spec", {})

    # Inject RuntimeClassName.
    pod_spec["runtimeClassName"] = RUNTIME_CLASS_NAME

    # NodeSelector → target the DWS node pool.
    node_selector = pod_spec.setdefault("nodeSelector", {})
    node_selector["cloud.google.com/gke-nodepool"] = spec["nodePoolName"]

    # Toleration for DWS-provisioned nodes.
    tolerations = pod_spec.setdefault("tolerations", [])
    tolerations.append(
        {
            "key": "cloud.google.com/gke-queued",
            "operator": "Equal",
            "value": "true",
            "effect": "NoSchedule",
        }
    )

    # Inject GPU / TPU resource requests into every container.
    resources = spec.get("resources", {})
    gpu_type = resources.get("gpuType", "")
    gpu_count = resources.get("gpuCount", 0)
    tpu_type = resources.get("tpuType", "")

    for container in pod_spec.get("containers", []):
        res_requests = container.setdefault("resources", {}).setdefault("requests", {})
        res_limits = container.setdefault("resources", {}).setdefault("limits", {})
        if gpu_type and gpu_count:
            res_requests["nvidia.com/gpu"] = str(gpu_count)
            res_limits["nvidia.com/gpu"] = str(gpu_count)
        if tpu_type:
            res_requests["google.com/tpu"] = "1"
            res_limits["google.com/tpu"] = "1"

    mode = spec.get("mode", DWSMode.FLEX_START)
    body = {
        "apiVersion": "batch/v1",
        "kind": "Job",
        "metadata": {
            "name": job_name,
            "namespace": namespace,
            "labels": {
                LABEL_DWSJOB: name,
                LABEL_NODE_POOL: spec["nodePoolName"],
                LABEL_MODE: mode,
            },
            "ownerReferences": [
                {
                    "apiVersion": f"{API_GROUP}/{API_VERSION}",
                    "kind": "DWSJob",
                    "name": name,
                    "uid": _get_dwsjob_uid(name, namespace),
                    "controller": True,
                    "blockOwnerDeletion": True,
                }
            ],
        },
        "spec": {
            "parallelism": template.get("parallelism", 1),
            "completions": template.get("completions", 1),
            "template": {"spec": pod_spec},
        },
    }

    batch_api = k8s_client.BatchV1Api()
    try:
        batch_api.create_namespaced_job(namespace=namespace, body=body)
    except ApiException as exc:
        if exc.status == 409:
            logger.info("Job %s already exists", job_name)
        else:
            raise

    return job_name


# ═══════════════════════════════════════════════════════════════════════════
# Misc helpers
# ═══════════════════════════════════════════════════════════════════════════


def _get_dwsjob_uid(name: str, namespace: str) -> str:
    """Fetch the UID of a DWSJob for ownerReference."""
    api = k8s_client.CustomObjectsApi()
    try:
        obj = api.get_namespaced_custom_object(
            group=API_GROUP,
            version=API_VERSION,
            namespace=namespace,
            plural=PLURAL,
            name=name,
        )
        return obj["metadata"]["uid"]
    except ApiException:
        return ""


def _check_queue_timeout(
    name: str, namespace: str, max_wait: str, queue_name: str
) -> None:
    """Fail the job if it has exceeded its maxWaitDuration."""
    api = k8s_client.CustomObjectsApi()
    try:
        obj = api.get_namespaced_custom_object(
            group=API_GROUP,
            version=API_VERSION,
            namespace=namespace,
            plural=PLURAL,
            name=name,
        )
    except ApiException:
        return

    created = obj["metadata"].get("creationTimestamp", "")
    if not created:
        return

    created_dt = dt.datetime.fromisoformat(created.replace("Z", "+00:00"))
    elapsed = dt.datetime.now(dt.timezone.utc) - created_dt

    # Parse Go-style duration (e.g. "2h", "30m", "1h30m").
    total_seconds = _parse_duration(max_wait)
    if total_seconds and elapsed.total_seconds() > total_seconds:
        _get_qm().dequeue(name, namespace, queue_name)
        _patch_status(
            name,
            namespace,
            Phase.FAILED,
            f"Exceeded max wait duration of {max_wait}",
        )


def _parse_duration(s: str) -> float:
    """Parse a simple duration string like '2h', '30m', '1h30m', '90s'."""
    import re

    total = 0.0
    for value, unit in re.findall(r"(\d+(?:\.\d+)?)\s*([hms])", s):
        v = float(value)
        if unit == "h":
            total += v * 3600
        elif unit == "m":
            total += v * 60
        elif unit == "s":
            total += v
    return total
