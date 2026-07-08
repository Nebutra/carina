# npm Installer Template

This directory is a publish-time template for a future `@nebutra/carina`
installer package. It is not a published package and is intentionally separate
from the TypeScript SDK under `sdk/typescript`.

The npm package should stay thin:

- download or bundle signed platform archives from the GitHub release;
- expose small launcher scripts for `carina`, `carina-daemon`, and `carina-tui`;
- keep SDK APIs in separate SDK packages;
- never start the daemon during install.

Publish checklist:

1. Run `make release-check`.
2. Run `VERSION=<version> make release-package` for each supported platform.
3. Verify archive checksums.
4. Render `package.json.template` with the release version.
5. Copy `bin/carina.js.template` for each launcher name and add platform binary
   acquisition in `scripts/postinstall.js`.
6. Smoke test `npm pack` and `npm install -g <tarball>`.
7. Publish only after GitHub release artifacts are signed or checksummed.
