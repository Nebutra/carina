from __future__ import annotations
import os
import unittest
from carina_sdk import CarinaClient

@unittest.skipUnless(os.environ.get("CARINA_CONFORMANCE_SOCKET"), "CARINA_CONFORMANCE_SOCKET is not set")
class PackagedDaemonConformanceTest(unittest.TestCase):
    def test_read_only_contract(self) -> None:
        client = CarinaClient(os.environ["CARINA_CONFORMANCE_SOCKET"], timeout=5)
        try:
            runtime = client.initialize()
            self.assertEqual(runtime["protocol_version"].split(".")[0], "1")
            client.doctor(); client.list_agents(); client.list_workers(); client.list_workflows(); client.list_extensions()
        finally:
            client.close()
