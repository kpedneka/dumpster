"""Unit tests for extract_regions.py's peak-RSS helper.

No pytest/CI harness exists for scripts/ yet (see v4.6) — run directly with:
    python3 -m unittest scripts/test_extract_regions.py
Only module-level (non-lazy) imports are exercised here, so this runs without
the heavy pdfplumber/unstructured/Pillow dependencies installed.
"""
import unittest
from unittest import mock

import extract_regions


class PeakRSSKBTests(unittest.TestCase):
    def test_linux_value_used_as_is(self):
        # ru_maxrss is already KB on Linux, the production runtime.
        with mock.patch.object(extract_regions.sys, "platform", "linux"), mock.patch.object(
            extract_regions.resource, "getrusage"
        ) as getrusage:
            getrusage.return_value = mock.Mock(ru_maxrss=391272)
            self.assertEqual(extract_regions._peak_rss_kb(), 391272)

    def test_macos_value_normalized_from_bytes_to_kb(self):
        # ru_maxrss is bytes on macOS/BSD; without normalizing, a local dev
        # run would misreport peak RSS as 1024x too high.
        with mock.patch.object(extract_regions.sys, "platform", "darwin"), mock.patch.object(
            extract_regions.resource, "getrusage"
        ) as getrusage:
            getrusage.return_value = mock.Mock(ru_maxrss=400_000_000)
            self.assertEqual(extract_regions._peak_rss_kb(), 400_000_000 // 1024)


if __name__ == "__main__":
    unittest.main()
