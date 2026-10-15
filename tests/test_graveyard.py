import pytest

from graveyard_shift import Graveyard


def make_gy(nodes, window=(0, 1440), clock=lambda: 600, **kw):
    log = []
    gy = Graveyard(
        nodes=nodes,
        window=window,
        maintenance_fn=lambda n: log.append(("maintain", n)),
        health_fn=lambda n: True,
        clock_fn=clock,
        drain_fn=kw.pop("drain_fn", lambda n: True),
        restore_fn=kw.pop("restore_fn", lambda n: log.append(("restore", n))),
        **kw,
    )
    return gy, log


def test_maintains_each_node_one_at_a_time():
    order = []

    def maint(n):
        order.append(f"start-{n}")

    gy = Graveyard(
        nodes=["a", "b", "c"],
        window=(0, 1440),
        maintenance_fn=maint,
        health_fn=lambda n: True,
        clock_fn=lambda: 600,
    )
    results = gy.run()
    assert order == ["start-a", "start-b", "start-c"]
    assert [r.status for r in results] == ["maintained"] * 3
    assert all(r.drained and r.restored and r.healthy for r in results)


def test_skips_nodes_outside_window_not_failed():
    gy, log = make_gy(["a", "b"], window=(120, 180), clock=lambda: 300)
    results = gy.run()
    assert [r.status for r in results] == ["skipped-outside-window", "skipped-outside-window"]
    assert log == []


def test_window_wraps_past_midnight():
    gy, log = make_gy(["a"], window=(1380, 120), clock=lambda: 60)  # 01:00 inside 23:00-02:00
    results = gy.run()
    assert results[0].status == "maintained"
    assert log == [("maintain", "a"), ("restore", "a")]


def test_abort_stops_after_current_node():
    gy, log = make_gy(["a", "b", "c"])
    gy.abort()  # abort before run: nothing maintained
    results = gy.run()
    assert [r.status for r in results] == ["aborted", "aborted", "aborted"]
    assert log == []


def test_abort_mid_run_stops_after_current():
    ran = []

    def maint(n):
        ran.append(n)
        if n == "b":
            gy.abort()

    gy = Graveyard(
        nodes=["a", "b", "c"],
        window=(0, 1440),
        maintenance_fn=maint,
        health_fn=lambda n: True,
        clock_fn=lambda: 600,
    )
    results = gy.run()
    assert ran == ["a", "b"]
    assert [r.status for r in results] == ["maintained", "maintained", "aborted"]


def test_unhealthy_node_not_restored():
    log = []
    gy = Graveyard(
        nodes=["a"],
        window=(0, 1440),
        maintenance_fn=lambda n: None,
        health_fn=lambda n: False,
        clock_fn=lambda: 600,
        restore_fn=lambda n: log.append(n),
    )
    results = gy.run()
    assert results[0].status == "failed"
    assert results[0].healthy is False
    assert results[0].restored is False
    assert log == []


def test_drain_failure_leaves_node_in_pool():
    gy, log = make_gy(["a"], drain_fn=lambda n: False)
    results = gy.run()
    assert results[0].status == "failed"
    assert results[0].drained is False
    assert log == []


def test_maintenance_exception_is_per_node_not_fatal():
    def boom(n):
        if n == "a":
            raise RuntimeError("disk stuck")

    gy = Graveyard(
        nodes=["a", "b"],
        window=(0, 1440),
        maintenance_fn=boom,
        health_fn=lambda n: True,
        clock_fn=lambda: 600,
    )
    results = gy.run()
    assert results[0].status == "failed"
    assert "disk stuck" in results[0].detail
    assert results[1].status == "maintained"


def test_empty_nodes_rejected():
    with pytest.raises(ValueError):
        Graveyard(nodes=[], window=(0, 1), maintenance_fn=lambda n: None, health_fn=lambda n: True)


def test_result_serializes():
    gy, _ = make_gy(["a"])
    results = gy.run()
    d = results[0].as_dict()
    assert d["node"] == "a" and d["status"] == "maintained" and d["restored"] is True
