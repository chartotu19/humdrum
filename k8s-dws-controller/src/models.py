"""DWSJob constants, enums, and helpers."""

from __future__ import annotations

from enum import StrEnum

# ── API coordinates ──────────────────────────────────────────────────────────

API_GROUP = "dws.google.com"
API_VERSION = "v1alpha1"
PLURAL = "dwsjobs"

# ── Phases ───────────────────────────────────────────────────────────────────


class Phase(StrEnum):
    PENDING = "Pending"
    QUEUED = "Queued"
    PROVISIONING = "Provisioning"
    RUNNING = "Running"
    COMPLETED = "Completed"
    FAILED = "Failed"


# ── DWS modes ───────────────────────────────────────────────────────────────


class DWSMode(StrEnum):
    FLEX_START = "FlexStart"
    CALENDAR = "Calendar"


# ── Provisioning classes per mode ────────────────────────────────────────────

PROVISIONING_CLASS = {
    DWSMode.FLEX_START: "queued-provisioning.gke.io",
    DWSMode.CALENDAR: "calendar-provisioning.gke.io",
}

# ── Well-known labels / annotations ─────────────────────────────────────────

LABEL_DWSJOB = "dws.google.com/dwsjob"
LABEL_NODE_POOL = "dws.google.com/node-pool"
LABEL_MODE = "dws.google.com/mode"

# ── Finalizer ────────────────────────────────────────────────────────────────

FINALIZER = "dws.google.com/finalizer"

# ── RuntimeClass name applied to pods ────────────────────────────────────────

RUNTIME_CLASS_NAME = "dws-gke"

# ── Requeue intervals (seconds) ─────────────────────────────────────────────

REQUEUE_DEFAULT = 30
REQUEUE_PROVISION = 15
