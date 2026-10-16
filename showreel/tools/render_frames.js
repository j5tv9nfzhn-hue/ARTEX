/* =============================================================================
 * tools/render_frames.js — deterministic frame exporter ("HyperFrames" out).
 * Renders the showreel as PNG frames (or a single frame) outside the browser,
 * e.g. for DaVinci Resolve. Requires:  npm i @napi-rs/canvas  and the Geist
 * TTFs from the `geist` npm package (static instances register per weight).
 *
 *   node tools/render_frames.js --out out/ --t 0,12.5,47.7
 *   node tools/render_frames.js --out out/ --range 0:60:0.5
 *   node tools/render_frames.js --out out/ --frames 0:3599:1
 * ========================================================================== */

'use strict';
const path = require('path');
const fs = require('fs');

let createCanvas, GlobalFonts;
try {
  ({ createCanvas, GlobalFonts } = require('@napi-rs/canvas'));
} catch (e) {
  console.error('Install the renderer first:  npm i @napi-rs/canvas');
  process.exit(1);
}

const ROOT = path.join(__dirname, '..');
const args = process.argv.slice(2);
function arg(name, dflt) {
  const i = args.indexOf('--' + name);
  return i >= 0 ? args[i + 1] : dflt;
}

const outDir = arg('out', path.join(ROOT, 'out'));
const fontDir = arg('fonts', path.join(ROOT, 'vendor'));

// register Geist static faces under one family so weights match
for (const [file, weight] of [
  ['Geist-SemiBold.ttf', '600'], ['Geist-Bold.ttf', '700'],
  ['Geist-Black.ttf', '900'], ['Geist-Variable.ttf', '100 900']
]) {
  const p = path.join(fontDir, file);
  if (fs.existsSync(p)) GlobalFonts.registerFromPath(p, 'Geist');
}

/* ---- stub DOM ------------------------------------------------------------ */
function makeCanvasNode() {
  return createCanvas(300, 150);
}
global.document = {
  createElement: (tag) => (tag === 'canvas' ? makeCanvasNode() : { style: {} }),
  fonts: { add: () => {}, ready: Promise.resolve() }
};
global.window = { addEventListener: () => {} };
global.performance = { now: () => Date.now() };

/* ---- load showreel modules ----------------------------------------------- */
const files = ['js/hyperframes.js', 'js/geometry.js', 'js/ink.js', 'js/mascot.js', 'js/scenes.js', 'js/render.js'];
const src = files.map((p) => fs.readFileSync(path.join(ROOT, p), 'utf8')).join('\n');
(0, eval)(src + '\n;globalThis.__T = { HF, SCENES, RENDER };');
const { HF, SCENES, RENDER } = globalThis.__T;

/* ---- build time list ----------------------------------------------------- */
function parseList(spec) {
  return spec.split(',').map(parseFloat);
}
function parseRange(spec, div) {
  // a:b:step
  const [a, b, s] = spec.split(':').map(parseFloat);
  const out = [];
  const step = s || 1 / div;
  for (let v = a; v < b - 1e-9; v += step) out.push(div ? v / div : v);
  return out;
}

let times = [];
if (arg('t')) times = parseList(arg('t'));
else if (arg('range')) times = parseRange(arg('range'), 1);
else if (arg('frames')) times = parseRange(arg('frames'), HF.FPS).map((f) => f); // already divided
else times = [0, 12.5, 47.7];

/* ---- render -------------------------------------------------------------- */
fs.mkdirSync(outDir, { recursive: true });
const { W, H } = HF.STAGE;
const canvas = createCanvas(W, H);
const ctx = canvas.getContext('2d');
SCENES.measureHead(ctx);

for (const tSec of times) {
  const t = ((tSec % 60) + 60) % 60;
  RENDER.frame(ctx, t);
  const frame = Math.round(tSec * HF.FPS);
  const file = path.join(outDir, `frame_${String(frame).padStart(4, '0')}_t${tSec.toFixed(2)}.png`);
  fs.writeFileSync(file, canvas.toBuffer('image/png'));
  console.log('wrote', file);
}
console.log('done —', times.length, 'frames');
