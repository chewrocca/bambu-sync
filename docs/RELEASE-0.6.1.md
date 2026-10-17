# Prepared release: v0.6.1

Preparation only. Publishing, merges, tag pushes, manual dispatch and deployment
require Matt's confirmation. No credential repair is included in this release.

## Verified baseline (2026-10-17)

- Source main: `0171296896be299840722ef02facb9afee653889`, including PR #5.
- Latest push CI succeeded, but push-to-main does not publish an image.
- GitOps main: `c84ac40`; `bambu/sync.yaml` pins image `0.6.0`.
- Read-only check with explicit context `admin@easytyger-k8s` confirms the live
  deployment uses `ghcr.io/chewrocca/bambu-sync:0.6.0`.
- ExternalSecret `bambu-sync` refreshes every `12h`; its token/expiry source
  keys are `bambu-printer/Bambu Sync/token` and `bambu-printer/Bambu Sync/expires`.
- Argo CD application `bambu` tracks GitOps `main` with automated sync enabled.

## Changes

- Clear Slack expiry/status lines, timezone, distinct upcoming/expired titles.
- Correct partial-day handling and deterministic boundary/DST tests.
- Secure interactive renewal helper with fixture tests. It follows the login
  workflow documented in this repository; live login and credential writes
  are deliberately not tested during preparation.
- JSON edits refuse Login and unsupported categories to avoid the CLI passkey
  round-trip limitation; the live item category remains unverified.
- Exact existing-section updates using JSON piped to `op item edit`; no field
  assignments containing credentials and no creation of sections/fields.
- GitOps image bump to `0.6.1` and deployment-specific renewal command.

## After confirmation

1. Commit and push the prepared `fix/token-renewal-alert` source branch, open
   a PR, require green CI (`test`, `lint`, `vuln`), and merge it. The source
   changes are README.md, internal/sync/alerts.go, internal/sync/alerts_test.go,
   scripts/renew-token.py, scripts/test_renew_token.py, .github/workflows/ci.yml
   (runs helper tests), and this release note.
2. Fetch merged main in the source checkout, advance to it, and tag that exact
   approved commit:

   ```sh
   git fetch origin
   git switch main
   git merge --ff-only origin/main
   git tag -a v0.6.1 -m 'v0.6.1'
   git push origin v0.6.1
   gh run list -R chewrocca/bambu-sync --workflow ci --limit 5
   ```

   If tag publication is unavailable, the approved alternative is:

   ```sh
   gh workflow run ci.yml -R chewrocca/bambu-sync --ref main -f version=0.6.1
   ```

   This alternative publishes images but creates no Git tag. Use exactly one
   publication path. Check refs and existing image tags again before publishing
   to avoid overwriting a version published since preparation.
3. Wait for the release run, including `publish`, to succeed. Confirm the
   `0.6.1` image exists for both supported architectures:

   ```sh
   docker buildx imagetools inspect ghcr.io/chewrocca/bambu-sync:0.6.1
   ```

4. Commit/push the prepared GitOps branch `chore/bambu-sync-0.6.1` (only
   `bambu/sync.yaml`), open its PR and review CI. Merge only after the image
   is available and deployment is approved. **Merging this PR triggers Argo CD
   deployment.** No kubectl apply or manual restart is needed.
5. Observe the resulting rollout using only the explicit cluster context:

   ```sh
   kubectl --context admin@easytyger-k8s -n bambu rollout status deployment/bambu-sync
   kubectl --context admin@easytyger-k8s -n bambu get deployment bambu-sync -o jsonpath='{.spec.template.spec.containers[?(@.name=="bambu-sync")].image}'
   ```

   Check build-info and sync-health metrics. Do not send a test Slack message.
   Expiry alerts are normally evaluated on the daily run, not startup.

The source credential section remains unchanged. This release cannot repair a
stale expiry or a token saved in a different section. Any credential repair is a
separate action requiring authorization.

Rollback, if required and approved: revert the GitOps commit to restore the
`0.6.0` image and prior configuration; Argo CD reconciles that revert.
