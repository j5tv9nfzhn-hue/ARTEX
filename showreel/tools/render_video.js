/* ============================================================================
 * tools/render_video.js — render the deterministic 60 s showreel to MP4.
 *
 * Requires @napi-rs/canvas (npm i) and ffmpeg with libx264. The script renders
 * one PNG at a time and pipes it to ffmpeg, so it never creates a 3600-frame
 * image sequence on disk.
 *
 *   node tools/render_video.js
 *   node tools/render_video.js --out ARTEX-Showreel-60s.mp4
 *   FFMPEG_PATH=/path/to/ffmpeg node tools/render_video.js
 * ========================================================================== */

'use strict';
const path = require('path');
const fs = require('fs');
const { spawn } = require('child_process');
const { once } = require('events');

let createCanvas, GlobalFonts;
try {
  ({ createCanvas, GlobalFonts } = require('@napi-rs/canvas'));
} catch (e) {
  console.error('Install the offline renderer first:  npm install');
  process.exit(1);
}

const ROOT = path.join(__dirname, '..');
const args = process.argv.slice(2);
function arg(name, fallback) {
  const i = args.indexOf('--' + name);
  return i >= 0 ? args[i + 1] : fallback;
}

const output = path.resolve(arg('out', path.join(ROOT, 'ARTEX-Showreel-60s.mp4')));
const mute = args.includes('--mute');
const audioPath = path.join(ROOT, 'audio', 'score.wav');
let ffmpeg = arg('ffmpeg', process.env.FFMPEG_PATH || '');
if (!ffmpeg) {
  // Optional convenience for environments that installed this package locally.
  try { ffmpeg = require('@ffmpeg-installer/linux-x64').path; } catch (_) { ffmpeg = 'ffmpeg'; }
}

// Register static Geist faces at the requested weights.
const fontDir = path.join(ROOT, 'vendor');
for (const file of ['Geist-SemiBold.ttf', 'Geist-Bold.ttf', 'Geist-Black.ttf']) {
  const p = path.join(fontDir, file);
  if (fs.existsSync(p)) GlobalFonts.registerFromPath(p, 'Geist');
}

// The scene files use a very small DOM surface; keep the offline renderer in
// sync with tools/render_frames.js and the browser implementation.
function makeCanvasNode() { return createCanvas(300, 150); }
global.document = {
  createElement: (tag) => (tag === 'canvas' ? makeCanvasNode() : { style: {} }),
  fonts: { add: () => {}, ready: Promise.resolve() }
};
global.window = { addEventListener: () => {} };
global.performance = { now: () => Date.now() };

const files = [
  'js/hyperframes.js', 'js/geometry.js', 'js/ink.js', 'js/mascot.js',
  'js/scenes.js', 'js/render.js'
];
const src = files.map((p) => fs.readFileSync(path.join(ROOT, p), 'utf8')).join('\n');
(0, eval)(src + '\n;globalThis.__T = { HF, SCENES, RENDER };');
const { HF, SCENES, RENDER } = globalThis.__T;
const { W, H } = HF.STAGE;
const fps = HF.FPS;
const totalFrames = Math.round(HF.DURATION * fps);
const canvas = createCanvas(W, H);
const ctx = canvas.getContext('2d');
SCENES.measureHead(ctx);

if (!mute && !fs.existsSync(audioPath)) {
  console.error('Missing audio/score.wav. Generate it first:  python3 audio/score.py');
  process.exit(1);
}
fs.mkdirSync(path.dirname(output), { recursive: true });

const ffArgs = [
  '-hide_banner', '-loglevel', 'warning', '-y',
  '-f', 'image2pipe', '-framerate', String(fps), '-vcodec', 'png', '-i', 'pipe:0'
];
if (!mute) ffArgs.push('-i', audioPath);
ffArgs.push(
  '-map', '0:v:0',
  ...(mute ? [] : ['-map', '1:a:0']),
  '-frames:v', String(totalFrames),
  '-t', HF.DURATION.toFixed(3),
  '-c:v', 'libx264', '-preset', 'medium', '-crf', '22',
  '-pix_fmt', 'yuv420p', '-r', String(fps),
  '-metadata', 'title=ARTEX — 60 s Looping Motion Design Showreel',
  '-metadata', 'comment=60 seconds · 1920x1080 · 60 fps · seamless loop',
  '-movflags', '+faststart'
);
if (!mute) ffArgs.push('-c:a', 'aac', '-b:a', '192k', '-ar', '44100', '-ac', '2');
ffArgs.push(output);

function childExit(child) {
  return new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', (code, signal) => {
      if (code === 0) resolve();
      else reject(new Error(`ffmpeg exited with ${signal || `code ${code}`}`));
    });
  });
}

async function writeFrame(stream, buffer) {
  if (stream.destroyed || stream.writableEnded) throw new Error('ffmpeg input closed early');
  if (stream.write(buffer)) return;
  await once(stream, 'drain');
}

async function main() {
  const child = spawn(ffmpeg, ffArgs, { stdio: ['pipe', 'ignore', 'inherit'] });
  child.stdin.on('error', (err) => {
    if (err.code !== 'EPIPE') console.error('ffmpeg input error:', err.message);
  });
  const exit = childExit(child);

  console.log(`Rendering ${totalFrames} frames (${W}×${H} @ ${fps} fps) → ${output}`);
  try {
    for (let frame = 0; frame < totalFrames; frame++) {
      // The 60 s endpoint is exclusive at 60 fps. Use its exact frame as the
      // final sample so the encoded last frame equals the first frame; this
      // drops only the otherwise-unseen 59.983 s sample at the loop seam.
      const t = frame === totalFrames - 1 ? HF.DURATION : frame / fps;
      RENDER.frame(ctx, t);
      await writeFrame(child.stdin, canvas.toBuffer('image/png'));
      if (frame % fps === 0) {
        process.stdout.write(`\r${String(frame).padStart(4, '0')} / ${totalFrames} frames (${(frame / fps).toFixed(0)} s)`);
      }
    }
    child.stdin.end();
    await exit;
  } catch (err) {
    child.stdin.destroy();
    try { await exit; } catch (_) { /* preserve the first useful error */ }
    throw err;
  }
  console.log(`\rDone — ${output}`);
}

main().catch((err) => {
  console.error(`\nVideo export failed: ${err.message}`);
  process.exitCode = 1;
});
