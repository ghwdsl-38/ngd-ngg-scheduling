from __future__ import annotations

import uuid
from typing import Any

from .algorithms.loadbalance import LoadBalanceAlgorithm
from .algorithms.requirement import RequirementAlgorithm
from .algorithms.topology import TopologyAlgorithm
from .cache.metrics import MetricsCache
from .cache.scheduler_state import RequestSchedulerState
from .cache.snapshot_resolver import SnapshotResolver
from .cache.static_nodes import StaticNodeCache
from .context import AllocationContext
from .errors import (
    InvalidAlgorithmOrder,
    InvalidAlgorithmParameters,
    InvalidRequest,
    UnknownAlgorithm,
)
from .models import AlgorithmPlugin, AlgorithmStage
from .quantity import pod_set_minimums
from .services.result_builder import ResultBuilder


class PipelineRunner:
    DEFAULT_ALGORITHMS = [
        {"name": "requirement", "version": "v1", "parameters": {}},
        {"name": "topology", "version": "v1", "parameters": {}},
        {"name": "loadbalance", "version": "v1", "parameters": {}},
    ]

    def __init__(
        self,
        plugins: list[AlgorithmPlugin] | None = None,
    ) -> None:
        registered = plugins or [
            RequirementAlgorithm(),
            TopologyAlgorithm(),
            LoadBalanceAlgorithm(),
        ]
        self.registry = {
            (plugin.name, plugin.version): plugin
            for plugin in registered
        }

    def run(self, context: AllocationContext) -> list[dict[str, Any]]:
        algorithms = (
            context.request.get("algorithms")
            or self.DEFAULT_ALGORITHMS
        )
        if not isinstance(algorithms, list):
            raise InvalidRequest("algorithms must be an array")

        previous_stage: AlgorithmStage | None = None
        seen_algorithms: set[tuple[str, str]] = set()
        seen_stages: set[AlgorithmStage] = set()

        for raw in algorithms:
            plugin, parameters = self._resolve_plugin(
                raw,
                context.request,
            )
            key = (plugin.name, plugin.version)
            if key in seen_algorithms:
                raise InvalidAlgorithmOrder(
                    f"algorithm {plugin.name}/{plugin.version} is duplicated",
                    request_id=str(context.request.get("requestId", "")),
                )
            if (
                previous_stage is not None
                and plugin.stage < previous_stage
            ):
                raise InvalidAlgorithmOrder(
                    "algorithm stages must follow FILTER -> GROUP -> SCORE",
                    request_id=str(context.request.get("requestId", "")),
                )

            plugin.validate_parameters(parameters)
            result = plugin.execute(context, parameters)
            for field_name, value in result.items():
                setattr(context, field_name, value)

            previous_stage = plugin.stage
            seen_algorithms.add(key)
            seen_stages.add(plugin.stage)

        required_stages = {
            AlgorithmStage.FILTER,
            AlgorithmStage.GROUP,
            AlgorithmStage.SCORE,
        }
        if seen_stages != required_stages:
            raise InvalidAlgorithmOrder(
                "pipeline must contain FILTER, GROUP and SCORE stages",
                request_id=str(context.request.get("requestId", "")),
            )

        max_groups = int(
            context.request.get("maxCandidateGroups", 3)
        )
        context.candidates = context.candidates[:max_groups]
        for rank, group in enumerate(context.candidates, start=1):
            group["rank"] = rank
        return context.candidates

    def _resolve_plugin(
        self,
        raw: Any,
        request: dict[str, Any],
    ) -> tuple[AlgorithmPlugin, dict[str, Any]]:
        if not isinstance(raw, dict):
            raise InvalidRequest(
                "every algorithm entry must be an object"
            )
        key = (
            str(raw.get("name", "")),
            str(raw.get("version", "")),
        )
        plugin = self.registry.get(key)
        if plugin is None:
            raise UnknownAlgorithm(
                f"algorithm {key[0]}/{key[1]} is not registered",
                request_id=str(request.get("requestId", "")),
            )
        parameters = raw.get("parameters") or {}
        if not isinstance(parameters, dict):
            raise InvalidAlgorithmParameters(
                f"parameters for {key[0]}/{key[1]} must be an object",
                request_id=str(request.get("requestId", "")),
            )
        return plugin, parameters


