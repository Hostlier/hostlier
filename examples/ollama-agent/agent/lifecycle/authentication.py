"""Require a separate app credential before accepting paid inference work."""

import hmac
import os

from harnest import lifecycle
from harnest.auth import AuthenticationError, AuthPrincipal


@lifecycle.authenticate
def authenticate(connection, principal):
    """Verify the bearer token without placing it in agent state or claims."""
    expected = os.getenv("AGENT_ACCESS_TOKEN", "")
    supplied = connection.headers.get("authorization", "")
    if not expected or not hmac.compare_digest(supplied, "Bearer " + expected):
        raise AuthenticationError()
    return AuthPrincipal(user_id="test-owner")
