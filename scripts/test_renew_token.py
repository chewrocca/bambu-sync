import copy
import importlib.util
import io
import json
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("renew_token", Path(__file__).with_name("renew-token.py"))
renew = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renew)


def fixture():
    return {"id": "test-item", "category": "API_CREDENTIAL", "sections": [
        {"id": "original", "label": "Bambu Sync"},
        {"id": "accidental", "label": "Bambu Sync <SECTION>"}], "fields": [
        {"id": "t", "section": {"id": "original"}, "label": "token", "type": "CONCEALED", "value": "old-fixture"},
        {"id": "e", "section": {"id": "original"}, "label": "expires", "type": "STRING", "value": "2026-10-30"},
        {"id": "other", "section": {"id": "accidental"}, "label": "token", "value": "untouched-fixture"}]}


class RenewalTests(unittest.TestCase):
    def test_only_original_two_fields_change(self):
        item = fixture()
        before = copy.deepcopy(item)
        with patch.object(renew.sys, "argv", ["renew-token.py", "--vault", "Easytyger k8s", "--item", "bambu-printer", "--section", "Bambu Sync"]), \
             patch.object(renew.sys.stdin, "isatty", return_value=True), \
             patch.object(renew.sys.stderr, "isatty", return_value=True), \
             patch.object(renew, "op_json", side_effect=[item, None]) as op, \
             patch.object(renew, "authenticate", return_value=("new-fixture", "2027-01-13")), \
             patch("sys.stdout", new_callable=io.StringIO) as output:
            self.assertEqual(renew.main(), 0)
            self.assertNotIn("new-fixture", output.getvalue())
        expected = copy.deepcopy(before)
        expected["fields"][0]["value"] = "new-fixture"
        expected["fields"][1]["value"] = "2027-01-13"
        self.assertEqual(item, expected)
        self.assertEqual(op.call_args.args[0], ["edit", "test-item", "--vault", "Easytyger k8s"])

    def test_login_and_unknown_categories_refused_even_without_passkey_fields(self):
        for category in ("LOGIN", "UNKNOWN", None):
            item = fixture()
            item["category"] = category
            with self.assertRaises(ValueError):
                renew.existing_fields(item, "Bambu Sync")
        item = fixture()
        item["category"] = "SECURE_NOTE"
        self.assertEqual(set(renew.existing_fields(item, "Bambu Sync")), {"token", "expires"})

    def test_missing_duplicate_and_wrong_schema_refused(self):
        for mutate in (
            lambda i: i["sections"].clear(),
            lambda i: i["sections"].append(i["sections"][0].copy()),
            lambda i: i["fields"].append(i["fields"][0].copy()),
            lambda i: i["fields"].pop(1),
            lambda i: i["fields"][1].update(type="DATE"),
        ):
            item = fixture()
            mutate(item)
            with self.assertRaises(ValueError):
                renew.existing_fields(item, "Bambu Sync")

    def test_code_flow_omits_password_and_computes_expiry(self):
        with patch.object(renew.getpass, "getpass", side_effect=["test@example.invalid", "password-fixture", "code-fixture"]), \
             patch.object(renew, "login", side_effect=[{"loginType": "verifyCode"}, {"accessToken": "token-fixture", "expiresIn": 86400}]) as login:
            token, expiry = renew.authenticate()
            self.assertEqual(token, "token-fixture")
            self.assertEqual(expiry, (renew.datetime.now(renew.timezone.utc) + renew.timedelta(days=1)).date().isoformat())
            self.assertEqual(login.call_args.args[0], {"account": "test@example.invalid", "code": "code-fixture"})

    def test_no_code_flow_and_invalid_response(self):
        for response in ({"accessToken": "token-fixture", "expiresIn": 90}, {}, {"accessToken": "token-fixture", "expiresIn": -1}):
            with patch.object(renew.getpass, "getpass", return_value="input-fixture"), patch.object(renew, "login", return_value=response):
                if response.get("expiresIn", 0) > 0:
                    self.assertEqual(renew.authenticate()[0], "token-fixture")
                else:
                    with self.assertRaises(ValueError):
                        renew.authenticate()

    def test_noninteractive_refuses_before_reading_item(self):
        with patch.object(renew.sys, "argv", ["renew-token.py", "--vault", "v", "--item", "i", "--section", "s"]), \
             patch.object(renew.sys.stdin, "isatty", return_value=False), \
             patch.object(renew, "op_json") as op, patch("sys.stderr", new_callable=io.StringIO):
            with self.assertRaises(SystemExit):
                renew.main()
            op.assert_not_called()

    def test_cli_failure_does_not_echo_sensitive_error(self):
        with patch.object(renew.subprocess, "run") as run:
            run.return_value.returncode = 1
            run.return_value.stderr = "sensitive-fixture"
            with self.assertRaises(ValueError) as raised:
                renew.op_json(["edit", "test-item"], fixture())
            self.assertNotIn("sensitive-fixture", str(raised.exception))

    def test_edit_uses_stdin_not_arguments(self):
        with patch.object(renew.subprocess, "run") as run:
            run.return_value.returncode = 0
            renew.op_json(["edit", "test-item", "--vault", "test-vault"], fixture())
            self.assertNotIn("old-fixture", repr(run.call_args.args))
            self.assertEqual(json.loads(run.call_args.kwargs["input"]), fixture())
            self.assertTrue(run.call_args.kwargs["capture_output"])


if __name__ == "__main__":
    unittest.main()
