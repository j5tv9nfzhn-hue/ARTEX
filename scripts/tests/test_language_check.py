"""中文化检查器的回归测试；测试韩文使用转义字符，避免在源码中重新引入韩文。"""

import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "check-language.py"
spec = importlib.util.spec_from_file_location("language_check", SCRIPT)
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class LanguageCheckTest(unittest.TestCase):
    def test_supported_languages_are_allowed(self):
        for text in ("简体中文：任务已暂停", "English", "Español", "zh / en / es / ko"):
            self.assertIsNone(checker.HANGUL.search(text))

    def test_all_hangul_blocks_are_rejected(self):
        for text in ("\uac00", "\ud7a3", "\u1100", "\u3131", "\ua960", "\ud7b0", "\uffa1"):
            self.assertIsNotNone(checker.HANGUL.search(text))

    def test_html_entities_are_decoded(self):
        self.assertIsNotNone(checker.HANGUL.search(checker.visible_html("<p>&#44032;</p>")))
        self.assertIsNotNone(checker.HANGUL.search(checker.visible_html("<p>&#xAC00;</p>")))

    def test_scripts_styles_and_comments_are_not_visible(self):
        text = "<script>\uac00</script><style>\uac00</style><!--\uac00--><p>简体中文</p>"
        self.assertEqual(checker.visible_html(text), "简体中文")

    def test_source_checks_include_comments_and_literal_strings(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "source.ts"
            source.write_text('// \uac00\nconst title = "\uac00";\n', encoding="utf-8")
            count, violations = checker.scan_files([source])
            self.assertEqual(count, 1)
            self.assertEqual([item[1] for item in violations], [1, 2])

    def test_web_scan_accepts_chinese_and_rejects_visible_hangul(self):
        with tempfile.TemporaryDirectory() as directory:
            page = Path(directory) / "index.html"
            page.write_text("<html lang='zh'><p>简体中文</p></html>", encoding="utf-8")
            self.assertEqual(checker.scan_files([page], web=True), (1, []))
            page.write_text("<p>\uac00</p>", encoding="utf-8")
            self.assertEqual(len(checker.scan_files([page], web=True)[1]), 1)

    def test_binary_files_are_skipped(self):
        with tempfile.TemporaryDirectory() as directory:
            image = Path(directory) / "image.png"
            image.write_bytes(b"\x00\xff\xfe")
            self.assertEqual(checker.scan_files([image]), (0, []))

    def test_missing_export_fails_instead_of_silently_passing(self):
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run([sys.executable, "-I", str(SCRIPT), "--web", directory], capture_output=True)
            self.assertEqual(result.returncode, 2)

    def test_legacy_entry_point_accepts_chinese(self):
        with tempfile.TemporaryDirectory() as directory:
            page = Path(directory) / "index.html"
            page.write_text("<p>简体中文</p>", encoding="utf-8")
            result = subprocess.run(
                [sys.executable, "-I", str(SCRIPT.with_name("check-web-cjk.py")), directory],
                capture_output=True,
            )
            self.assertEqual(result.returncode, 0, result.stdout.decode("utf-8"))


if __name__ == "__main__":
    unittest.main()
