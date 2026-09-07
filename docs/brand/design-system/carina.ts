/**
 * Carina's framework-neutral theme contract.
 *
 * This file deliberately has no runtime dependency on an external design
 * system. Web, Tauri, documentation, and future adapters can consume the
 * same typed semantic values without requiring a particular component
 * supplier to be installed in every package.
 */

export type ThemeMode = 'light' | 'dark'
export type ColorPair = readonly [light: string, dark: string]

export interface CarinaTheme {
  readonly name: 'carina'
  readonly color: {
    readonly accent: string
    readonly neutralStyle: 'cool'
    readonly contrast: 'standard'
  }
  readonly typography: {
    readonly scale: { readonly base: number; readonly ratio: number }
    readonly body: { readonly family: string; readonly fallbacks: string }
    readonly heading: { readonly family: string; readonly fallbacks: string; readonly weight: 'semibold' }
    readonly code: { readonly family: string; readonly fallbacks: string }
  }
  readonly radius: { readonly base: 4; readonly multiplier: 1 }
  readonly motion: {
    readonly fast: 120
    readonly medium: 220
    readonly slow: 480
    readonly panel: 320
    readonly overlay: 220
    readonly ratio: 0.55
    readonly easing: {
      readonly standard: 'cubic-bezier(0.2, 0, 0, 1)'
      readonly enter: 'cubic-bezier(0.16, 1, 0.3, 1)'
      readonly exit: 'cubic-bezier(0.7, 0, 0.84, 0)'
    }
  }
  readonly tokens: Readonly<Record<string, string | ColorPair>>
}

const bodySans = 'Geist Sans'
const sansFallbacks = '-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif'
const codeMono = 'Geist Mono'
const monoFallbacks = '"SFMono-Regular", Consolas, monospace'

/** Canonical semantic tokens from design-tokens.json, expressed as CSS-ready values. */
export const carinaTheme = {
  name: 'carina',
  color: {
    accent: '#8edbd2',
    neutralStyle: 'cool',
    contrast: 'standard',
  },
  typography: {
    scale: { base: 14, ratio: 1.2 },
    body: { family: bodySans, fallbacks: sansFallbacks },
    heading: { family: bodySans, fallbacks: sansFallbacks, weight: 'semibold' },
    code: { family: codeMono, fallbacks: monoFallbacks },
  },
  radius: { base: 4, multiplier: 1 },
  motion: {
    fast: 120,
    medium: 220,
    slow: 480,
    panel: 320,
    overlay: 220,
    ratio: 0.55,
    easing: {
      standard: 'cubic-bezier(0.2, 0, 0, 1)',
      enter: 'cubic-bezier(0.16, 1, 0.3, 1)',
      exit: 'cubic-bezier(0.7, 0, 0.84, 0)',
    },
  },
  tokens: {
    '--font-family-brand': '"Carina Display Alpha", Georgia, serif',
    '--font-family-serif': '"Newsreader Variable", "Newsreader", Georgia, "Times New Roman", serif',
    '--font-family-display': '"Geist Sans", ui-sans-serif, system-ui, sans-serif',
    '--font-family-sans': '"Geist Sans", ui-sans-serif, system-ui, sans-serif',
    '--font-family-mono': '"Geist Mono", "SFMono-Regular", Consolas, monospace',
    '--color-brand-mark': ['#8e4053', '#8e4053'],
    '--color-terminal-brand-display': ['#8e4053', '#a86d79'],
    '--color-accent': ['#176f70', '#8edbd2'],
    '--color-accent-muted': ['#dcefed', '#19302f'],
    '--color-on-accent': ['#ffffff', '#0b1716'],
    '--color-background-body': ['#f5f3ed', '#0d1214'],
    '--color-background-surface': ['#fffdf8', '#141b1d'],
    '--color-background-card': ['#fffdf8', '#141b1d'],
    '--color-background-popover': ['#eceae3', '#1c2527'],
    '--color-text-primary': ['#182023', '#f3f0e8'],
    '--color-text-secondary': ['#5d6868', '#b0b7b3'],
    '--color-text-disabled': ['#7b8583', '#7e8885'],
    '--color-border': ['#cfd3ce', '#344144'],
    '--color-border-emphasized': ['#176f70', '#8edbd2'],
    '--color-success': ['#087c58', '#68d2a3'],
    '--color-warning': ['#8c5a15', '#e8a85f'],
    '--color-error': ['#c42e45', '#ff7c78'],
    '--color-data-categorical-blue': ['#176f70', '#78bff2'],
    '--color-data-categorical-orange': ['#8c5a15', '#e8a85f'],
    '--color-data-categorical-purple': ['#704e9e', '#c6a6ea'],
  },
} as const satisfies CarinaTheme

export const carinaThemeModes = {
  light: Object.fromEntries(Object.entries(carinaTheme.tokens).map(([key, value]) => [key, Array.isArray(value) ? value[0] : value])),
  dark: Object.fromEntries(Object.entries(carinaTheme.tokens).map(([key, value]) => [key, Array.isArray(value) ? value[1] : value])),
} as const satisfies Record<ThemeMode, Readonly<Record<string, string>>>
