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

/**
 * Dark palette — tuned for docs readability (Mintlify always-dark cards).
 * Slightly deeper bg than surface so light-page contrast is crisp.
 */
const darkPalette = {
  bg: '#0f1416',
  fg: '#e8ebe8', // soft starlight (less harsh than pure #f3f0e8)
  lineNumber: '#3d4749',
  comment: '#6b7572',
  keyword: '#c6a6ea', // dust-violet
  string: '#7dcea0', // spectral-green softened
  func: '#8edbd2', // ion-cyan
  constant: '#e0b06a', // copper-amber softened
  type: '#7ab8e8', // oxygen-blue softened
  parameter: '#b0b7b3', // dust
  operator: '#8b9490',
  inserted: '#68d2a3',
  deleted: '#ff7c78',
};

/** Light palette — available if dual-theme code is re-enabled later. */
const lightPalette = {
  bg: '#fffdf8',
  fg: '#182023',
  lineNumber: '#cfd3ce',
  comment: '#7b8583',
  keyword: '#6d43b8',
  string: '#087c58',
  func: '#176f70',
  constant: '#8c5a15',
  type: '#1d6fae',
  parameter: '#5d6868',
  operator: '#7b8583',
  inserted: '#087c58',
  deleted: '#c42e45',
};

/** TextMate scopes — enough coverage for bash/json/ts/go in docs. */
function tokenColors(c) {
  return [
    {
      scope: ['comment', 'punctuation.definition.comment', 'string.comment'],
      settings: { foreground: c.comment, fontStyle: 'italic' },
    },
    {
      scope: [
        'string',
        'string.quoted',
        'string.template',
        'punctuation.definition.string',
        'string.regexp',
      ],
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
        'keyword.operator.new',
        'storage',
        'storage.type',
        'storage.modifier',
      ],
      settings: { foreground: c.keyword },
    },
    {
      scope: ['keyword.operator', 'keyword.operator.assignment', 'keyword.operator.comparison'],
      settings: { foreground: c.operator },
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
      scope: [
        'entity.name.function',
        'support.function',
        'meta.function-call entity.name',
        'entity.name.command',
        'support.function.builtin',
      ],
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
      scope: [
        'variable.parameter',
        'variable.other.option',
        'entity.name.section.option',
        'constant.other.option',
      ],
      settings: { foreground: c.parameter },
    },
    {
      scope: ['variable', 'variable.other', 'variable.language'],
      settings: { foreground: c.fg },
    },
    {
      scope: [
        'support.type.property-name',
        'entity.other.attribute-name',
        'meta.object-literal.key',
        'string.json support.type.property-name',
      ],
      settings: { foreground: c.type },
    },
    {
      scope: [
        'punctuation',
        'meta.brace',
        'punctuation.separator',
        'punctuation.terminator',
        'punctuation.definition.array',
        'punctuation.definition.dictionary',
      ],
      settings: { foreground: c.operator },
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

/*
 * Retained for a possible future per-theme code mode; currently unused by
 * design — code cards are always-dark in both page themes
 * (user arbitration 2026-07-16, see task prd.md amendments).
 */
export const carinaLight = makeTheme('carina-light', 'light', lightPalette);
