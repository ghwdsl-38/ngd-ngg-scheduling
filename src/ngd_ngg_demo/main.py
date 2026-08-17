from __future__ import annotations

import argparse
import logging
import os

from .algorithm_client import AlgorithmClient
from .controller import PRCController
from .kube import KubeClient


def main() -> None:
    parser = argparse.ArgumentParser(description="NGD/NGG Pool Resource Controller demo")
    parser.add_argument("--interval", type=int, default=3)
    args = parser.parse_args()
    logging.basicConfig(
        level=os.environ.get("LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    PRCController(
        KubeClient(),
        AlgorithmClient(
            os.environ.get(
                "ALGORITHM_URL", "http://ngd-ngg-algorithm.ngd-ngg-system.svc:8080"
            ),
            float(os.environ.get("ALGORITHM_TIMEOUT_SECONDS", "5")),
        ),
        os.environ.get("CLUSTER_ID", "volcano-ngd-ngg-demo"),
    ).run(args.interval)


if __name__ == "__main__":
    main()
