"""A read-only clock capability."""

from datetime import datetime, timezone

from harnest.agent import tool


@tool
def utc_time() -> dict:
    """Return the current UTC time from the agent server's clock."""
    return {"utc": datetime.now(timezone.utc).isoformat()}
