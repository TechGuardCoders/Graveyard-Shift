"""Graveyard Shift — scheduled maintenance window runner.

Drains ONE node at a time inside a maintenance window, runs an injectable
maintenance task, verifies health, and returns the node to the pool.
Nodes outside the window are skipped, not failed. An abort switch stops
after the current node finishes.
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Callable, List, Optional, Sequence, Tuple


@dataclass
class NodeResult:
    """Outcome for a single node in one run()."""

    node: str
    status: str  # "maintained" | "skipped-outside-window" | "aborted" | "failed"
    drained: bool = False
    healthy: Optional[bool] = None
    restored: bool = False
    detail: str = ""
    duration_s: float = 0.0

    def as_dict(self) -> dict:
        return {
            "node": self.node,
            "status": self.status,
            "drained": self.drained,
            "healthy": self.healthy,
            "restored": self.restored,
            "detail": self.detail,
            "duration_s": round(self.duration_s, 6),
        }


def _in_window(now_minutes: int, window: Tuple[int, int]) -> bool:
    """Window is (start, end) in minutes-since-midnight; supports wrap past midnight."""
    start, end = window
    if start == end:
        return True  # degenerate: always in window
    if start < end:
        return start <= now_minutes < end
    return now_minutes >= start or now_minutes < end


class Graveyard:
    """Drain one node at a time, maintain it, verify health, restore it.

    Args:
        nodes: node identifiers (strings).
        window: (start, end) minutes since midnight; wraps past midnight.
        maintenance_fn: callable(node) -> object; the maintenance task.
        health_fn: callable(node) -> bool; post-maintenance health check.
        clock_fn: callable() -> int minutes since midnight (injectable for tests).
        drain_fn: optional callable(node) -> bool; drain hook (defaults to True).
        restore_fn: optional callable(node); return-to-pool hook.
    """

    def __init__(
        self,
        nodes: Sequence[str],
        window: Tuple[int, int],
        maintenance_fn: Callable[[str], object],
        health_fn: Callable[[str], bool],
        clock_fn: Optional[Callable[[], int]] = None,
        drain_fn: Optional[Callable[[str], bool]] = None,
        restore_fn: Optional[Callable[[str], None]] = None,
    ) -> None:
        if not nodes:
            raise ValueError("nodes must be non-empty")
        self.nodes: List[str] = list(nodes)
        self.window = window
        self.maintenance_fn = maintenance_fn
        self.health_fn = health_fn
        self.clock_fn = clock_fn or (lambda: time.localtime().tm_hour * 60 + time.localtime().tm_min)
        self.drain_fn = drain_fn or (lambda node: True)
        self.restore_fn = restore_fn or (lambda node: None)
        self._aborted = False

    def abort(self) -> None:
        """Stop after the currently running node completes."""
        self._aborted = True

    @property
    def aborted(self) -> bool:
        return self._aborted

    def run(self) -> List[NodeResult]:
        """Run the window. One node at a time; never all at once."""
        results: List[NodeResult] = []
        for node in self.nodes:
            if self._aborted:
                results.append(NodeResult(node=node, status="aborted", detail="abort switch engaged"))
                continue
            if not _in_window(self.clock_fn(), self.window):
                results.append(
                    NodeResult(node=node, status="skipped-outside-window", detail="outside maintenance window")
                )
                continue
            start = time.monotonic()
            drained = bool(self.drain_fn(node))
            if not drained:
                results.append(
                    NodeResult(
                        node=node,
                        status="failed",
                        drained=False,
                        detail="drain failed; node left in pool",
                        duration_s=time.monotonic() - start,
                    )
                )
                continue
            try:
                self.maintenance_fn(node)
            except Exception as exc:  # noqa: BLE001 - maintenance errors are per-node, not fatal
                results.append(
                    NodeResult(
                        node=node,
                        status="failed",
                        drained=True,
                        detail=f"maintenance raised: {exc}",
                        duration_s=time.monotonic() - start,
                    )
                )
                continue
            healthy = bool(self.health_fn(node))
            if healthy:
                self.restore_fn(node)
            results.append(
                NodeResult(
                    node=node,
                    status="maintained" if healthy else "failed",
                    drained=True,
                    healthy=healthy,
                    restored=healthy,
                    detail="" if healthy else "health check failed; node NOT restored",
                    duration_s=time.monotonic() - start,
                )
            )
        return results
