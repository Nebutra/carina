import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import sharp from 'sharp';
import stringWidth from 'string-width';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(scriptDir, '../../..');
const docsRoot = resolve(repoRoot, 'apps/docs');
const outputDir = resolve(docsRoot, 'public/images');
const fontPath = resolve(outputDir, '../fonts/geist-mono-latin-variable.woff2');
const brandVariablesPath = resolve(repoRoot, 'docs/brand/design-system/variables.css');
const checkOnly = process.argv.includes('--check');

const fontBuffer = readFileSync(fontPath);
const brandVariablesBuffer = readFileSync(brandVariablesPath);
const brandVariables = brandVariablesBuffer.toString('utf8');

function readBrandHex(name) {
  const match = new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6})\\s*;`).exec(brandVariables);
  if (!match) throw new Error(`Missing six-digit ${name} in ${relative(repoRoot, brandVariablesPath)}`);
  return match[1];
}

const DEFAULT_FG = readBrandHex('--carina-code-fg');
const DEFAULT_BG = readBrandHex('--carina-code-void');
const CELL_WIDTH = 10;
const CELL_HEIGHT = 20;
const FONT_SIZE = 15;
const PADDING = 28;

const captures = [
  {
    id: 'session',
    output: 'tui-session.webp',
    width: 120,
    height: 30,
    sources: [
      {
        path: 'crates/carina-tui/src/app/snapshots/carina_tui__app__render__transcript_tests__visual_density_en_120.snap',
        row: 0,
      },
      {
        path: 'crates/carina-tui/src/app/snapshots/carina_tui__app__render__transcript_tests__composer_chrome_en_running_120.snap',
        row: 28,
      },
    ],
  },
  {
    id: 'review',
    output: 'tui-review.webp',
    width: 160,
    height: 40,
    sources: [
      {
        path: 'crates/carina-tui/src/app/snapshots/carina_tui__app__render__transcript_tests__fullscreen_patch_review_en_160.snap',
        row: 0,
      },
    ],
  },
  {
    id: 'review-zh-cn',
    output: 'tui-review-zh-cn.webp',
    width: 160,
    height: 40,
    sources: [
      {
        path: 'crates/carina-tui/src/app/snapshots/carina_tui__app__render__transcript_tests__fullscreen_patch_review_zh_hans_160.snap',
        row: 0,
      },
    ],
  },
];

const stylePattern = /^\((\d+),(\d+)\)\.\.\((\d+),(\d+)\) \((Rgb\(\d+, \d+, \d+\)|Reset), (Rgb\(\d+, \d+, \d+\)|Reset), (.+)\)$/;
const supportedModifiers = new Set(['NONE', 'BOLD', 'DIM', 'ITALIC', 'UNDERLINED']);

function digest(buffer) {
  return createHash('sha256').update(buffer).digest('hex');
}

function parseColor(value, fallback) {
  if (value === 'Reset') return fallback;
  const match = /^Rgb\((\d+), (\d+), (\d+)\)$/.exec(value);
  if (!match) throw new Error(`Unsupported snapshot color: ${value}`);
  return `#${match
    .slice(1)
    .map((part) => Number(part).toString(16).padStart(2, '0'))
    .join('')}`;
}

function validateModifiers(value, sourcePath) {
  const names = value.split(' | ');
  const unsupported = names.filter((name) => !supportedModifiers.has(name));
  if (
    unsupported.length > 0 ||
    new Set(names).size !== names.length ||
    (names.includes('NONE') && names.length !== 1)
  ) {
    throw new Error(`${sourcePath}: unsupported snapshot modifiers ${value}`);
  }
  return value;
}

function emptyCell() {
  return { char: ' ', fg: DEFAULT_FG, bg: DEFAULT_BG, modifiers: 'NONE' };
}

function putLine(cells, row, line, width, sourcePath) {
  let column = 0;
  for (const glyph of Array.from(line)) {
    const glyphWidth = stringWidth(glyph);
    if (glyphWidth === 0) {
      const previous = cells[row][Math.max(0, column - 1)];
      previous.char += glyph;
      continue;
    }
    if (column + glyphWidth > width) {
      throw new Error(`${sourcePath}:${row + 1} exceeds declared ${width}-cell width`);
    }
    cells[row][column].char = glyph;
    for (let continuation = 1; continuation < glyphWidth; continuation += 1) {
      cells[row][column + continuation].char = '';
    }
    column += glyphWidth;
  }
}

function parseSnapshot(sourcePath) {
  const absolutePath = resolve(repoRoot, sourcePath);
  const raw = readFileSync(absolutePath, 'utf8');
  const sizeMatch = /^size=(\d+)x(\d+)$/m.exec(raw);
  if (!sizeMatch) throw new Error(`${sourcePath}: missing size header`);
  const width = Number(sizeMatch[1]);
  const height = Number(sizeMatch[2]);
  const frameStart = raw.indexOf('\n', sizeMatch.index + sizeMatch[0].length) + 1;
  const stylesStart = raw.indexOf('\nstyles:\n', frameStart);
  if (stylesStart < 0) throw new Error(`${sourcePath}: missing styles section`);

  const cells = Array.from({ length: height }, () =>
    Array.from({ length: width }, emptyCell),
  );
  const lines = raw.slice(frameStart, stylesStart).split('\n');
  if (lines.length > height) {
    throw new Error(`${sourcePath}: frame has ${lines.length} rows, expected at most ${height}`);
  }
  lines.forEach((line, row) => putLine(cells, row, line, width, sourcePath));

  const styleLines = raw
    .slice(stylesStart + '\nstyles:\n'.length)
    .trim()
    .split('\n')
    .filter(Boolean);
  for (const styleLine of styleLines) {
    const match = stylePattern.exec(styleLine);
    if (!match) throw new Error(`${sourcePath}: unsupported style run ${styleLine}`);
    const [, startXRaw, startYRaw, endXRaw, endYRaw, fgRaw, bgRaw, modifiersRaw] = match;
    const modifiers = validateModifiers(modifiersRaw, sourcePath);
    const startX = Number(startXRaw);
    const startY = Number(startYRaw);
    const endX = Number(endXRaw);
    const endY = Number(endYRaw);
    if (startY !== endY || startY >= height || startX > endX || endX > width) {
      throw new Error(`${sourcePath}: invalid style geometry ${styleLine}`);
    }
    for (let column = startX; column < endX; column += 1) {
      cells[startY][column].fg = parseColor(fgRaw, DEFAULT_FG);
      cells[startY][column].bg = parseColor(bgRaw, DEFAULT_BG);
      cells[startY][column].modifiers = modifiers;
    }
  }

  return { cells, width, height, sourcePath, sha256: digest(Buffer.from(raw)) };
}

function composeCapture(capture) {
  const frame = Array.from({ length: capture.height }, () =>
    Array.from({ length: capture.width }, emptyCell),
  );
  const sourceMetadata = [];
  for (const source of capture.sources) {
    const parsed = parseSnapshot(source.path);
    if (parsed.width !== capture.width) {
      throw new Error(`${source.path}: ${parsed.width} columns cannot compose into ${capture.width}`);
    }
    if (source.row + parsed.height > capture.height && source.row !== 0) {
      throw new Error(`${source.path}: rows exceed ${capture.id} capture geometry`);
    }
    const rows = Math.min(parsed.height, capture.height - source.row);
    for (let row = 0; row < rows; row += 1) {
      frame[source.row + row] = parsed.cells[row].map((cell) => ({ ...cell }));
    }
    sourceMetadata.push({
      path: source.path,
      row: source.row,
      sha256: parsed.sha256,
    });
  }
  return { frame, sourceMetadata };
}

function escapeXml(value) {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&apos;');
}

function renderSvg(frame, width, height, fontData) {
  const imageWidth = width * CELL_WIDTH + PADDING * 2;
  const imageHeight = height * CELL_HEIGHT + PADDING * 2;
  const backgrounds = [];
  const glyphs = [];

  for (let row = 0; row < height; row += 1) {
    for (let column = 0; column < width; column += 1) {
      const cell = frame[row][column];
      if (cell.bg !== DEFAULT_BG) {
        backgrounds.push(
          `<rect x="${PADDING + column * CELL_WIDTH}" y="${PADDING + row * CELL_HEIGHT}" width="${CELL_WIDTH}" height="${CELL_HEIGHT}" fill="${cell.bg}"/>`,
        );
      }
      if (!cell.char || cell.char === ' ') continue;
      const weight = cell.modifiers.includes('BOLD') ? 700 : 450;
      const style = cell.modifiers.includes('ITALIC') ? 'italic' : 'normal';
      const decoration = cell.modifiers.includes('UNDERLINED') ? 'underline' : 'none';
      glyphs.push(
        `<text x="${PADDING + column * CELL_WIDTH}" y="${PADDING + row * CELL_HEIGHT + 15}" fill="${cell.fg}" font-family="Carina Capture Mono, monospace" font-size="${FONT_SIZE}" font-weight="${weight}" font-style="${style}" text-decoration="${decoration}">${escapeXml(cell.char)}</text>`,
      );
    }
  }

  return {
    imageWidth,
    imageHeight,
    svg: Buffer.from(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="${imageWidth}" height="${imageHeight}" viewBox="0 0 ${imageWidth} ${imageHeight}">
  <style>@font-face{font-family:'Carina Capture Mono';src:url(data:font/woff2;base64,${fontData}) format('woff2');font-weight:100 900;font-style:normal}</style>
  <rect width="100%" height="100%" fill="${DEFAULT_BG}"/>
  <g shape-rendering="crispEdges">${backgrounds.join('')}</g>
  <g xml:space="preserve" text-rendering="geometricPrecision">${glyphs.join('')}</g>
</svg>`),
  };
}

