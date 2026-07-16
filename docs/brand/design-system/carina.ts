import {defineTheme} from '@astryxdesign/core/theme';
import {neutralTheme} from '@astryxdesign/theme-neutral';

export const carinaTheme = defineTheme({
  name: 'carina',
  extends: neutralTheme,
  color: {
    accent: '#8edbd2',
    neutralStyle: 'cool',
    contrast: 'standard',
  },
  typography: {
    scale: {base: 14, ratio: 1.2},
    body: {
      family: 'Geist Sans',
      fallbacks: '-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
    },
    heading: {
      family: 'Geist Sans',
      fallbacks: '-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
      weight: 'semibold',
    },
    code: {
      family: 'Geist Mono',
      fallbacks: '"SFMono-Regular", Consolas, monospace',
    },
  },
  radius: {base: 4, multiplier: 1},
  motion: {
    fast: 120,
    medium: 220,
    ratio: 0.55,
    easing: 'cubic-bezier(0.2, 0, 0, 1)',
  },
  tokens: {
    '--font-family-brand': '"Carina Display Alpha", Georgia, serif',
    '--font-family-serif':
      '"Newsreader Variable", "Newsreader", Georgia, "Times New Roman", serif',
    '--font-family-display': '"Geist Sans", ui-sans-serif, system-ui, sans-serif',
    '--font-family-sans': '"Geist Sans", ui-sans-serif, system-ui, sans-serif',
    '--font-family-mono':
      '"Geist Mono", "SFMono-Regular", Consolas, monospace',
    '--color-brand-mark': ['#8e4053', '#8e4053'],
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
  components: {
    button: {
      base: {
        borderRadius: '6px',
        '--button-press-scale': 'scale(0.98)',
      },
    },
    card: {
      base: {
        borderRadius: '8px',
        padding: '24px',
      },
    },
  },
});
