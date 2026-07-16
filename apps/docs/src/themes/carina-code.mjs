/**
 * Carina syntax-highlight themes for Expressive Code (dark + light).
 *
 * SOLE hex exception in this package: Shiki/TextMate themes require concrete
 * colors. Every hex below mirrors a brand token from
 * docs/brand/design-system/variables.css (see per-entry comments).
 *
 * Consumed by astro.config.mjs → starlight.expressiveCode.themes.
 * EC accepts plain VS Code theme JSON objects ({ name, type, colors,
 * tokenColors }) and wraps them in ExpressiveCodeTheme internally.
 */

/** Dark palette — mirrors the `:root` / [data-theme='dark'] brand block. */
const darkPalette = {
  bg: '#141b1d', // --carina-surface (--color-background-surface)
  fg: '#f3f0e8', // --carina-starlight (--color-text-primary)
  lineNumber: '#344144', // --carina-border (very faint gutter ink)
  comment: '#7e8885', // --carina-disabled (--color-text-disabled)
  keyword: '#c6a6ea', // --carina-dust-violet
  string: '#68d2a3', // --carina-spectral-green
  func: '#8edbd2', // --carina-ion-cyan
  constant: '#e8a85f', // --carina-copper-amber
  type: '#78bff2', // --carina-oxygen-blue
  inserted: '#68d2a3', // --carina-spectral-green
  deleted: '#ff7c78', // --carina-event-red
};

/** Light palette — mirrors the [data-theme='light'] brand block. */
const lightPalette = {
  bg: '#fffdf8', // light --color-background-surface
  fg: '#182023', // light --color-text-primary
  lineNumber: '#cfd3ce', // light --color-border (very faint gutter ink)
  comment: '#7b8583', // light --color-text-disabled
  keyword: '#6d43b8', // light --color-capability-agent (violet family)
  string: '#087c58', // light --color-success (spectral-green family)
  func: '#176f70', // light --color-accent (ion-cyan family)
  constant: '#8c5a15', // light --color-warning (copper-amber family)
  type: '#1d6fae', // light --color-info (oxygen-blue family)
  inserted: '#087c58', // light --color-success
  deleted: '#c42e45', // light --color-danger (event-red family)
};

/** Standard TextMate scope coverage from a Carina palette. */
function tokenColors(c) {
  return [
    {
      scope: ['comment', 'punctuation.definition.comment'],
      settings: { foreground: c.comment, fontStyle: 'italic' },
    },
    {
      scope: ['string', 'string.quoted', 'punctuation.definition.string', 'string.regexp'],
      settings: { foreground: c.string },
    },
    {
      scope: ['constant.character.escape'],
      settings: { foreground: c.constant },
    },
    {
      scope: [
        'keyword',
        'keyword.control',
        'keyword.operator',
        'storage',
        'storage.type',
        'storage.modifier',
      ],
      settings: { foreground: c.keyword },
    },
    {
      scope: [
        'constant',
        'constant.numeric',
        'constant.language',
        'support.constant',
        'variable.other.constant',
      ],
      settings: { foreground: c.constant },
    },
    {
      scope: ['entity.name.function', 'support.function', 'meta.function-call entity.name'],
      settings: { foreground: c.func },
    },
    {
      scope: [
        'entity.name.type',
        'entity.name.class',
        'entity.name.namespace',
        'entity.other.inherited-class',
        'support.type',
        'support.class',
        'entity.name.tag',
      ],
      settings: { foreground: c.type },
    },
    {
      scope: ['variable', 'variable.parameter', 'variable.other'],
      settings: { foreground: c.fg },
    },
    {
      scope: [
        'support.type.property-name',
        'entity.other.attribute-name',
        'meta.object-literal.key',
      ],
      settings: { foreground: c.type },
    },
    {
      scope: ['punctuation', 'meta.brace', 'punctuation.separator', 'punctuation.terminator'],
      settings: { foreground: c.fg },
    },
    {
      scope: ['markup.inserted'],
      settings: { foreground: c.inserted },
    },
    {
      scope: ['markup.deleted'],
      settings: { foreground: c.deleted },
    },
    {
      scope: ['markup.heading', 'entity.name.section'],
      settings: { foreground: c.type, fontStyle: 'bold' },
    },
    {
      scope: ['markup.bold'],
      settings: { fontStyle: 'bold' },
    },
    {
      scope: ['markup.italic'],
      settings: { fontStyle: 'italic' },
    },
  ];
}

function makeTheme(name, type, c) {
  return {
    name,
    type,
    colors: {
      'editor.background': c.bg,
      'editor.foreground': c.fg,
      'editorLineNumber.foreground': c.lineNumber,
    },
    tokenColors: tokenColors(c),
  };
}

export const carinaDark = makeTheme('carina-dark', 'dark', darkPalette);
export const carinaLight = makeTheme('carina-light', 'light', lightPalette);
