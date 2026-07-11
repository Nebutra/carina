# npm Native Launcher

This directory contains the publishable `@nebutra/carina` launcher package. It
selects an OS/architecture-specific optional package and directly executes its
native binary. It never downloads code in `postinstall` and never starts the
daemon during installation. SDK APIs remain in `sdk/typescript`.

Publish checklist:

1. Run `make release-check`.
2. Run `VERSION=<version> make release-package` for each supported platform.
3. Verify archive checksums.
4. Build each package from `platform-package.json.template`, including native
   binaries, `SHA256SUMS`, SPDX SBOM, and provenance statement.
5. Publish platform packages before the launcher package.
6. Smoke test `npm pack` and a global install from the packed tarballs.
7. Publish only after the release assets are attested and checksummed.

The files here are release inputs; their presence does not mean the npm package
has already been published.
