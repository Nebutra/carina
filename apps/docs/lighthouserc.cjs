/**
 * Lighthouse CI — static docs site performance / a11y floor.
 * Run: pnpm lighthouse (builds + previews + autorun)
 *
 * Thresholds tuned for a content-heavy docs site with search/fonts.
 * Performance 0.9 is the product bar from the original polish brief.
 */
module.exports = {
  ci: {
    collect: {
      startServerCommand: 'pnpm preview --host 127.0.0.1 --port 4322',
      startServerReadyPattern: 'Local',
      startServerReadyTimeout: 60000,
      url: [
        'http://127.0.0.1:4322/',
        'http://127.0.0.1:4322/getting-started/quickstart/',
        'http://127.0.0.1:4322/api/methods/',
      ],
      numberOfRuns: 1,
      settings: {
        preset: 'desktop',
        // CI chrome flags
        chromeFlags: '--no-sandbox --disable-dev-shm-usage',
      },
    },
    assert: {
      assertions: {
        'categories:performance': ['error', { minScore: 0.9 }],
        'categories:accessibility': ['error', { minScore: 0.9 }],
        'categories:best-practices': ['warn', { minScore: 0.9 }],
        'categories:seo': ['warn', { minScore: 0.9 }],
        'cumulative-layout-shift': ['warn', { maxNumericValue: 0.1 }],
      },
    },
    upload: {
      target: 'temporary-public-storage',
    },
  },
};