function assertOrWrite(path, expected) {
  if (checkOnly) {
    let current;
    try {
      current = readFileSync(path);
    } catch {
      throw new Error(`${relative(repoRoot, path)} is missing; run pnpm render:tui-captures`);
    }
    if (!current.equals(expected)) {
      throw new Error(`${relative(repoRoot, path)} is stale; run pnpm render:tui-captures`);
    }
    return;
  }
  writeFileSync(path, expected);
}

async function verifyOrWriteImage(path, rendered, expectedWidth, expectedHeight) {
  if (!checkOnly) {
    writeFileSync(path, rendered);
    return rendered;
  }

  let current;
  try {
    current = readFileSync(path);
  } catch {
    throw new Error(`${relative(repoRoot, path)} is missing; run pnpm render:tui-captures`);
  }

  const metadata = await sharp(current).metadata();
  if (metadata.width !== expectedWidth || metadata.height !== expectedHeight) {
    throw new Error(
      `${relative(repoRoot, path)} has ${metadata.width}x${metadata.height}; expected ${expectedWidth}x${expectedHeight}`,
    );
  }

  // WebP encoder output is not byte-stable across libvips platforms. In check
  // mode the manifest locks the committed image hash and every source input;
  // dimensions additionally prove the asset remains decodable.
  return current;
}

