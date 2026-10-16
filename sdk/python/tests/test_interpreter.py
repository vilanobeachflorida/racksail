import pathlib
import re
import sys
import unittest

PYPROJECT = pathlib.Path(__file__).resolve().parents[1] / "pyproject.toml"


class InterpreterTest(unittest.TestCase):
    # Passing on an interpreter the package doesn't support would say
    # nothing about the ones it does.
    def test_runs_on_a_supported_python(self):
        found = re.search(r'^requires-python\s*=\s*">=\s*(\d+)\.(\d+)"', PYPROJECT.read_text(), re.MULTILINE)
        self.assertIsNotNone(found, "pyproject.toml doesn't say which Python the SDK needs")
        needed = (int(found.group(1)), int(found.group(2)))
        self.assertGreaterEqual(
            sys.version_info[:2],
            needed,
            "the SDK needs Python %d.%d or later, but these tests run on %s (%s); "
            "install a newer Python, or name one with PYTHON=/path/to/python3 for make sdk-check"
            % (needed[0], needed[1], sys.version.split()[0], sys.executable),
        )


if __name__ == "__main__":
    unittest.main()
