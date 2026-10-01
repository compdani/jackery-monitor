"""Stand-in for the Home Assistant sensor catalog.

parsers.py and core.py only ask whether a key is registered. An empty
catalog made core.py drop every cached value on startup (`key not in
SENSORS`). Treating every key as registered keeps the decoded snapshot.
"""


class _AcceptAll(dict):
    def __contains__(self, key) -> bool:
        return True


SENSORS = _AcceptAll()
UNDECODED_SENSOR_KEYS: frozenset = frozenset()
