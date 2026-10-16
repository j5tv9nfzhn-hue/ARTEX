/* Pixel-level loop seam test using the same Canvas renderer as MP4 export.
 * Requires the project's @napi-rs/canvas dev dependency.
 *   node test/loop.js
 */
'use strict';
const fs = require('fs');
const path = require('path');
const { createCanvas, GlobalFonts } = require('@napi-rs/canvas');

const ROOT = path.join(__dirname, '..');
for (const file of ['Geist-SemiBold.ttf', 'Geist-Bold.ttf', 'Geist-Black.ttf']) {
  const font = path.join(ROOT, 'vendor', file);
  if (fs.existsSync(font)) GlobalFonts.registerFromPath(font, 'Geist');
}

global.document = {
  createElement: (tag) => (tag === 'canvas' ? createCanvas(300, 150) : { style: {} }),
  fonts: { add: () => {}, ready: Promise.resolve() }
};
global.window = { addEventListener: () => {} };
global.performance = { now: () => Date.now() };

const modules = [
  'js/hyperframes.js', 'js/geometry.js', 'js/ink.js', 'js/mascot.js',
  'js/scenes.js', 'js/render.js'
];
const source = modules.map((file) => fs.readFileSync(path.join(ROOT, file), 'utf8')).join('\n');
(0, eval)(source + '\n;globalThis.__T = { HF, SCENES, RENDER };');
const { HF, SCENES, RENDER } = globalThis.__T;
const canvas = createCanvas(HF.STAGE.W, HF.STAGE.H);
const ctx = canvas.getContext('2d');
SCENES.measureHead(ctx);

function pixelsAt(t) {
  ctx.clearRect(0, 0, HF.STAGE.W, HF.STAGE.H);
  RENDER.frame(ctx, t);
  return Buffer.from(ctx.getImageData(0, 0, HF.STAGE.W, HF.STAGE.H).data);
}

const first = pixelsAt(0);
const endpoint = pixelsAt(HF.DURATION);
if (!first.equals(endpoint)) {
  let differing = 0;
  for (let i = 0; i < first.length; i++) if (first[i] !== endpoint[i]) differing++;
  console.error(`Loop seam mismatch: ${differing} / ${first.length} pixel channels differ`);
  process.exit(1);
}
console.log(`LOOP OK — frame 0 equals frame ${HF.FRAMES} pixel-for-pixel (${HF.STAGE.W}×${HF.STAGE.H})`);
