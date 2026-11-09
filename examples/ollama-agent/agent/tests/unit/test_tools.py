"""Offline tests of the agent's bounded tools."""

from datetime import datetime, timezone

import pytest


@pytest.mark.parametrize("op,left,right,answer", [
    ("add", 2, 3, 5), ("subtract", 8, 3, 5),
    ("multiply", 17, 23, 391), ("divide", 9, 2, 4.5),
])
def test_arithmetic(tools, op, left, right, answer):
    assert tools["calculate"](left, op, right) == {"result": answer}


@pytest.mark.parametrize("op,left,right", [
    ("divide", 1, 0), ("eval", 1, 1),
    ("add", float("inf"), 1), ("multiply", 1e308, 1e308),
])
def test_invalid_arithmetic(tools, op, left, right):
    assert "error" in tools["calculate"](left, op, right)


def test_clock(tools):
    timestamp = datetime.fromisoformat(tools["utc_time"]()["utc"])
    assert abs((datetime.now(timezone.utc) - timestamp).total_seconds()) < 5
