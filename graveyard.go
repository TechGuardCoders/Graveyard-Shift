// Package graveyardshift ports the Python Graveyard-Shift maintenance window
// runner to Go. It drains ONE node at a time inside a maintenance window,
// runs an injectable maintenance task, verifies health, and returns the node
// to the pool. Nodes outside the window are skipped, not failed. An abort
// switch stops after the current node finishes.
package graveyardshift

import (
	"errors"
	"fmt"
	"time"
)

// NodeResult is the outcome for a single node in one Run().
type NodeResult struct {
	Node      string  `json:"node"`
	Status    string  `json:"status"` // "maintained" | "skipped-outside-window" | "aborted" | "failed"
	Drained   bool    `json:"drained"`
	Healthy   *bool   `json:"healthy"`
	Restored  bool    `json:"restored"`
	Detail    string  `json:"detail"`
	DurationS float64 `json:"duration_s"`
}

// MaintenanceFn is the maintenance task for a node.
type MaintenanceFn func(node string) error

// HealthFn is the post-maintenance health check.
type HealthFn func(node string) bool

// ClockFn returns minutes since midnight (injectable for tests).
type ClockFn func() int

// DrainFn is the drain hook; returning false fails the node.
type DrainFn func(node string) bool

// RestoreFn is the return-to-pool hook.
type RestoreFn func(node string)

func defaultClock() int {
	now := time.Now()
	return now.Hour()*60 + now.Minute()
}

// InWindow reports whether nowMinutes falls in the window (start, end) in
// minutes-since-midnight; supports wrap past midnight.
func InWindow(nowMinutes int, window [2]int) bool {
	start, end := window[0], window[1]
	if start == end {
		return true // degenerate: always in window
	}
	if start < end {
		return nowMinutes >= start && nowMinutes < end
	}
	return nowMinutes >= start || nowMinutes < end
}

// Graveyard drains one node at a time, maintains it, verifies health, and
// restores it.
type Graveyard struct {
	Nodes         []string
	Window        [2]int
	MaintenanceFn MaintenanceFn
	HealthFn      HealthFn
	ClockFn       ClockFn
	DrainFn       DrainFn
	RestoreFn     RestoreFn

	aborted bool
}

// New builds a Graveyard, applying defaults for optional hooks. It returns an
// error when nodes is empty.
func New(nodes []string, window [2]int, maintenanceFn MaintenanceFn, healthFn HealthFn, clockFn ClockFn, drainFn DrainFn, restoreFn RestoreFn) (*Graveyard, error) {
	if len(nodes) == 0 {
		return nil, errors.New("nodes must be non-empty")
	}
	g := &Graveyard{
		Nodes:         append([]string(nil), nodes...),
		Window:        window,
		MaintenanceFn: maintenanceFn,
		HealthFn:      healthFn,
	}
	if clockFn != nil {
		g.ClockFn = clockFn
	} else {
		g.ClockFn = defaultClock
	}
	if drainFn != nil {
		g.DrainFn = drainFn
	} else {
		g.DrainFn = func(string) bool { return true }
	}
	if restoreFn != nil {
		g.RestoreFn = restoreFn
	} else {
		g.RestoreFn = func(string) {}
	}
	return g, nil
}

// Abort stops the run after the currently running node completes.
func (g *Graveyard) Abort() { g.aborted = true }

// Aborted reports whether the abort switch is engaged.
func (g *Graveyard) Aborted() bool { return g.aborted }

// Run executes the window. One node at a time; never all at once.
func (g *Graveyard) Run() []NodeResult {
	var results []NodeResult
	for _, node := range g.Nodes {
		if g.aborted {
			results = append(results, NodeResult{Node: node, Status: "aborted", Detail: "abort switch engaged"})
			continue
		}
		if !InWindow(g.ClockFn(), g.Window) {
			results = append(results, NodeResult{Node: node, Status: "skipped-outside-window", Detail: "outside maintenance window"})
			continue
		}
		start := time.Now()
		elapsed := func() float64 { return time.Since(start).Seconds() }
		drained := g.DrainFn(node)
		if !drained {
			results = append(results, NodeResult{
				Node:      node,
				Status:    "failed",
				Drained:   false,
				Detail:    "drain failed; node left in pool",
				DurationS: elapsed(),
			})
			continue
		}
		if err := g.MaintenanceFn(node); err != nil {
			results = append(results, NodeResult{
				Node:      node,
				Status:    "failed",
				Drained:   true,
				Detail:    fmt.Sprintf("maintenance raised: %v", err),
				DurationS: elapsed(),
			})
			continue
		}
		healthy := g.HealthFn(node)
		if healthy {
			g.RestoreFn(node)
		}
		status := "failed"
		detail := "health check failed; node NOT restored"
		if healthy {
			status = "maintained"
			detail = ""
		}
		results = append(results, NodeResult{
			Node:      node,
			Status:    status,
			Drained:   true,
			Healthy:   &healthy,
			Restored:  healthy,
			Detail:    detail,
			DurationS: elapsed(),
		})
	}
	return results
}
