import importlib.util
import tempfile
import unittest
from pathlib import Path


def load_module(name: str):
    path = Path(__file__).with_name(f"{name}.py")
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


backup = load_module("backup")
restore = load_module("restore")


class RecoveryTests(unittest.TestCase):
    def test_image_gateway_listener_dropin_is_backed_up(self):
        self.assertIn(
            Path("/etc/systemd/system/image-media-gateway.service.d/10-listen-addresses.conf"),
            backup.OPTIONAL_PATHS,
        )

    def test_file_retention_keeps_newest_names(self):
        with tempfile.TemporaryDirectory() as temp_name:
            root = Path(temp_name)
            for index in range(6):
                (root / f"20260802T0{index}0000Z.tar.gz").write_bytes(b"x")
            backup.keep_newest_files(root, 3)
            self.assertEqual(
                [item.name for item in sorted(root.iterdir())],
                [
                    "20260802T030000Z.tar.gz",
                    "20260802T040000Z.tar.gz",
                    "20260802T050000Z.tar.gz",
                ],
            )

    def test_directory_retention_keeps_newest_names(self):
        with tempfile.TemporaryDirectory() as temp_name:
            root = Path(temp_name)
            for index in range(5):
                (root / f"snapshot-{index}").mkdir()
            backup.keep_newest_directories(root, 2)
            self.assertEqual(
                [item.name for item in sorted(root.iterdir())],
                ["snapshot-3", "snapshot-4"],
            )

    def test_safe_extract_rejects_parent_traversal(self):
        import io
        import tarfile

        with tempfile.TemporaryDirectory() as temp_name:
            tar_path = Path(temp_name) / "unsafe.tar"
            with tarfile.open(tar_path, "w") as archive:
                member = tarfile.TarInfo("../escape")
                member.size = 1
                archive.addfile(member, io.BytesIO(b"x"))
            with tarfile.open(tar_path, "r") as archive:
                with self.assertRaises(RuntimeError):
                    restore.safe_extract(archive, Path(temp_name) / "output")


if __name__ == "__main__":
    unittest.main()
