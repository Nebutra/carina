# Homebrew Formula Source

This directory contains the source template for the live
[`Nebutra/homebrew-tap`](https://github.com/Nebutra/homebrew-tap).

On a `v<version>` tag, `.github/workflows/release.yml`:

1. builds native Apple Silicon and Intel archives;
2. installs each archive through a temporary Homebrew tap and runs `brew test`;
3. publishes checksums and GitHub build provenance;
4. renders `Formula/carina.rb` with both archive SHA-256 values;
5. updates the official tap through a repository-scoped deploy key.

Render a Formula locally with:

```bash
VERSION=0.6.0 \
DARWIN_ARM64_SHA256=<sha256> \
DARWIN_AMD64_SHA256=<sha256> \
./scripts/render-homebrew-formula.sh
```

The Formula does not auto-start `carina-daemon`. The optional Headroom context
engine is not bundled in the Homebrew package until all supported architectures
have a reproducible standalone artifact.