class AlgorithmService:
    """Coordinates caches, request normalization, pipeline and response."""

    def __init__(
        self,
        metrics_cache: MetricsCache | None = None,
        static_cache: StaticNodeCache | None = None,
        scheduler_state: RequestSchedulerState | None = None,
        pipeline: PipelineRunner | None = None,
        result_builder: ResultBuilder | None = None,
    ) -> None:
        self.boot_id = f"algorithm-{uuid.uuid4().hex[:12]}"
        self.metrics_cache = metrics_cache or MetricsCache()
        self.static_cache = static_cache or StaticNodeCache()
        self.scheduler_state = (
            scheduler_state or RequestSchedulerState()
        )
        self.snapshot_resolver = SnapshotResolver(
            self.static_cache,
            self.metrics_cache,
        )
        self.pipeline = pipeline or PipelineRunner()
        self.result_builder = result_builder or ResultBuilder()

    def health(self) -> dict[str, Any]:
        return {"status": "ok", "bootId": self.boot_id}

    def ready(self) -> dict[str, Any]:
        return {"status": "ready", "bootId": self.boot_id}

    def cache_status(self) -> dict[str, Any]:
        return {
            "bootId": self.boot_id,
            "nodeStatic": self.static_cache.status(),
            "schedulerState": {
                "cached": False,
                "mode": "request-scoped",
            },
            "metrics": self.metrics_cache.status(),
        }

    def put_static_snapshot(
        self,
        snapshot_id: str,
        body: dict[str, Any],
    ) -> dict[str, Any]:
        snapshot = self.static_cache.put(snapshot_id, body)
        return {
            "accepted": True,
            "snapshotId": snapshot.snapshot_id,
            "acceptedSnapshotId": snapshot.snapshot_id,
            "algorithmBootId": self.boot_id,
            "bootId": self.boot_id,
            "nodeCount": len(snapshot.nodes),
            "checksum": snapshot.snapshot_id,
        }

    def allocate(
        self,
        request: dict[str, Any],
        *,
        legacy_contract: bool = False,
    ) -> dict[str, Any]:
        self._validate_request(request)
        request_id = str(request["requestId"])
        resolved = self.snapshot_resolver.resolve(
            str(request["nodeStaticSnapshotId"]),
            request_id,
        )

        normalized = dict(request)
        normalized["nodeUsageStates"] = (
            self.scheduler_state.normalize(
                request,
                resolved.static.nodes,
            )
        )
        context = AllocationContext(
            request=normalized,
            static_snapshot=resolved.static,
            metric_snapshot=resolved.metrics,
            metrics_degraded=resolved.metrics_degraded,
            warnings=resolved.metric_warnings,
            pod_minimums=pod_set_minimums(
                request.get("podSets", [])
            ),
        )
        self.pipeline.run(context)
        return self.result_builder.build(
            context,
            boot_id=self.boot_id,
            legacy_contract=legacy_contract,
        )

    def calculate(self, request: dict[str, Any]) -> dict[str, Any]:
        """Compatibility entry point for the current PRC."""
        return self.allocate(request, legacy_contract=True)

    @staticmethod
    def _validate_request(request: dict[str, Any]) -> None:
        for field_name in (
            "requestId",
            "taskUID",
            "ngdUID",
            "nodeStaticSnapshotId",
        ):
            if not str(request.get(field_name, "")):
                raise InvalidRequest(
                    f"{field_name} must not be empty"
                )
        try:
            generation = int(request.get("ngdGeneration", 0))
            max_groups = int(
                request.get("maxCandidateGroups", 3)
            )
        except (TypeError, ValueError) as exc:
            raise InvalidRequest(
                "ngdGeneration and maxCandidateGroups must be integers"
            ) from exc
        if generation < 1:
            raise InvalidRequest(
                "ngdGeneration must be at least 1"
            )
        if not 1 <= max_groups <= 3:
            raise InvalidRequest(
                "maxCandidateGroups must be between 1 and 3",
                request_id=str(request.get("requestId", "")),
            )
