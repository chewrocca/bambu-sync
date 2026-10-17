#!/usr/bin/env python3
"""Interactively renew a Bambu cloud token and update existing 1Password fields.

Never print login responses or item JSON. Credentials stay in memory and pipes.
"""
import argparse
from datetime import datetime, timedelta, timezone
import getpass
import json
import subprocess
import sys
import urllib.request

LOGIN_URL = "https://api.bambulab.com/v1/user-service/user/login"


def op_json(args, item=None):
    result = subprocess.run(
        ["op", "item", *args],
        input=None if item is None else json.dumps(item),
        capture_output=True, text=True, check=False,
    )
    if result.returncode:
        # CLI errors can contain sensitive input; do not echo stderr.
        raise ValueError("1Password operation failed; check sign-in and item permissions.")
    return json.loads(result.stdout) if item is None else None


def existing_fields(item, section):
    # CLI JSON does not round-trip passkeys. Never accept Login items, even
    # when no passkey field appears in the JSON. Restrict this helper to the
    # two supported categories intended for storing standalone API secrets.
    if item.get("category") not in ("API_CREDENTIAL", "SECURE_NOTE"):
        raise ValueError("JSON renewal supports only API Credential or Secure Note items; use the app for Login items.")
    sections = [s for s in item.get("sections", []) if s.get("label") == section]
    if len(sections) != 1:
        raise ValueError("Expected exactly one existing section with the exact requested name.")
    section_id = sections[0]["id"]
    fields = {}
    for label in ("token", "expires"):
        matches = [f for f in item.get("fields", [])
                   if f.get("section", {}).get("id") == section_id and f.get("label") == label]
        if len(matches) != 1 or not matches[0].get("id"):
            raise ValueError("Expected exactly one existing token and expires field in that section.")
        fields[label] = matches[0]
    if fields["token"].get("type") != "CONCEALED" or fields["expires"].get("type") != "STRING":
        raise ValueError("Expected a concealed token and a text expires field (YYYY-MM-DD).")
    return fields


def login(payload):
    request = urllib.request.Request(
        LOGIN_URL, data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"}, method="POST",
    )
    # Refuse redirects: never forward account credentials to a different URL.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    with urllib.request.build_opener(NoRedirect).open(request, timeout=30) as response:
        return json.load(response)


def authenticate():
    account = getpass.getpass("Bambu account email (hidden): ")
    password = getpass.getpass("Bambu password (hidden): ")
    response = login({"account": account, "password": password})
    if response.get("loginType") == "verifyCode":
        code = getpass.getpass("Emailed verification code (hidden): ")
        response = login({"account": account, "code": code})
    token, seconds = response.get("accessToken"), response.get("expiresIn")
    if not isinstance(token, str) or not token.strip():
        raise ValueError("Login returned no accessToken; the upstream login flow may have changed.")
    if isinstance(seconds, bool) or not isinstance(seconds, (int, float)) or seconds <= 0:
        raise ValueError("Login returned no valid expiresIn; no item was updated.")
    expiry = (datetime.now(timezone.utc) + timedelta(seconds=seconds)).date().isoformat()
    return token, expiry


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--vault", required=True)
    parser.add_argument("--item", required=True)
    parser.add_argument("--section", required=True, help="Exact existing section name, without type suffixes")
    args = parser.parse_args()
    # Fail closed instead of getpass falling back to echoed stdin.
    if not sys.stdin.isatty() or not sys.stderr.isatty():
        parser.exit(1, "Run interactively in a terminal; credentials must be entered at hidden prompts.\n")
    try:
        item = op_json(["get", args.item, "--vault", args.vault, "--format=json"])
        fields = existing_fields(item, args.section)
        token, expiry = authenticate()
        fields["token"]["value"] = token
        fields["expires"]["value"] = expiry
        # JSON on stdin preserves existing IDs and avoids section-name assignment syntax.
        op_json(["edit", item["id"], "--vault", args.vault], item)
        print("Updated the existing token and expires fields. No section was created.")
    except (Exception, KeyboardInterrupt):
        # Never print exception text: it may include response bodies or CLI output.
        print("Renewal did not complete. Check connectivity, login, supported item category (API Credential or Secure Note), "
              "and exact existing section/field names. "
              "If saving was interrupted, verify the item in 1Password before retrying.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
