# Android Magisk deployment

The repository can build a native `arm64` Magisk module through **Actions → Build Android Magisk module → Run workflow**. Pushes to `main` upload a workflow artifact; pushing a `v*` tag also creates a GitHub release with an installable ZIP and SHA-256 file.

The module packages the existing Go service, including its embedded Web panel and browser OAuth flow. It does **not** replace the scheduler with an APK-only check-in implementation: check-in, activity, travel, keepalive, blackcat, streak/school tasks, and balance refresh remain part of the same long-lived server.

See [`magisk/README.md`](magisk/README.md) for installation, storage, security, power behavior, and diagnostics.
