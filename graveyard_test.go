package graveyardshift

import (
	"errors"
	"strings"
	"testing"
)

type logEntry struct {
	kind string
	node string
}

func makeGy(t *testing.T, nodes []string, window [2]int, clock ClockFn, drainFn DrainFn, log *[]logEntry) *Graveyard {
	t.Helper()
	if drainFn == nil {
		drainFn = func(string) bool { return true }
	}
	restore := func(n string) {
		if log != nil {
			*log = append(*log, logEntry{"restore", n})
		}
	}
	g, err := New(nodes, window,
		func(n string) error {
			if log != nil {
				*log = append(*log, logEntry{"maintain", n})
			}
			return nil
		},
		func(string) bool { return true },
		clock, drainFn, restore)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func statuses(rs []NodeResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Status
	}
	return out
}

func eqStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMaintainsEachNodeOneAtATime(t *testing.T) {
	var order []string
	g, err := New([]string{"a", "b", "c"}, [2]int{0, 1440},
		func(n string) error { order = append(order, "start-"+n); return nil },
		func(string) bool { return true },
		func() int { return 600 }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	results := g.Run()
	eqStrings(t, order, []string{"start-a", "start-b", "start-c"})
	eqStrings(t, statuses(results), []string{"maintained", "maintained", "maintained"})
	for _, r := range results {
		if !r.Drained || !r.Restored || r.Healthy == nil || !*r.Healthy {
			t.Fatalf("bad result: %+v", r)
		}
	}
}

func TestSkipsNodesOutsideWindowNotFailed(t *testing.T) {
	var log []logEntry
	g := makeGy(t, []string{"a", "b"}, [2]int{120, 180}, func() int { return 300 }, nil, &log)
	results := g.Run()
	eqStrings(t, statuses(results), []string{"skipped-outside-window", "skipped-outside-window"})
	if len(log) != 0 {
		t.Fatalf("log should be empty: %v", log)
	}
}

func TestWindowWrapsPastMidnight(t *testing.T) {
	var log []logEntry
	// 01:00 inside 23:00-02:00
	g := makeGy(t, []string{"a"}, [2]int{1380, 120}, func() int { return 60 }, nil, &log)
	results := g.Run()
	if results[0].Status != "maintained" {
		t.Fatalf("status: %s", results[0].Status)
	}
	if len(log) != 2 || log[0].kind != "maintain" || log[1].kind != "restore" || log[0].node != "a" || log[1].node != "a" {
		t.Fatalf("log: %v", log)
	}
}

func TestAbortStopsAfterCurrentNode(t *testing.T) {
	var log []logEntry
	g := makeGy(t, []string{"a", "b", "c"}, [2]int{0, 1440}, func() int { return 600 }, nil, &log)
	g.Abort() // abort before run: nothing maintained
	results := g.Run()
	eqStrings(t, statuses(results), []string{"aborted", "aborted", "aborted"})
	if len(log) != 0 {
		t.Fatalf("log should be empty: %v", log)
	}
}

func TestAbortMidRunStopsAfterCurrent(t *testing.T) {
	var ran []string
	var g *Graveyard
	var err error
	g, err = New([]string{"a", "b", "c"}, [2]int{0, 1440},
		func(n string) error {
			ran = append(ran, n)
			if n == "b" {
				g.Abort()
			}
			return nil
		},
		func(string) bool { return true },
		func() int { return 600 }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	results := g.Run()
	eqStrings(t, ran, []string{"a", "b"})
	eqStrings(t, statuses(results), []string{"maintained", "maintained", "aborted"})
}

func TestUnhealthyNodeNotRestored(t *testing.T) {
	var log []string
	g, err := New([]string{"a"}, [2]int{0, 1440},
		func(string) error { return nil },
		func(string) bool { return false },
		func() int { return 600 }, nil, func(n string) { log = append(log, n) })
	if err != nil {
		t.Fatal(err)
	}
	results := g.Run()
	if results[0].Status != "failed" {
		t.Fatalf("status: %s", results[0].Status)
	}
	if results[0].Healthy == nil || *results[0].Healthy {
		t.Fatalf("healthy should be false: %+v", results[0])
	}
	if results[0].Restored {
		t.Fatalf("restored should be false")
	}
	if len(log) != 0 {
		t.Fatalf("restore should not be called: %v", log)
	}
}

func TestDrainFailureLeavesNodeInPool(t *testing.T) {
	var log []logEntry
	g := makeGy(t, []string{"a"}, [2]int{0, 1440}, func() int { return 600 }, func(string) bool { return false }, &log)
	results := g.Run()
	if results[0].Status != "failed" {
		t.Fatalf("status: %s", results[0].Status)
	}
	if results[0].Drained {
		t.Fatalf("drained should be false")
	}
	if len(log) != 0 {
		t.Fatalf("log should be empty: %v", log)
	}
}

func TestMaintenanceExceptionIsPerNodeNotFatal(t *testing.T) {
	g, err := New([]string{"a", "b"}, [2]int{0, 1440},
		func(n string) error {
			if n == "a" {
				return errors.New("disk stuck")
			}
			return nil
		},
		func(string) bool { return true },
		func() int { return 600 }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	results := g.Run()
	if results[0].Status != "failed" {
		t.Fatalf("status: %s", results[0].Status)
	}
	if !strings.Contains(results[0].Detail, "disk stuck") {
		t.Fatalf("detail: %s", results[0].Detail)
	}
	if results[1].Status != "maintained" {
		t.Fatalf("status: %s", results[1].Status)
	}
}

func TestEmptyNodesRejected(t *testing.T) {
	_, err := New([]string{}, [2]int{0, 1},
		func(string) error { return nil },
		func(string) bool { return true }, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty nodes")
	}
}

func TestResultSerializes(t *testing.T) {
	g := makeGy(t, []string{"a"}, [2]int{0, 1440}, func() int { return 600 }, nil, nil)
	results := g.Run()
	r := results[0]
	if r.Node != "a" || r.Status != "maintained" || !r.Restored {
		t.Fatalf("bad result: %+v", r)
	}
}

func TestInWindowBoundaries(t *testing.T) {
	cases := []struct {
		now    int
		window [2]int
		want   bool
	}{
		{120, [2]int{120, 180}, true},   // start inclusive
		{180, [2]int{120, 180}, false},  // end exclusive
		{60, [2]int{1380, 120}, true},   // wrap: after midnight
		{1380, [2]int{1380, 120}, true}, // wrap: at start
		{120, [2]int{1380, 120}, false}, // wrap: at end exclusive
		{600, [2]int{300, 300}, true},   // degenerate: always in window
	}
	for _, c := range cases {
		if got := InWindow(c.now, c.window); got != c.want {
			t.Errorf("InWindow(%d, %v) = %v, want %v", c.now, c.window, got, c.want)
		}
	}
}
