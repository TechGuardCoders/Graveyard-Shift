# Graveyard Shift

Scheduled maintenance window runner. In a defined window, it drains **one node at a time** (never all at once), runs an injectable maintenance task, verifies health, and returns the node to the pool. Nodes outside the window are **skipped, not failed**. An abort switch stops after the current node finishes.

## Why

Real fleets do maintenance in windows, not all at once. The two failure modes this guards against:

- **Draining everything at once** — one bad maintenance task takes the whole pool down. Graveyard Shift is structurally incapable of that: it holds one node, finishes it, then moves on.
- **Failing nodes that were never eligible** — outside the window, nodes are skipped with an explicit status, so a run report never conflates "not in window" with "broken".

## API

```python
from graveyard_shift import Graveyard

gy = Graveyard(
    nodes=["node-a", "node-b", "node-c"],
    window=(2 * 60, 4 * 60),          # 02:00–04:00 local, wraps past midnight
    maintenance_fn=lambda node: node.upgrade_agent(),
    health_fn=lambda node: node.is_healthy(),
    clock_fn=lambda: now_minutes(),    # injectable for tests
    drain_fn=lambda node: node.drain(),
    restore_fn=lambda node: node.uncordon(),
)

results = gy.run()   # one NodeResult per node
gy.abort()           # stops after the current node
```

Each `NodeResult` carries `status` (`maintained` / `skipped-outside-window` / `aborted` / `failed`), `drained`, `healthy`, `restored`, `detail`, and `duration_s` — serializable via `.as_dict()`.

## Semantics

| Situation | Behavior |
|---|---|
| In window, healthy | drained → maintained → health check → restored |
| In window, health check fails | drained, **not restored**, status `failed` |
| In window, maintenance raises | per-node `failed`, run continues to next node |
| In window, drain fails | node left in pool, `failed` |
| Outside window | `skipped-outside-window` — never touched |
| `abort()` called | current node completes, remaining nodes report `aborted` |

## Architecture

```
run()
 └─ for each node:
     ├─ aborted?            → record "aborted"
     ├─ outside window?     → record "skipped-outside-window"
     ├─ drain_fn(node)      → fail? record "failed", node stays in pool
     ├─ maintenance_fn(node)→ raise? record "failed", continue
     ├─ health_fn(node)     → fail? record "failed", NOT restored
     └─ restore_fn(node)    → record "maintained"
```

Pure Python, zero dependencies. The clock, drain, maintenance, health, and restore hooks are all injectable, so the whole thing tests offline — no live network in CI.

## Go implementation

The same runner is also implemented in Go (`graveyard.go`), porting every Python test case to `graveyard_test.go`. Python remains the reference implementation.

```go
package main

import (
	gs "github.com/TechGuardCoders/Graveyard-Shift"
)

func main() {
	gy, _ := gs.New(
		[]string{"node-a", "node-b", "node-c"},
		[2]int{2 * 60, 4 * 60}, // 02:00–04:00 local, wraps past midnight
		func(node string) error { return nil },  // maintenance task
		func(node string) bool { return true },  // health check
		nil, nil, nil,                            // clock/drain/restore default
	)

	results := gy.Run() // one NodeResult per node
	gy.Abort()          // stops after the current node
}
```

Each `NodeResult` carries `Status` (`maintained` / `skipped-outside-window` / `aborted` / `failed`), `Drained`, `Healthy`, `Restored`, `Detail`, and `DurationS` — JSON-tagged for direct serialization.

## Tests

```bash
uv venv && .venv/Scripts/pip install pytest && .venv/Scripts/pytest -q
```

10 tests covering ordering (one-at-a-time), window boundaries incl. midnight wrap, skip-vs-fail, abort before and mid-run, unhealthy-no-restore, drain failure, and per-node exception isolation.

Go tests (same cases, plus window-boundary table):

```bash
go test ./... -v
```

## CI

GitHub Actions runs `pytest` and `go vet` + `go test ./...` on push. Badge: ![CI](https://github.com/TechGuardCoders/Graveyard-Shift/actions/workflows/ci.yml/badge.svg)

## Docker

Multi-stage, multi-arch image (linux/amd64 + linux/arm64) built on `python:3.12-slim`:

```bash
docker build -t graveyard-shift .
docker run --rm graveyard-shift   # runs the test suite as a smoke check
```

For the Go/Rust variant, see the note at the top of the Dockerfile.
