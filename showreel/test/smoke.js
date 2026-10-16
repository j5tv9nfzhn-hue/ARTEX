/* Smoke test: run the renderer across the full 60 s timeline on a stub canvas.
 * Catches runtime errors in scene code without a browser.
 *   node test/smoke.js
 */
'use strict';
const path = require('path');
const fs = require('fs');

/* ---- minimal DOM + canvas stubs ----------------------------------------- */
function makeCtx() {
  const noop = () => {};
  const state = { _font: '', _size: 24 };
  const ctxObj = {
    canvas: { width: 1920, height: 1080 },
    measureText(s) {
      return {
        width: String(s).length * 0.62 * state._size,
        actualBoundingBoxAscent: state._size * 0.72,
        actualBoundingBoxDescent: state._size * 0.2
      };
    },
    createImageData: (w, h) => ({ width: w, height: h, data: new Uint8ClampedArray(w * h * 4) }),
    putImageData: noop,
    getTransform: () => ({ a: 1, b: 0, c: 0, d: 1, e: 0, f: 0 })
  };
  return new Proxy(ctxObj, {
    get(target, prop) {
      if (prop in target) return target[prop];
      if (prop === 'font') return state._font;
      return noop;
    },
    set(target, prop, value) {
      if (prop === 'font') {
        state._font = String(value);
        const m = /(\d+(?:\.\d+)?)px/.exec(state._font);
        state._size = m ? parseFloat(m[1]) : 24;
      } else {
        target[prop] = value;
      }
      return true;
    }
  });
}

function makeCanvas() {
  return { width: 300, height: 150, getContext: () => makeCtx(), style: {} };
}

global.document = {
  createElement: (tag) => (tag === 'canvas' ? makeCanvas() : { style: {} }),
  fonts: { add: () => {}, ready: Promise.resolve() }
};
global.window = { addEventListener: () => {} };
global.performance = { now: () => Date.now() };

/* ---- load modules (concatenated so consts share scope) -------------------- */
const files = ['js/hyperframes.js', 'js/geometry.js', 'js/ink.js', 'js/mascot.js', 'js/scenes.js', 'js/render.js'];
const src = files.map((p) => fs.readFileSync(path.join(__dirname, '..', p), 'utf8')).join('\n');
(0, eval)(src + '\n;globalThis.__T = { HF, GEO, INK, MASCOT, SCENES, RENDER };');
const { HF, SCENES, RENDER } = globalThis.__T;

/* ---- run ----------------------------------------------------------------- */
const ctx = makeCtx();
SCENES.measureHead(ctx);
console.log('HEAD layout:', SCENES.HEAD());

let n = 0;
const failures = [];
for (let f = 0; f < 3600; f++) {
  const t = f / 60;
  try {
    RENDER.frame(ctx, t);
    n++;
  } catch (e) {
    failures.push({ t: t.toFixed(3), msg: e.message, stack: e.stack.split('\n').slice(0, 4).join(' | ') });
    if (failures.length > 8) break;
  }
}

console.log(`rendered ${n} frames`);
if (failures.length) {
  console.log('FAILURES:');
  for (const f of failures) console.log(' t=' + f.t, f.msg, '\n   ', f.stack);
  process.exit(1);
}

/* ---- loop bookkeeping ---------------------------------------------------- */
console.log('bar at t=0:', HF.barIndex(0), HF.BAR_NAMES[HF.barIndex(0)]);
console.log('beat at 0 →', (Math.floor(0 / HF.BEAT + 1e-9) % 120) + 1,
  '| beat at 60 →', (Math.floor(60 / HF.BEAT + 1e-9) % 120) + 1);
console.log('fig at 60 →', HF.BAR_NAMES[(Math.floor(60 / HF.BAR) % 30)]);
console.log('SMOKE OK');