const fontData = fontBuffer.toString('base64');
const manifest = {
  generatedBy: 'apps/docs/scripts/render-tui-captures.mjs',
  renderer: 'production Ratatui fixture snapshots',
  inputs: {
    brandVariables: {
      path: relative(repoRoot, brandVariablesPath),
      sha256: digest(brandVariablesBuffer),
    },
    font: {
      path: relative(repoRoot, fontPath),
      sha256: digest(fontBuffer),
    },
  },
  captures: [],
};

for (const capture of captures) {
  const { frame, sourceMetadata } = composeCapture(capture);
  const { svg, imageWidth, imageHeight } = renderSvg(
    frame,
    capture.width,
    capture.height,
    fontData,
  );
  const image = await sharp(svg)
    .webp({ quality: 92, effort: 6, smartSubsample: true })
    .toBuffer();
  const outputPath = resolve(outputDir, capture.output);
  const canonicalImage = await verifyOrWriteImage(
    outputPath,
    image,
    imageWidth,
    imageHeight,
  );
  manifest.captures.push({
    id: capture.id,
    output: `apps/docs/public/images/${capture.output}`,
    terminalCells: `${capture.width}x${capture.height}`,
    pixels: `${imageWidth}x${imageHeight}`,
    sha256: digest(canonicalImage),
    sources: sourceMetadata,
  });
}

const manifestPath = resolve(outputDir, 'tui-captures.json');
const manifestBuffer = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`);
assertOrWrite(manifestPath, manifestBuffer);

console.log(
  `${checkOnly ? 'Verified' : 'Rendered'} ${captures.length} production TUI captures`,
);
