# Homebrew Template

This directory is a publish-time template, not a live Nebutra tap.

To publish a Homebrew formula later:

1. Run `make release-check`.
2. Run `VERSION=<version> make release-package` on the target platform.
3. Upload the archive and checksum to a GitHub release.
4. Replace `__VERSION__` and `__SHA256__` in `carina.rb.template`.
5. Commit the rendered formula to a Nebutra-maintained tap.
6. Smoke test with `brew install --build-from-source ./carina.rb` or the tap URL.

The formula must not auto-start `carina-daemon`; service startup stays explicit.
