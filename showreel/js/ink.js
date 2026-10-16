/* =============================================================================
 * ink.js — the three drawing styles of the reel. Same geometry, three inks:
 *   1. PAPER      graphite pencil on cream: sine-distorted double strokes,
 *                 diagonal hand hatching, multiply grain (masked from strokes).
 *   2. BLUEPRINT  clean white 3–4 px lines on blue, thin dashed hidden edges,
 *                 fine neat parallel hatching.
 *   3. HOLOGRAM   cyan beam with a solid white core at 30 % of line width on
 *                 black, fills built from 8 px pitch scan lines. NO blur, NO
 *                 glow — light is expressed purely by line density.
 * ========================================================================== */

const INK = (() => {
  'use strict';
  const { boilStep, vnoise, clamp, lerp, TAU, COLOR, STAGE } = HF;

  /* ---- stage skins -------------------------------------------------------- */

  const SKIN = {
    paper: {
      id: 'paper',
      bg: COLOR.paper,
      ink: COLOR.graphite,
      accent: COLOR.blue,
      highlight: COLOR.blue,
      gridBase: 'rgba(22,22,26,0.13)',
      gridMajor: 'rgba(22,22,26,0.26)',
      hud: COLOR.graphite
    },
    blueprint: {
      id: 'blueprint',
      bg: COLOR.blue,
      ink: COLOR.white,
      accent: COLOR.cyan,
      highlight: COLOR.cyan,
      gridBase: 'rgba(255,255,255,0.13)',
      gridMajor: 'rgba(255,255,255,0.26)',
      hud: COLOR.white
    },
    hologram: {
      id: 'hologram',
      bg: COLOR.black,
      ink: COLOR.cyan,
      accent: COLOR.white,
      highlight: COLOR.white,
      gridBase: 'rgba(69,240,221,0.13)',
      gridMajor: 'rgba(69,240,221,0.26)',
      hud: COLOR.cyan
    }
  };

  /* ---- path → canvas ----------------------------------------------------- */

  function pathTo(ctx, pts, close = false) {
    if (!pts.length) return;
    ctx.beginPath();
    ctx.moveTo(pts[0].x, pts[0].y);
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
    if (close) ctx.closePath();
  }

  /* ---- pencil wobble ------------------------------------------------------
   * Strokes are distorted by slow sine-wave noise; the phase updates 8 times
   * per second (stepped — the hand-drawn "boil"). Deterministic per t.
   * -------------------------------------------------------------------- */

  function wobble(pts, t, amp, freq, seed, phaseShift = 0) {
    const step = boilStep(t);
    const phase = step * 0.37 + phaseShift;
    const out = [];
    for (let i = 0; i < pts.length; i++) {
      const s = i * freq + phase;
      const n1 = Math.sin(s) * 0.62 + vnoise(i * 0.13 + step * 0.41, seed) * 0.38;
      const n2 = Math.sin(s * 2.13 + 1.7) * 0.35;
      // perpendicular offset from local tangent
      const p = pts[i];
      const a = pts[Math.max(0, i - 1)];
      const b = pts[Math.min(pts.length - 1, i + 1)];
      let dx = b.x - a.x, dy = b.y - a.y;
      const len = Math.hypot(dx, dy) || 1;
      dx /= len; dy /= len;
      const off = (n1 + n2) * amp;
      out.push({ x: p.x - dy * off, y: p.y + dx * off });
    }
    return out;
  }

  /**
   * Double stroke rendering: full stroke at 90 % of nominal width, thinner
   * secondary at 40 % width with stronger wobble, slightly offset.
   */
  function pencil(ctx, pts, t, opts = {}) {
    const {
      width = 4, color = SKIN.paper.ink, amp = 2.2, freq = 0.11,
      seed = 7, alpha = 1, close = false, ghost = true
    } = opts;
    ctx.save();
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.strokeStyle = color;
    ctx.globalAlpha = alpha;

    const main = wobble(pts, t, amp, freq, seed);
    pathTo(ctx, main, close);
    ctx.lineWidth = width * 0.9;
    ctx.stroke();

    if (ghost) {
      const sec = wobble(pts, t, amp * 1.85, freq * 1.31, seed + 11);
      // secondary passes slightly off the primary line
      const shifted = sec.map((p, i) => {
        const q = main[i];
        return { x: lerp(q.x, p.x, 1) + (p.x - q.x) * 0.35, y: lerp(q.y, p.y, 1) + (p.y - q.y) * 0.35 };
      });
      pathTo(ctx, shifted, close);
      ctx.lineWidth = width * 0.4;
      ctx.globalAlpha = alpha * 0.85;
      ctx.stroke();
    }
    ctx.restore();
  }

  /* ---- wire (blueprint) --------------------------------------------------- */

  function wire(ctx, pts, opts = {}) {
    const {
      width = 3.4, color = SKIN.blueprint.ink, alpha = 1,
      close = false, dashed = false, dash = [10, 8]
    } = opts;
    ctx.save();
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.strokeStyle = color;
    ctx.globalAlpha = alpha;
    ctx.lineWidth = width;
    if (dashed) ctx.setLineDash(dash);
    pathTo(ctx, pts, close);
    ctx.stroke();
    ctx.restore();
  }

  /* ---- beam (hologram) ----------------------------------------------------
   * Cyan body + solid white core occupying 30 % of line width. No blur/glow.
   * -------------------------------------------------------------------- */

  function beam(ctx, pts, opts = {}) {
    const {
      width = 5, color = SKIN.hologram.ink, core = COLOR.white, alpha = 1,
      close = false, coreAlpha = 1
    } = opts;
    ctx.save();
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.globalAlpha = alpha;
    ctx.strokeStyle = color;
    ctx.lineWidth = width;
    pathTo(ctx, pts, close);
    ctx.stroke();
    ctx.strokeStyle = core;
    ctx.lineWidth = width * 0.3;
    ctx.globalAlpha = alpha * coreAlpha;
    pathTo(ctx, pts, close);
    ctx.stroke();
    ctx.restore();
  }

  /** Style-dispatched polyline stroke. */
  function stroke(ctx, skin, pts, t, opts = {}) {
    if (skin.id === 'paper') return pencil(ctx, pts, t, opts);
    if (skin.id === 'blueprint') return wire(ctx, pts, opts);
    return beam(ctx, pts, opts);
  }

  /* ---- fills --------------------------------------------------------------
   * Paper: diagonal hand-drawn hatching (wobbly, variable spacing).
   * Blueprint: fine neat parallel hatching.
   * Hologram: horizontal scan lines, 8 px pitch, thickness from lighting.
   * Fills are clipped to the destination path.
   * -------------------------------------------------------------------- */

  function hatchFill(ctx, skin, pts, t, opts = {}) {
    if (!pts.length) return;
    const {
      angle = -45, pitch = 12, color = null, alpha = 0.9,
      seed = 3, lighting = null, close = true
    } = opts;
    const inkColor = color || (skin.id === 'paper' ? skin.accent : skin.id === 'blueprint' ? skin.ink : skin.ink);
    ctx.save();
    pathTo(ctx, pts, close);
    ctx.clip();

    // bounding box
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const p of pts) {
      x0 = Math.min(x0, p.x); y0 = Math.min(y0, p.y);
      x1 = Math.max(x1, p.x); y1 = Math.max(y1, p.y);
    }
    const diag = Math.hypot(x1 - x0, y1 - y0) + 40;
    const cx = (x0 + x1) / 2, cy = (y0 + y1) / 2;

    ctx.strokeStyle = inkColor;
    ctx.lineCap = 'round';

    if (skin.id === 'hologram') {
      // horizontal scan lines at 8 px pitch; thickness varies with lighting
      const step = 8;
      ctx.strokeStyle = inkColor;
      for (let y = Math.floor((y0 - 20) / step) * step; y <= y1 + 20; y += step) {
        let th = 1.2;
        if (lighting) th = lighting((y - y0) / Math.max(1, y1 - y0));
        th = clamp(th, 0.6, 3.2);
        ctx.lineWidth = th;
        ctx.beginPath();
        ctx.moveTo(x0 - 30, y);
        ctx.lineTo(x1 + 30, y);
        ctx.stroke();
      }
    } else if (skin.id === 'blueprint') {
      // fine neat parallel hatching
      ctx.lineWidth = 1.4;
      ctx.globalAlpha = alpha * 0.75;
      const a = (angle * Math.PI) / 180;
      const dx = Math.cos(a), dy = Math.sin(a);
      const nx = -dy, ny = dx;
      for (let o = -diag / 2; o <= diag / 2; o += pitch) {
        const bx = cx + nx * o, by = cy + ny * o;
        ctx.beginPath();
        ctx.moveTo(bx - dx * diag, by - dy * diag);
        ctx.lineTo(bx + dx * diag, by + dy * diag);
        ctx.stroke();
      }
    } else {
      // hand-drawn diagonal hatching — wobble & vary spacing
      const step = boilStep(t);
      ctx.globalAlpha = alpha;
      const a = (angle * Math.PI) / 180;
      const dx = Math.cos(a), dy = Math.sin(a);
      const nx = -dy, ny = dx;
      let o = -diag / 2;
      let k = 0;
      while (o <= diag / 2) {
        const jitter = vnoise(k * 0.9 + step * 0.13, seed) * pitch * 0.45;
        const bx = cx + nx * (o + jitter), by = cy + ny * (o + jitter);
        const wob = 1.6 + vnoise(k * 1.7, seed + 5) * 1.2;
        ctx.lineWidth = wob;
        const segs = 6;
        ctx.beginPath();
        for (let s = 0; s <= segs; s++) {
          const p = s / segs;
          const sx = bx + dx * diag * (p - 0.5) * 2;
          const sy = by + dy * diag * (p - 0.5) * 2;
          const n = vnoise(k * 3.1 + s * 0.7 + step * 0.21, seed + 17) * 2.2;
          if (s === 0) ctx.moveTo(sx - dy * n, sy + dx * n);
          else ctx.lineTo(sx - dy * n, sy + dx * n);
        }
        ctx.stroke();
        o += pitch + vnoise(k * 2.3, seed + 9) * pitch * 0.35;
        k++;
      }
    }
    ctx.restore();
  }

  /** Cross-hatching: second pass at the mirrored angle (paper style). */
  function crossHatch(ctx, skin, pts, t, opts = {}) {
    hatchFill(ctx, skin, pts, t, opts);
    hatchFill(ctx, skin, pts, t, {
      ...opts,
      angle: -(opts.angle ?? -45),
      alpha: (opts.alpha ?? 0.9) * 0.8,
      seed: (opts.seed ?? 3) + 31
    });
  }

  /* ---- grid ---------------------------------------------------------------
   * 60 px grid, base opacity 13 %, every 5th line 26 %. Identical across
   * all three stages — only the ink changes.
   * -------------------------------------------------------------------- */

  function grid(ctx, skin) {
    const { W, H, GRID, GRID_MAJOR } = STAGE;
    ctx.save();
    ctx.lineWidth = 1;
    for (let x = 0; x <= W; x += GRID) {
      const major = (x / GRID) % GRID_MAJOR === 0;
      ctx.strokeStyle = major ? skin.gridMajor : skin.gridBase;
      ctx.beginPath();
      ctx.moveTo(x + 0.5, 0);
      ctx.lineTo(x + 0.5, H);
      ctx.stroke();
    }
    for (let y = 0; y <= H; y += GRID) {
      const major = (y / GRID) % GRID_MAJOR === 0;
      ctx.strokeStyle = major ? skin.gridMajor : skin.gridBase;
      ctx.beginPath();
      ctx.moveTo(0, y + 0.5);
      ctx.lineTo(W, y + 0.5);
      ctx.stroke();
    }
    ctx.restore();
  }

  /* ---- paper grain --------------------------------------------------------
   * Subtle hand-drawn grain, multiply blend, laid down BEFORE the strokes so
   * it is masked out from pencil marks (strokes stay clean).
   * -------------------------------------------------------------------- */

  let grainTile = null;
  function getGrainTile() {
    if (grainTile) return grainTile;
    const size = 256;
    const c = document.createElement('canvas');
    c.width = size; c.height = size;
    const g = c.getContext('2d');
    const img = g.createImageData(size, size);
    const rng = HF.mulberry32(1337);
    for (let i = 0; i < size * size; i++) {
      const v = 200 + rng() * 55;
      // sparse darker flecks — hand-made paper tooth
      const fleck = rng() > 0.994 ? rng() * 90 : 0;
      const px = Math.max(0, Math.min(255, v - fleck));
      img.data[i * 4] = px;
      img.data[i * 4 + 1] = px - 2;
      img.data[i * 4 + 2] = px - 6;
      img.data[i * 4 + 3] = 255;
    }
    g.putImageData(img, 0, 0);
    grainTile = c;
    return c;
  }

  function grain(ctx, t) {
    const tile = getGrainTile();
    ctx.save();
    ctx.globalCompositeOperation = 'multiply';
    ctx.globalAlpha = 0.16;
    const step = boilStep(t);
    // slow drift, periodic over the loop
    const ox = (step * 13) % 256;
    const oy = (step * 7) % 256;
    for (let y = -256; y < STAGE.H + 256; y += 256) {
      for (let x = -256; x < STAGE.W + 256; x += 256) {
        ctx.drawImage(tile, x + ox, y + oy);
      }
    }
    ctx.restore();
  }

  /* ---- hatch-clipped type -------------------------------------------------
   * Large headline letters: hand hatching clipped to the letter shapes,
   * doubled outline (inner + offset outer stroke).
   * -------------------------------------------------------------------- */

  // cached offscreen pair for hatched type (headline only — avoid per-frame alloc)
  let textOff = null, textMask = null;
  function textCanvases(w, h) {
    if (!textOff) {
      textOff = document.createElement('canvas');
      textMask = document.createElement('canvas');
    }
    if (textOff.width !== w || textOff.height !== h) {
      textOff.width = w; textOff.height = h;
      textMask.width = w; textMask.height = h;
    }
    return { off: textOff, mask: textMask };
  }

  function hatchedText(ctx, skin, text, x, y, size, weight, t, opts = {}) {
    const {
      trackingEm = 0.02, align = 'center', hatchAngle = -48,
      hatchPitch = 14, seed = 21, outlineW = 0, flatScaleY = 1
    } = opts;
    ctx.save();
    ctx.translate(x, y);
    if (flatScaleY !== 1) ctx.scale(1, flatScaleY);
    ctx.font = `${weight} ${size}px Geist, sans-serif`;
    ctx.textBaseline = 'alphabetic';

    // measure total width for alignment
    const tracking = trackingEm * size;
    let total = 0;
    for (const ch of text) total += ctx.measureText(ch).width + tracking;
    total -= tracking;
    let cx = align === 'center' ? -total / 2 : align === 'right' ? -total : 0;

    // hatch strokes drawn first, then masked to the letter shapes
    const pad = size * 0.5;
    const { off, mask } = textCanvases(
      Math.ceil(total + pad * 2),
      Math.ceil(size * 1.4)
    );
    const mg = mask.getContext('2d');
    mg.clearRect(0, 0, mask.width, mask.height);
    mg.font = ctx.font;
    mg.textBaseline = 'alphabetic';
    mg.fillStyle = '#fff';
    let ox = pad;
    for (const ch of text) {
      mg.fillText(ch, ox, size * 1.05);
      ox += mg.measureText(ch).width + tracking;
    }

    const og = off.getContext('2d');
    og.clearRect(0, 0, off.width, off.height);
    og.globalCompositeOperation = 'source-over';
    const step = boilStep(t);
    const a = (hatchAngle * Math.PI) / 180;
    const dx = Math.cos(a), dy = Math.sin(a);
    const diag = Math.hypot(off.width, off.height);
    og.strokeStyle = skin.accent;
    og.lineCap = 'round';
    for (let o = -diag; o <= diag; o += hatchPitch) {
      const wob = vnoise(o * 0.11 + step * 0.17, seed) * 3.2;
      og.lineWidth = 3 + vnoise(o * 0.19, seed + 4) * 2.4;
      og.beginPath();
      const mx = off.width / 2 - dy * o + wob;
      const my = size * 1.05 + dx * o;
      og.moveTo(mx - dx * diag, my - dy * diag);
      og.lineTo(mx + dx * diag, my + dy * diag);
      og.stroke();
    }
    // keep the hatch only where the glyphs are (single hard mask, no fades)
    og.globalCompositeOperation = 'destination-in';
    og.drawImage(mask, 0, 0);
    og.globalCompositeOperation = 'source-over';
    ctx.drawImage(off, cx - pad, -size * 1.05);

    // doubled outline
    if (outlineW > 0) {
      ctx.lineJoin = 'round';
      let lx = cx;
      for (const ch of text) {
        ctx.strokeStyle = skin.ink;
        ctx.lineWidth = outlineW;
        ctx.strokeText(ch, lx, 0);
        ctx.lineWidth = outlineW * 0.45;
        ctx.strokeText(ch, lx + outlineW * 0.16, -outlineW * 0.16);
        lx += ctx.measureText(ch).width + tracking;
      }
    }
    ctx.restore();
    return total;
  }

  return {
    SKIN, pathTo, wobble,
    pencil, wire, beam, stroke,
    hatchFill, crossHatch, grid, grain, hatchedText
  };
})();
