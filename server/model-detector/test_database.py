import tempfile
import unittest

from database import Database


class DatabaseTests(unittest.TestCase):
    def test_existing_operator_settings_survive_service_restart(self) -> None:
        with tempfile.TemporaryDirectory() as data_dir:
            first = Database(data_dir)
            first.set_setting("interval_minutes", 15)
            first.set_setting("scheduled_mode", "active")
            first.set_setting("scheduler_enabled", True)
            first.set_setting("webhook_url", "https://alerts.example/hook")

            restarted = Database(data_dir)
            settings = restarted.settings()

        self.assertEqual(settings["interval_minutes"], 15)
        self.assertEqual(settings["scheduled_mode"], "active")
        self.assertTrue(settings["scheduler_enabled"])
        self.assertEqual(settings["webhook_url"], "https://alerts.example/hook")


if __name__ == "__main__":
    unittest.main()
