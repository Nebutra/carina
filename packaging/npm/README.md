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
   binaries (the five services/CLIs, all six Zig tools, and Headroom), `SHA256SUMS`, and
   an SPDX SBOM. Do not put a self-asserted provenance statement in the tarball.
5. Publish platform packages before the launcher package with
   `npm publish --provenance`; npm's OIDC flow creates the provenance evidence.
6. Freeze all five tarballs in one checksum-verified GitHub draft-release
   bundle before the first npm publish. Every retry must reuse that bundle.
7. Smoke test `npm pack` and an offline global install from the packed tarballs.
8. Publish only after the release assets are attested and checksummed.

The files here are release inputs; their presence does not mean the npm package
has already been published.
