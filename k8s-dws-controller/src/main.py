"""Entry point for the DWS controller operator."""

from __future__ import annotations

import argparse
import logging

import kopf
import kubernetes.config as k8s_config

from . import controller
from .queue_manager import QueueManager

logger = logging.getLogger(__name__)


def main() -> None:
    parser = argparse.ArgumentParser(description="DWS Job Controller for GKE")
    parser.add_argument(
        "--max-concurrent-per-queue",
        type=int,
        default=5,
        help="Max jobs in Provisioning/Running state per queue (default: 5)",
    )
    parser.add_argument(
        "--in-cluster",
        action="store_true",
        default=True,
        help="Load in-cluster Kubernetes config (default: True)",
    )
    parser.add_argument(
        "--dev",
        action="store_true",
        default=False,
        help="Use local kubeconfig for development",
    )
    parser.add_argument(
        "--log-level",
        default="INFO",
        choices=["DEBUG", "INFO", "WARNING", "ERROR"],
    )
    args = parser.parse_args()

    logging.basicConfig(
        level=getattr(logging, args.log_level),
        format="%(asctime)s %(levelname)-8s %(name)s  %(message)s",
    )

    # Load Kubernetes config.
    if args.dev:
        k8s_config.load_kube_config()
        logger.info("Using local kubeconfig (dev mode)")
    else:
        k8s_config.load_incluster_config()
        logger.info("Using in-cluster config")

    # Wire up the queue manager.
    qm = QueueManager(max_concurrent_per_queue=args.max_concurrent_per_queue)
    qm.rebuild_from_cluster()
    controller.queue_manager = qm

    logger.info(
        "Starting DWS controller  (max_concurrent_per_queue=%d)",
        args.max_concurrent_per_queue,
    )

    # kopf.run() blocks and drives the event loop.
    kopf.run(clusterwide=True)


if __name__ == "__main__":
    main()
