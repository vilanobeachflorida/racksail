import json
import pathlib
import unittest

import racksail_addon

SPEC = pathlib.Path(__file__).resolve().parents[3] / "spec" / "addon-protocol.json"


class ProtocolTest(unittest.TestCase):
    def test_speaks_the_published_protocol_version(self):
        version = json.loads(SPEC.read_text())["info"]["version"]
        self.assertEqual(racksail_addon.API_VERSIONS[0], version)
        self.assertTrue(racksail_addon.supports(version))
        self.assertFalse(racksail_addon.supports("v0"))


if __name__ == "__main__":
    unittest.main()
