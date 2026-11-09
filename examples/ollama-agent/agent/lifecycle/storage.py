"""Process-local storage for the disposable single-replica test agent."""

from harnest import lifecycle
from harnest.store import MemoryStore

store = MemoryStore()


@lifecycle.storage.sessions
def sessions():
    """Keep test sessions in memory; restarting clears them."""
    return store


@lifecycle.storage.checkpoints
def checkpoints():
    """Use the same store for test checkpoints."""
    return store
