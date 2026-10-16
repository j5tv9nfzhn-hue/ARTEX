/* =============================================================================
 * scenes.js — 30 bars × 2 s choreography. 120 BPM · 120 beats · 0 cuts.
 * One analytical line evolves three times (pencil → wire → light beam) and
 * never leaves the screen. Every shape, every word and every colour morphs
 * continuously into the next; first frame ≡ final frame.
 *
 * Stage plan:
 *   0.0 – 10.0  PAPER       (bars 1–5)   flood-out at 9.6  (hexagon, hard edge)
 *  10.0 – 24.0  BLUEPRINT   (bars 6–12)  scan-out at 23.5  (horizontal scan line)
 *  24.0 – 30.0  HOLOGRAM    (bars 13–15) scan-out at 29.5
 *  30.0 – 40.0  PAPER       (bars 16–20) flood-out at 39.5 (rectangle, hard edge)
 *  40.0 – 48.0  BLUEPRINT   (bars 21–24) scan-out at 47.5
 *  48.0 – 56.4  HOLOGRAM    (bars 25–28) scan-out at 55.8
 *  56.4 – 60.0  PAPER       (bars 29–30)  → lands exactly on frame 0
 * ========================================================================== */

const SCENES = (() => {
  'use strict';
  const { lerp, clamp, invLerp, rad, TAU, expOut, expIn, spring, linear } = HF;
  const { W, H, MX, MY } = HF.STAGE;
  const C = HF.COLOR;

  /* ---- shared layout ------------------------------------------------------ */
  const CX = 960;
  const HEAD_BASE = 740;               // headline baseline
  const SUB_TOP = 'AN OPEN-SOURCE AGENT';
  const SUB_BOT = 'AUTOMATED AUTHORIZED SECURITY TESTING';
  const RAIL0 = { y: 299, x1: 150, x2: 1770 };   // frame-0 pencil rail (1620 px)
  const MASCOT0_X = 1560;
  const LABEL = 'CREATIVE DIRECTOR';
  const CHART = [
    42, 30, 36, 44, 50, 46, 52, 62, 50, 56, 62, 72, 60, 66, 58, 76, 56, 62,
    68, 74, 70, 78, 86, 94, 100, 93, 97, 90, 80, 50
  ];
  const STAGES_5 = ['COLLECT', 'ANALYZE', 'PLAN', 'EXECUTE', 'AUDIT'];

  // headline metrics (filled after font load)
  let HEAD = { size: 430, w: 1500, top: 348, cap: 320 };

  function measureHead(ctx) {
    const probe = 200;
    const track = 0.012;
    const w100 = GEO.measureText(ctx, 'ARTEX', probe, 900, track);
    HEAD.size = probe * (1500 / w100);
    HEAD.w = GEO.measureText(ctx, 'ARTEX', HEAD.size, 900, track);
    HEAD.cap = HEAD.size * 0.725;
    HEAD.top = HEAD_BASE - HEAD.cap;
    // frame-0 pencil rail sits 96 px above the headline
    RAIL0.y = HEAD.top - 96;
  }

  /* ---- text helpers ------------------------------------------------------- */

  function trackedText(ctx, text, x, y, size, weight, trackingEm, color, align = 'left', alpha = 1) {
    ctx.save();
    ctx.globalAlpha = alpha;
    const lay = GEO.layoutText(ctx, text, x, y, size, weight, trackingEm, align);
    ctx.fillStyle = color;
    for (const c of lay.chars) {
      ctx.font = `${weight} ${size}px Geist, sans-serif`;
      ctx.fillText(c.ch, c.x, c.y);
    }
    ctx.restore();
    return lay.width;
  }

  /** Digital counter: value itself rolls upward; digits sit clean on the baseline. */
  function rollingNum(ctx, value, x, y, size, weight, trackingEm, color, t, align = 'left', digits = 3) {
    const s = String(Math.round(value)).padStart(digits, '0');
    const lay = GEO.layoutText(ctx, s, x, y, size, weight, trackingEm, align);
    ctx.save();
    ctx.fillStyle = color;
    ctx.font = `${weight} ${size}px Geist, sans-serif`;
    for (const c of lay.chars) ctx.fillText(c.ch, c.x, c.y);
    ctx.restore();
    return lay.width;
  }

  /* ---- HUD ---------------------------------------------------------------- */

  function hud(ctx, skin, t) {
    const barIdx = HF.barIndex(t);
    const barName = HF.BAR_NAMES[barIdx];
    const fig = `FIG. ${HF.pad(barIdx + 1, 2)} — ${barName}`;
    const beatIdx = Math.floor(t / HF.BEAT + 1e-9) % 120;
    const beatTxt = `BEAT ${HF.pad(beatIdx + 1, 3)} / 120`;

    // top-left FIG label — permanent; each bar stamps an accent rule past it
    trackedText(ctx, fig, MX, MY + 24, 26, 700, 0.18, skin.hud, 'left');
    const barT = t - barIdx * HF.BAR;
    const sweep = expOut(10, 0.3)(barT);
    if (sweep < 0.995) {
      const figW = GEO.measureText(ctx, fig, 26, 700, 0.18);
      ctx.save();
      ctx.fillStyle = skin.id === 'paper' ? C.blue : skin.id === 'blueprint' ? C.cyan : C.white;
      ctx.fillRect(MX, MY + 36, (figW + 8) * sweep, 3);
      ctx.restore();
    }

    // bottom-right BEAT counter with a beat pip
    const beatT = t - beatIdx * HF.BEAT;
    const grow = spring(30, 0.18)(beatT);
    const pipR = 5 + 3 * (1 - grow);
    const bw = trackedText(ctx, beatTxt, W - MX, H - MY - 6, 26, 700, 0.18, skin.hud, 'right');
    ctx.save();
    ctx.beginPath();
    ctx.arc(W - MX - bw - 22, H - MY - 16, pipR, 0, TAU);
    ctx.fillStyle = skin.id === 'paper' ? C.blue : skin.id === 'blueprint' ? C.cyan : C.white;
    ctx.fill();
    ctx.restore();
  }

  /* ---- stage bookkeeping -------------------------------------------------- */

  function skinAt(t) {
    if (t < 10) return 'paper';
    if (t < 24) return 'blueprint';
    if (t < 30) return 'hologram';
    if (t < 40) return 'paper';
    if (t < 48) return 'blueprint';
    if (t < 56.4) return 'hologram';
    return 'paper';
  }

  function transitionAt(t) {
    if (t >= 9.6 && t < 10.0) return { type: 'flood', shape: 'hex', from: 'paper', to: 'blueprint', t0: 9.6, D: 0.4 };
    if (t >= 23.5 && t < 24.0) return { type: 'scan', dir: 'down', from: 'blueprint', to: 'hologram', t0: 23.5, D: 0.5 };
    if (t >= 29.5 && t < 30.0) return { type: 'scan', dir: 'up', from: 'hologram', to: 'paper', t0: 29.5, D: 0.5 };
    if (t >= 39.5 && t < 40.0) return { type: 'flood', shape: 'rect', from: 'paper', to: 'blueprint', t0: 39.5, D: 0.5 };
    if (t >= 47.5 && t < 48.0) return { type: 'scan', dir: 'down', from: 'blueprint', to: 'hologram', t0: 47.5, D: 0.5 };
    if (t >= 55.8 && t < 56.4) return { type: 'scan', dir: 'down', from: 'hologram', to: 'paper', t0: 55.8, D: 0.6 };
    return null;
  }

  /* =========================================================================
   * BAR 1 (0–2) PAPER · HOOK — hero composition (≡ frame 0 at rest).
   * ====================================================================== */

  function heroParams(t) {
    // REST state (frame 0). Bars 29–30 animate INTO this; bar 1 animates OUT.
    const P = {
      railY: RAIL0.y,
      railX1: RAIL0.x1,
      railX2: RAIL0.x2,
      railGap: 0,     // half-distance between split rails
      subTop: 1,      // 1 = fully drawn
      subBot: 1,
      flat: 0,        // headline squash factor (0 = normal)
      headAlpha: 1,
      headHatch: 1,
      headOutline: 1,
      mascotX: MASCOT0_X,
      mascotRun: 0,
      mascotLean: 0,
      mascotAlpha: 1,
      labelWipe: 1
    };
    if (t < 2.0) {
      // 0.00–0.30 subtext un-draws (letters retract to the baseline)
      P.subTop = 1 - expIn(6, 0.30)(t);
      P.subBot = 1 - expIn(6, 0.30)(t - 0.06);
      // 0.00–0.55 mascot runs right and exits, accelerating
      P.mascotRun = 1;
      P.mascotX = MASCOT0_X + expIn(2.6, 0.55)(t) * 620;
      P.mascotLean = 7;
      P.mascotAlpha = 1 - expIn(9, 0.12)(t - 0.42);
      P.labelWipe = 1 - expIn(8, 0.22)(t);
      // 0.36–0.50 rail drops to 26 px below baseline, pressing letters flat
      const drop = spring(40, 0.14)(t - 0.36);
      P.railY = lerp(RAIL0.y, HEAD_BASE + 26, drop);
      P.flat = spring(38, 0.14)(t - 0.36);
      // 0.95–1.30 rail rises to middle, shortens, splits into two rails 140 px apart
      const rise = spring(22, 0.35)(t - 0.95);
      if (rise > 0) {
        P.railY = lerp(HEAD_BASE + 26, 540, rise);
        P.railX1 = lerp(RAIL0.x1, 300, rise);
        P.railX2 = lerp(RAIL0.x2, 1620, rise);
        P.railGap = 70 * rise;
      }
      // 1.45–1.75 rails spread apart to 300 px
      const spread = spring(22, 0.3)(t - 1.45);
      if (spread > 0) P.railGap = lerp(70, 150, spread);
    }
    return P;
  }

  function drawHero(ctx, skin, t) {
    const P = heroParams(t);
    const ink = skin.id === 'paper' ? C.graphite : skin.id === 'blueprint' ? C.white : C.cyan;

    /* small subtexts */
    if (P.subTop > 0.001) drawWipedText(ctx, SUB_TOP, CX, HEAD.top - 42, 26, 700, 0.22, skin.hud, P.subTop);
    if (P.subBot > 0.001) drawWipedText(ctx, SUB_BOT, CX, HEAD_BASE + 72, 26, 700, 0.22, skin.hud, P.subBot);

    /* headline — hatching clipped to letter shapes, doubled outline */
    if (P.flat < 0.999) {
      const sy = lerp(1, 0.1, P.flat);
      INK.hatchedText(ctx, skin, 'ARTEX', CX, HEAD_BASE, HEAD.size, 900, t, {
        trackingEm: 0.012, align: 'center', hatchAngle: -48, hatchPitch: 15,
        outlineW: 7, flatScaleY: sy
      });
    } else {
      // pressed completely flat: the letters become a solid bar that gets
      // absorbed into the rail as the rail rises (0.95–1.30)
      const absorb = spring(22, 0.35)(t - 0.95);
      const barY = lerp(HEAD_BASE, 540, absorb);
      const barH = lerp(7, 1.5, absorb);
      const barW = lerp(1500, 1320, absorb);
      if (absorb < 0.995) {
        ctx.save();
        ctx.fillStyle = ink;
        ctx.fillRect(CX - barW / 2, barY - barH + 2, barW, barH);
        ctx.restore();
      }
    }

    /* the rail (the analytical line) */
    const railPts = GEO.line(P.railX1, P.railY - P.railGap, P.railX2, P.railY - P.railGap, 80);
    INK.stroke(ctx, skin, railPts, t, { width: 5, amp: 1.6, freq: 0.06, seed: 5 });

    /* split second rail */
    if (P.railGap > 1) {
      const rail2 = GEO.line(P.railX1, P.railY + P.railGap, P.railX2, P.railY + P.railGap, 80);
      INK.stroke(ctx, skin, rail2, t, { width: 5, amp: 1.6, freq: 0.06, seed: 9 });
    }

    /* mascot + CREATIVE DIRECTOR label */
    if (P.mascotAlpha > 0.01) {
      MASCOT.draw(ctx, P.mascotX, P.railY - P.railGap - 2, t, {
        skin, scale: 1, running: P.mascotRun, lean: P.mascotLean, alpha: P.mascotAlpha
      });
    }
    if (P.labelWipe > 0.001) {
      drawWipedText(ctx, LABEL, P.mascotX - 92, P.railY - P.railGap - 64, 24, 700, 0.2, skin.accent, P.labelWipe, 'right');
    }
  }

  /** Text revealed/hidden by a hard horizontal wipe (no fades). */
  function drawWipedText(ctx, text, x, y, size, weight, trackingEm, color, p, align = 'center') {
    const lay = GEO.layoutText(ctx, text, x, y, size, weight, trackingEm, align);
    const total = lay.width;
    ctx.save();
    ctx.beginPath();
    if (align === 'center') ctx.rect(x - total / 2 - 4, y - size * 1.2, total * clamp(p, 0, 1) + 8, size * 1.6);
    else if (align === 'right') ctx.rect(x - total - 4 + total * (1 - clamp(p, 0, 1)), y - size * 1.2, total * clamp(p, 0, 1) + 8, size * 1.6);
    else ctx.rect(x - 4, y - size * 1.2, total * clamp(p, 0, 1) + 8, size * 1.6);
    ctx.clip();
    ctx.fillStyle = color;
    ctx.font = `${weight} ${size}px Geist, sans-serif`;
    for (const c of lay.chars) ctx.fillText(c.ch, c.x, c.y);
    ctx.restore();
    return total;
  }

  /* =========================================================================
   * BAR 1b (1.0–2.0) ruler ticks + dimension line
   * ====================================================================== */

  function drawTicksAndDim(ctx, skin, t) {
    if (t < 1.0 || t >= 2.6) return;
    const P = heroParams(t);
    const topRailY = P.railY - P.railGap;
    const sweep = expOut(8, 0.45)(t - 1.0);
    // 41 blue ruler ticks along the top rail
    const n = 41;
    for (let i = 0; i < n; i++) {
      const p = i / (n - 1);
      const x = lerp(P.railX1 + 20, P.railX2 - 20, p);
      if (p > sweep) continue;
      const major = i % 5 === 0;
      const h = major ? 18 : 10;
      const app = spring(26, 0.18)(t - 1.0 - p * 0.42);
      if (app <= 0) continue;
      const yOff = (1 - app) * -14;
      INK.stroke(ctx, skin, GEO.line(x, topRailY - h * app + yOff, x, topRailY - 1 + yOff, 4), t,
        { width: major ? 2.6 : 1.8, color: skin.accent, amp: 0.7, seed: 3 + i, ghost: false });
    }

    // blue dimension line on the right with live numeric readout
    const dimP = spring(20, 0.4)(t - 1.45);
    if (dimP > 0.01) {
      const x = 1700;
      const y1 = P.railY - P.railGap;
      const y2 = P.railY + P.railGap;
      const gap = Math.abs(y2 - y1);
      INK.stroke(ctx, skin, GEO.line(x, y1, x, y2, 12), t,
        { width: 2.2, color: skin.accent, amp: 0.6, seed: 44, ghost: false });
      for (const yy of [y1, y2]) {
        INK.stroke(ctx, skin, GEO.line(x - 10, yy, x + 10, yy, 4), t,
          { width: 2.2, color: skin.accent, amp: 0.5, seed: 45, ghost: false });
      }
      const val = lerp(0, gap, dimP);
      rollingNum(ctx, val, x + 18, (y1 + y2) / 2 + 8, 24, 700, 0.12, skin.accent, t, 'left', 3);
    }
  }

  /* =========================================================================
   * BAR 2 (2–4) PAPER · SCAN — words clipped between rails.
   * ====================================================================== */

  function railsAt(t) {
    // returns {top, bot} y of the two rails during bar 2
    if (t < 2.0) return { top: 390, bot: 690 };
    if (t < 2.5) {          // hug "ASSET DISCOVERY"
      const p = spring(22, 0.3)(t - 1.95);
      return { top: lerp(390, 470, p), bot: lerp(690, 585, p) };
    }
    if (t < 3.0) {          // hug "SCAN"
      const p = spring(22, 0.3)(t - 2.45);
      return { top: lerp(470, 465, p), bot: lerp(585, 615, p) };
    }
    if (t < 3.45) {         // clamp shut then reopen
      const shut = spring(30, 0.18)(t - 2.95);
      const open = spring(22, 0.3)(t - 3.15);
      const top = lerp(465, 540, shut);
      const bot = lerp(615, 540, shut);
      return { top: lerp(top, 400, open), bot: lerp(bot, 680, open) };
    }
    if (t < 4.0) {          // PORT MAP drop
      const p = spring(22, 0.3)(t - 3.4);
      return { top: lerp(400, 420, p), bot: lerp(680, 660, p) };
    }
    const close = spring(28, 0.18)(t - 3.95);   // compress into single line
    return { top: lerp(420, 540, close), bot: lerp(660, 540, close) };
  }

  function bar2(ctx, skin, t) {
    const R = railsAt(t);
    const rx1 = lerp(300, 240, invLerp(2, 4, t));
    const rx2 = lerp(1620, 1680, invLerp(2, 4, t));

    /* words */
    if (t < 2.62) {
      // "ASSET DISCOVERY" rises letter by letter through the baseline, then lifts out
      const txt = 'ASSET DISCOVERY';
      const size = 120, track = 0.06;
      const lay = GEO.layoutText(ctx, txt, CX, R.bot - 14, size, 900, track, 'center');
      for (let i = 0; i < lay.chars.length; i++) {
        const c = lay.chars[i];
        const t0 = 1.95 + i * 0.028;
        const rise = spring(30, 0.28)(t - t0);
        const lift = expIn(5, 0.3)(t - 2.28);          // lifts up out of frame
        const y = c.y + (1 - rise) * 150 - lift * 900;
        if (rise <= 0.001) continue;
        ctx.save();
        ctx.font = `900 ${size}px Geist, sans-serif`;
        ctx.fillStyle = skin.ink;
        ctx.fillText(c.ch, c.x, y);
        ctx.restore();
      }
    }
    if (t >= 2.45 && t < 2.98) {
      // "SCAN" whips in from the left over 0.2 s
      const p = spring(26, 0.2)(t - 2.45);
      const x = lerp(-700, CX, p);
      const size = 170;
      ctx.save();
      ctx.font = `900 ${size}px Geist, sans-serif`;
      ctx.fillStyle = skin.ink;
      const w = GEO.measureText(ctx, 'SCAN', size, 900, 0.04);
      ctx.fillText('SCAN', x - w / 2, R.bot - 18);
      ctx.restore();
    }
    if (t >= 2.95 && t < 3.48) {
      // "ENDPOINT" stretched by the rails, letters fly sideways outward
      const txt = 'ENDPOINT';
      const size = 130, track = 0.05;
      const stretch = t < 3.15 ? 1 : lerp(1, 1.9, spring(24, 0.35)(t - 3.15));
      const fly = expIn(4.5, 0.3)(t - 3.22);
      const lay = GEO.layoutText(ctx, txt, CX, 540 + size * 0.34, size, 900, track, 'center');
      for (let i = 0; i < lay.chars.length; i++) {
        const c = lay.chars[i];
        const dir = c.x + c.w / 2 < CX ? -1 : 1;
        ctx.save();
        ctx.translate(c.x, 540 + size * 0.34 * stretch);
        ctx.scale(1, stretch);
        ctx.font = `900 ${size}px Geist, sans-serif`;
        ctx.fillStyle = skin.ink;
        ctx.fillText(c.ch, dir * fly * 1500 * (1 + Math.abs(c.x - CX) / 700), 0);
        ctx.restore();
      }
    }
    if (t >= 3.4) {
      // "PORT MAP" in blue pencil, letters drop one by one from the top every 0.05 s
      const txt = 'PORT MAP';
      const size = 130, track = 0.16;
      const lay = GEO.layoutText(ctx, txt, CX, R.bot - 22, size, 800, track, 'center');
      const compress = expIn(8, 0.12)(t - 3.95);
      for (let i = 0; i < lay.chars.length; i++) {
        const c = lay.chars[i];
        const drop = spring(34, 0.22)(t - (3.4 + i * 0.05));
        if (drop <= 0.001) continue;
        const y = lerp(R.top - 260, c.y, drop);
        ctx.save();
        ctx.translate(c.x, y);
        ctx.scale(1, 1 - compress * 0.9);
        ctx.font = `800 ${size}px Geist, sans-serif`;
        ctx.fillStyle = skin.accent;
        ctx.fillText(c.ch, 0, 0);
        ctx.restore();
      }
    }

    /* rails + clipped edges */
    const railW = lerp(5, 4, invLerp(2, 4, t));
    INK.stroke(ctx, skin, GEO.line(rx1, R.top, rx2, R.top, 64), t, { width: railW, amp: 1.4, seed: 12 });
    INK.stroke(ctx, skin, GEO.line(rx1, R.bot, rx2, R.bot, 64), t, { width: railW, amp: 1.4, seed: 13 });

    /* 4.0 : rails close and compress text into a single continuous 1476 px line */
    if (t >= 3.95) {
      const p = spring(26, 0.16)(t - 3.95);
      const lx1 = lerp(rx1, 222, p), lx2 = lerp(rx2, 1698, p);
      INK.stroke(ctx, skin, GEO.line(lx1, 540, lx2, 540, 64), t, { width: 5, amp: 1.3, seed: 14 });
    }
  }

  /* =========================================================================
   * BAR 3 (4–6) PAPER · CURVE — pen-tool handles, leaf fan, sine waves.
   * ====================================================================== */

  function bar3(ctx, skin, t) {
    const X1 = 222, X2 = 1698, Y = 540;
    const bend = spring(22, 0.45)(t - 4.05);          // line bends into S
    const bow = lerp(0, 118, bend);

    // leaf fan: 8 blue copies 34 px apart, connected at both ends
    const fan = spring(20, 0.4)(t - 4.45);
    const waveTake = spring(22, 0.45)(t - 4.95);      // ribbon → sine waves
    const collapse = spring(24, 0.42)(t - 5.45);      // waves → flat line

    const copies = 8;
    const copyVis = fan * (1 - collapse);   // accent copies exist only while fanned
    for (let k = 0; k < copies; k++) {
      const isCenter = k === 0;
      if (!isCenter && copyVis < 0.03) continue;
      const off = (k - (copies - 1) / 2) * 34 * fan * (1 - collapse) * (isCenter ? 0 : 1);
      const bowK = bow * (1 + (isCenter ? 0 : (k - (copies - 1) / 2) * 0.06));
      let pts;
      if (waveTake > 0.001) {
        const amp = lerp(0, 92, waveTake) * (1 - collapse);
        const phase = k * 0.55 + t * 2.2;
        pts = GEO.wave(X1, X2, Y + off * 0.4, amp, 2.75, phase, 120);
        // travelling waves slide along x
        pts = pts.map((p, i) => ({ x: p.x, y: p.y + Math.sin(i * 0.09 - t * 3.1 + k * 0.8) * amp * 0.16 }));
      } else {
        pts = GEO.sCurve(X1, Y + off, X2, Y + off, bowK * (isCenter ? 1 : 0.86), 110);
      }
      const col = isCenter ? skin.ink : skin.accent;
      INK.stroke(ctx, skin, pts, t, {
        width: isCenter ? 5.2 : 3.4 * copyVis, color: col,
        amp: 1.5, freq: 0.07, seed: 20 + k, ghost: isCenter
      });
    }

    // connected ends of the fan (leaf outline)
    if (fan > 0.05 && collapse < 0.95 && waveTake < 0.5) {
      for (const ex of [X1, X2]) {
        const h = 34 * ((copies - 1) / 2) * fan * (1 - collapse);
        INK.stroke(ctx, skin, GEO.line(ex, Y - h, ex, Y + h, 12), t,
          { width: 3, color: skin.accent, amp: 1.1, seed: 33, ghost: false });
      }
    }

    /* pen-tool bezier handles */
    const handleP = spring(24, 0.3)(t - 4.05) * (1 - expIn(7, 0.35)(t - 4.9));
    if (handleP > 0.02) {
      const angle = rad(58);
      const mirror = t >= 4.5 ? spring(22, 0.35)(t - 4.5) : 0;
      const hLen = 430 * handleP;
      for (const side of [-1, 1]) {
        const ax = side < 0 ? X1 : X2;
        const ay = Y + (side < 0 ? -bow : bow) * 0.55 * (1 - mirror * 0.2);
        const a = side < 0 ? angle : angle + Math.PI;
        const m = side < 0 ? mirror * rad(64) : -mirror * rad(64);
        const hx = ax + Math.cos(a + m) * hLen * side * -1;
        const hy = ay - Math.sin(a + m) * hLen;
        // handle rod
        INK.stroke(ctx, skin, GEO.line(ax, ay, hx, hy, 16), t,
          { width: 2.4, color: skin.accent, amp: 0.8, seed: 51, ghost: false });
        // square anchor
        const s = 9;
        INK.stroke(ctx, skin, GEO.roundRect(ax - s, ay - s, s * 2, s * 2, 0, 2), t,
          { width: 2.4, color: skin.accent, amp: 0.6, seed: 52, ghost: false, close: true });
        // circle handle tip
        INK.stroke(ctx, skin, GEO.circle(hx, hy, 11, 24), t,
          { width: 2.4, color: skin.accent, amp: 0.6, seed: 53, ghost: false, close: true });
      }
      // label EASE
      const lp = expOut(10, 0.25)(t - 4.15);
      if (lp > 0.02) {
        drawWipedText(ctx, 'EASE', CX, Y - bow - 150 * handleP - 30, 24, 700, 0.26, skin.accent, lp);
      }
    }
  }

  /* =========================================================================
   * BAR 4 (6–8) PAPER · MORPH — circle → triangle → square → hexagon.
   * ====================================================================== */

  function shapeTarget(t) {
    // equal-ish perimeter shapes around (CX, 540)
    const line = GEO.line(222, 540, 1698, 540, 97);
    const circ = GEO.circle(CX, 540, 235, 97);
    const tri = GEO.polygon(CX, 540, 284, 3, false);
    const triR = GEO.resample(tri, 97);
    const sq = GEO.resample(GEO.polygon(CX, 540, 261, 4, false), 97);
    const hex = GEO.resample(GEO.polygon(CX, 540, 270, 6, true), 97);
    if (t < 6.05) return line;
    if (t < 6.5) {
      const p = spring(20, 0.42)(t - 6.05);
      return GEO.morph(line, circ, p);
    }
    if (t < 7.0) {
      // morph to triangle while rotating a full 120° (silhouette lands clean)
      const p = spring(20, 0.42)(t - 6.45);
      const m = GEO.morph(circ, triR, p);
      return GEO.rotate(m, CX, 540, rad(120) * p);
    }
    if (t < 7.5) {
      const p = spring(20, 0.42)(t - 6.95);
      return GEO.morph(triR, sq, p);
    }
    const p = spring(20, 0.42)(t - 7.45);
    return GEO.morph(sq, hex, p);
  }

  function bar4(ctx, skin, t) {
    const pts = shapeTarget(t);
    INK.stroke(ctx, skin, pts, t, { width: 5, amp: 1.6, freq: 0.06, seed: 60, close: true });

    // rotation of 120° during triangle transition
    if (t >= 6.45 && t < 7.0) {
      const p = spring(20, 0.42)(t - 6.45);
      const spun = GEO.rotate(pts, CX, 540, rad(120) * (p - 0.5) * 2 * 0.25);
      INK.stroke(ctx, skin, spun, t, { width: 2.4, color: skin.accent, amp: 1.2, seed: 61, ghost: false, close: true });
    }

    // blue hatching fills the hexagon, crosshatching afterwards
    if (t >= 7.55) {
      const fillP = expOut(7, 0.35)(t - 7.55);
      const clipPts = GEO.resample(pts, 97);
      ctx.save();
      ctx.beginPath();
      INK.pathTo(ctx, clipPts, true);
      ctx.clip();
      const full = GEO.polygon(CX, 540, 300, 6, true);
      const revealX = lerp(CX - 320, CX + 320, fillP);
      ctx.beginPath();
      ctx.rect(CX - 340, 200, (revealX - (CX - 340)), 700);
      ctx.clip();
      INK.hatchFill(ctx, skin, full, t, { angle: -48, pitch: 11, alpha: 0.85, seed: 62 });
      ctx.restore();

      if (t >= 7.75) {
        const crossP = expOut(7, 0.3)(t - 7.75);
        ctx.save();
        ctx.beginPath();
        INK.pathTo(ctx, clipPts, true);
        ctx.clip();
        ctx.beginPath();
        ctx.rect(CX - 340, 200, 680 * crossP, 700);
        ctx.clip();
        INK.hatchFill(ctx, skin, full, t, { angle: 48, pitch: 11, alpha: 0.6, seed: 63 });
        ctx.restore();
      }
    }
  }

  /* =========================================================================
   * BAR 5 (8–10) PAPER · NODE & GRAPH — honeycomb, solid flood, hex flood-out.
   * ====================================================================== */

  function bar5(ctx, skin, t) {
    const shrink = spring(22, 0.35)(t - 7.95);
    const r = lerp(270, 96, clamp(shrink, 0, 1));
    const cells = GEO.honeycomb(CX, 540, 96);

    for (let i = 0; i < cells.length; i++) {
      const cell = cells[i];
      const ring = cell.ring;
      let show = 1, dx = 0, dy = 0, cr = r;
      if (ring === 1) {
        show = spring(20, 0.4)(t - 7.95);
        const k = 1 - show;
        dx = (cell.x - CX) * k; dy = (cell.y - 540) * k;
      } else if (ring === 2) {
        show = spring(20, 0.42)(t - 8.45);
        const k = 1 - show;
        dx = (cell.x - CX) * k * 1.6; dy = (cell.y - 540) * k * 1.6;
      }
      if (show <= 0.002) continue;
      const x = CX + dx * (1) + (cell.x - CX) * (ring === 0 ? 0 : 1) * (ring !== 0 ? 1 : 0);
      const px = ring === 0 ? CX : CX + (cell.x - CX) * show + (cell.x - CX) * (1 - show) * 0;
      const py = ring === 0 ? 540 : 540 + (cell.y - 540) * show;
      const hexPts = GEO.resample(GEO.polygon(ring === 0 ? CX : px, ring === 0 ? 540 : py, cr * show + 96 * (1 - show) * show, 6, true), 48);
      const h2 = GEO.resample(GEO.polygon(ring === 0 ? CX : px, ring === 0 ? 540 : py, Math.max(4, 96 * show + (cr - 96) * show * show), 6, true), 48);
      INK.stroke(ctx, skin, h2, t, { width: 4.2, amp: 1.5, freq: 0.09, seed: 70 + i, close: true });
    }

    // 9.0 dense hatching sweeps across, all cells filled solid blue
    if (t >= 8.95 && t < 9.98) {
      const sweep = expOut(6, 0.5)(t - 8.95);
      ctx.save();
      ctx.beginPath();
      ctx.rect(0, 0, W * sweep, H);
      ctx.clip();
      for (const cell of cells) {
        const hexPts = GEO.resample(GEO.polygon(cell.x, cell.y, 94, 6, true), 36);
        INK.hatchFill(ctx, skin, hexPts, t, { angle: -55, pitch: 6, alpha: 0.95, seed: 80 + cell.ring });
      }
      ctx.restore();
    }
  }

  /* =========================================================================
   * BLUEPRINT bars 6–7 · 3D NODE → GEAR
   * ====================================================================== */

  function bar6(ctx, skin, t) {
    // corner-view cube wireframes: 3 solid + 3 dashed hidden edges per cell
    const cells = GEO.honeycomb(CX, 540, 96);
    const merge = spring(18, 0.42)(t - 10.45);        // outer cubes slide inward
    const grow = spring(16, 0.5)(t - 10.45);          // centre cube grows to 270
    // perspective-activated turn: 0 → 90° (11.0–11.45), back to front-on (11.7)
    const turnUp = spring(18, 0.42)(t - 11.0);
    const turnBack = spring(18, 0.28)(t - 11.45);
    const spinP = t < 11.0 ? 0 : Math.max(0, turnUp - turnBack);

    for (let i = 0; i < cells.length; i++) {
      const cell = cells[i];
      let x = cell.x, y = cell.y, r = 96;
      if (cell.ring > 0) {
        if (merge > 0.001) {
          x = lerp(cell.x, CX, merge);
          y = lerp(cell.y, 540, merge);
          r = lerp(96, 40, merge);
        }
        if (merge >= 0.999) continue;
      } else {
        r = lerp(96, 270, grow);
      }
      const drawP = expOut(8, 0.3)(t - 9.95 - i * 0.012);
      if (drawP <= 0.01) continue;

      // rotate about the vertical axis with perspective squash
      const theta = spinP * rad(90);
      const squash = Math.cos(theta) * 0.55 + 0.45;
      const cube = GEO.cubeEdges(x, y, r * drawP, theta * 0.5);
      const squashPts = (pts) => pts.map((p) => ({ x: x + (p.x - x) * squash, y: p.y }));

      for (const seg of cube.vis) {
        INK.stroke(ctx, skin, squashPts(seg), t, { width: 3.6, color: skin.ink, amp: 0.7, seed: 90 + i, ghost: false });
      }
      for (const seg of cube.spokes) {
        INK.stroke(ctx, skin, squashPts(seg), t, { width: 3.2, color: skin.ink, amp: 0.7, seed: 91 + i, ghost: false });
      }
      for (const seg of cube.back) {
        // hidden/back edges: thinner dashed lines
        INK.wire(ctx, squashPts(seg), { width: 1.8, color: skin.ink, alpha: 0.85, dashed: true, dash: [7, 7] });
      }
    }
  }

  function bar7(ctx, skin, t) {
    // 12.0 cube flattens → 16-tooth gear, pitch radius 208
    const teethGrow = expOut(9, 0.3)(t - 11.95);
    const revert = expIn(7, 0.45)(t - 13.45);
    const gearP = clamp(teethGrow * (1 - revert), 0, 1);
    const depth = (1 - gearP) * lerp(0, 1, expOut(6, 0.4)(t - 13.62));

    // stepped rotation: 45° every eighth note (0.25 s) starting at 12.25
    const eighths = Math.max(0, Math.floor((t - 12.25) / 0.25));
    const stepT = (t - 12.25) - eighths * 0.25;
    const stepP = t < 12.25 ? 0 : spring(32, 0.09)(stepT);
    const gearAngle = (eighths * 45 + stepP * 45) * (Math.PI / 180) * (t >= 12.25 ? 1 : 0);

    if (gearP > 0.01 || depth > 0.01) {
      // main gear 16T
      const r = 208;
      const teeth = Math.round(lerp(0, 16, gearP));
      const toothH = 28 * gearP;
      const pts = teeth > 0
        ? GEO.gear(CX, 540, teeth, r, toothH, gearAngle)
        : GEO.resample(GEO.polygon(CX, 540, r, 4, false), 96);   // reverts to square
      const sq = (ps) => ps.map((p) => ({ x: CX + (p.x - CX) * (1 - depth * 0.0), y: p.y }));
      INK.stroke(ctx, skin, sq(pts), t, { width: 3.8, color: skin.ink, amp: 0.7, seed: 100, close: true, ghost: false });

      // dashed pitch circle + hub
      if (gearP > 0.2) {
        INK.wire(ctx, GEO.circle(CX, 540, 208, 80), { width: 1.8, dashed: true, dash: [9, 8], alpha: 0.9 });
        INK.stroke(ctx, skin, GEO.circle(CX, 540, 34, 40), t, { width: 3.2, color: skin.ink, amp: 0.6, seed: 101, close: true, ghost: false });
        INK.stroke(ctx, skin, GEO.circle(CX, 540, 12, 24), t, { width: 2.6, color: skin.ink, amp: 0.5, seed: 102, close: true, ghost: false });
        drawWipedText(ctx, '16T', CX, 540 - 232, 24, 700, 0.2, skin.accent, gearP);
      }

      // 10T gear upper right (12.5) — meshing, opposite rotation
      const g2 = expOut(8, 0.35)(t - 12.45) * (1 - expIn(7, 0.4)(t - 13.45));
      if (g2 > 0.02) {
        const gx = CX + 320, gy = 540 - 190;
        const p2 = GEO.gear(gx, gy, 10, 122, 26 * g2, -gearAngle * (16 / 10));
        INK.stroke(ctx, skin, p2, t, { width: 3.2, color: skin.ink, amp: 0.7, seed: 103, close: true, ghost: false });
        INK.wire(ctx, GEO.circle(gx, gy, 122 * g2, 64), { width: 1.6, dashed: true, dash: [8, 7], alpha: 0.85 });
        drawWipedText(ctx, '10T', gx, gy - 146, 24, 700, 0.2, skin.accent, g2);
      }
      // 12T gear upper left (13.0)
      const g3 = expOut(8, 0.35)(t - 12.95) * (1 - expIn(7, 0.4)(t - 13.45));
      if (g3 > 0.02) {
        const gx = CX - 330, gy = 540 - 175;
        const p3 = GEO.gear(gx, gy, 12, 148, 28 * g3, -gearAngle * (16 / 12));
        INK.stroke(ctx, skin, p3, t, { width: 3.2, color: skin.ink, amp: 0.7, seed: 104, close: true, ghost: false });
        INK.wire(ctx, GEO.circle(gx, gy, 148 * g3, 64), { width: 1.6, dashed: true, dash: [8, 7], alpha: 0.85 });
        drawWipedText(ctx, '12T', gx, gy - 172, 24, 700, 0.2, skin.accent, g3);
      }
    }

    // cube depth restored at 13.62+
    if (depth > 0.02) {
      const hex = GEO.resample(GEO.polygon(CX, 540, 208, 6, true), 60);
      INK.stroke(ctx, skin, hex, t, { width: 3, color: skin.ink, amp: 0.7, seed: 105, close: true, ghost: false });
    }
  }

  /* =========================================================================
   * BAR 8 (14–16) BLUEPRINT · PANEL UNFOLD — cube net → UI panels.
   * ====================================================================== */

  const PANELS = [
    { id: 'header', x: 150, y: 120, w: 1620, h: 62, label: 'HEADER' },
    { id: 'side', x: 150, y: 210, w: 170, h: 660, label: 'SIDEBAR' },
    { id: 'chart', x: 350, y: 210, w: 860, h: 400, label: 'CHART' },
    { id: 'tileA', x: 1240, y: 210, w: 255, h: 190, label: 'TILE' },
    { id: 'tileB', x: 1515, y: 210, w: 255, h: 190, label: 'TILE' },
    { id: 'timeline', x: 350, y: 640, w: 1420, h: 230, label: 'TIMELINE' }
  ];

  /** Six squares of the cube net at t≈14.2 (cross shape). */
  const NET = [
    { x: 960, y: 540 }, { x: 960 - 160, y: 540 }, { x: 960 + 160, y: 540 },
    { x: 960, y: 540 - 160 }, { x: 960, y: 540 + 160 }, { x: 960, y: 540 + 320 }
  ];

  function bar8(ctx, skin, t) {
    const unfold = spring(18, 0.45)(t - 13.95);
    const slide = spring(16, 0.5)(t - 14.45);

    for (let i = 0; i < 6; i++) {
      const P = PANELS[i];
      // square size morphs from 160 net squares into panel rects
      const net = NET[i];
      const cx0 = net.x, cy0 = net.y;
      const x = lerp(cx0 - 80, P.x, slide);
      const y = lerp(cy0 - 80, P.y, slide);
      const w = lerp(160, P.w, slide);
      const h = lerp(160, P.h, slide);
      const pts = GEO.roundRect(x, y, w, h, slide < 0.9 ? 4 : 6, 3);
      INK.stroke(ctx, skin, pts, t, { width: 3.4, color: skin.ink, amp: 0.8, seed: 110 + i, close: true, ghost: false });

      // hinge lines while unfolding
      if (unfold > 0.02 && slide < 0.55) {
        const hp = 1 - slide * 2;
        for (const [x1, y1, x2, y2] of [
          [x, y + h / 2, x + w, y + h / 2],
          [x + w / 2, y, x + w / 2, y + h]
        ]) {
          INK.wire(ctx, GEO.line(x1, y1, x2, y2, 12), { width: 1.6, dashed: true, dash: [6, 6], alpha: 0.7 * hp });
        }
      }
    }

    /* content populates from 15.0 */
    const pop = expOut(6, 0.5)(t - 14.95);
    if (pop > 0.02) drawPanelContent(ctx, skin, t, pop);
  }

  function drawPanelContent(ctx, skin, t, pop) {
    const ac = skin.accent;
    // header
    drawWipedText(ctx, 'ARTEX — AUTONOMOUS ASSESSMENT AGENT', 170, 160, 26, 700, 0.14, skin.ink, expOut(8, 0.35)(t - 15.0), 'left');
    // toggles in header right: [label x, switch x]
    const toggles = [
      { label: 'BOUNDARY', x: 1120, sx: 1290, on: true },
      { label: 'LOG', x: 1380, sx: 1470, on: false },
      { label: 'AUDIT', x: 1550, sx: 1660, on: false }
    ];
    for (let i = 0; i < toggles.length; i++) {
      const tg = toggles[i];
      const tp = expOut(9, 0.3)(t - 15.08 - i * 0.08);
      if (tp <= 0.01) continue;
      drawWipedText(ctx, tg.label, tg.x, 158, 22, 700, 0.16, skin.ink, tp, 'left');
      const sw = GEO.roundRect(tg.sx, 138, 52, 26, 13, 4);
      INK.stroke(ctx, skin, sw, t, { width: 2.4, color: skin.ink, amp: 0.5, seed: 120 + i, close: true, ghost: false });
      const kx = tg.sx + (tg.on ? 30 : 8);
      INK.stroke(ctx, skin, GEO.circle(kx + 7, 151, 8, 16), t,
        { width: 2, color: ac, amp: 0.4, seed: 123 + i, close: true, ghost: false });
    }

    // sidebar lines
    for (let i = 0; i < 7; i++) {
      const p = expOut(10, 0.25)(t - 15.12 - i * 0.05);
      if (p <= 0.01) continue;
      const y = 250 + i * 54;
      INK.stroke(ctx, skin, GEO.line(172, y, 172 + 126 * p, y, 8), t,
        { width: 2.6, color: skin.ink, amp: 0.7, seed: 130 + i, ghost: false });
    }

    // chart: 60 bars (30-value sequence ×2)
    const cx0 = 350 + 40, cy1 = 210 + 340, cw = 860 - 80, ch = 250;
    const n = 60;
    for (let i = 0; i < n; i++) {
      const p = spring(30, 0.22)(t - 15.15 - i * 0.008);
      if (p <= 0.004) continue;
      const v = CHART[i % 30] / 100;
      const bw = cw / n;
      const bh = v * ch * p;
      const x = cx0 + i * bw;
      const pts = GEO.roundRect(x + 1, cy1 - bh, bw - 2.4, Math.max(2, bh), 1, 1);
      INK.stroke(ctx, skin, pts, t, { width: 1.7, color: skin.ink, amp: 0.4, seed: 140 + i, close: true, ghost: false });
      if (p > 0.85) {
        INK.hatchFill(ctx, skin, pts, t, { angle: -70, pitch: 5, alpha: 0.42, color: skin.ink, seed: 150 + i });
      }
    }

    // timeline strip: 30 bar names + keyframe diamonds
    const tlx = 350 + 40, tly = 640 + 120, tlw = 1420 - 80;
    INK.stroke(ctx, skin, GEO.line(tlx, tly, tlx + tlw, tly, 48), t, { width: 3, color: skin.ink, amp: 0.8, seed: 160, ghost: false });
    for (let i = 0; i < 30; i++) {
      const p = expOut(12, 0.3)(t - 15.25 - i * 0.018);
      if (p <= 0.01) continue;
      const x = tlx + (i / 29) * tlw;
      // keyframe diamond
      const d = 7;
      INK.stroke(ctx, skin, [{ x, y: tly - d }, { x: x + d, y: tly }, { x, y: tly + d }, { x: x - d, y: tly }, { x, y: tly - d }], t,
        { width: 2, color: ac, amp: 0.4, seed: 170 + i, close: true, ghost: false });
      if (i % 2 === 0) {
        ctx.save();
        ctx.font = `600 13px Geist, sans-serif`;
        ctx.fillStyle = skin.ink;
        ctx.globalAlpha = p;
        ctx.translate(x, tly + 26);
        ctx.rotate(rad(-38));
        ctx.fillText(HF.BAR_NAMES[i], 0, 0);
        ctx.restore();
      }
    }

    // counters roll upwards at 15.5
    const roll = spring(20, 0.5)(t - 15.45);
    if (roll > 0.02) {
      const items = [
        ['120', 'BPM'], ['120', 'BEATS'], ['3600', 'FRAMES'], ['0', 'CUTS']
      ];
      for (let i = 0; i < items.length; i++) {
        const x = 420 + i * 200;
        const y = 640 + 58 + (1 - roll) * 60;
        rollingNum(ctx, parseFloat(items[i][0]) * roll, x, y, 34, 800, 0.06, skin.ink, t, 'left', String(items[i][0]).length);
        trackedText(ctx, items[i][1], x + 8, y + 30, 18, 700, 0.2, ac, 'left', roll > 0.5 ? 1 : 0);
      }
    }
  }

  /* =========================================================================
   * BAR 9 (16–18) BLUEPRINT · DATA — graph line, playhead, cursor, warp.
   * ====================================================================== */

  function bar9(ctx, skin, t) {
    const ac = skin.accent;
    const cx0 = 390, cy1 = 550, cw = 780, ch = 250;
    const n = 60;

    // graph line draws across the tops of the chart bars
    const lineP = expOut(7, 0.45)(t - 15.95);
    if (lineP > 0.02) {
      const pts = [];
      for (let i = 0; i < n; i++) {
        const v = CHART[i % 30] / 100;
        pts.push({ x: cx0 + (i + 0.5) * (cw / n), y: cy1 - v * ch });
      }
      const cut = Math.max(2, Math.floor(pts.length * lineP));
      INK.stroke(ctx, skin, pts.slice(0, cut), t, { width: 2.6, color: ac, amp: 0.8, seed: 180, ghost: false });
    }

    // playhead follows real reel time along the timeline
    const tlx = 390, tly = 760, tlw = 1340;
    const px = tlx + (t / 60) * tlw;
    if (t < 24) {
      INK.stroke(ctx, skin, GEO.line(px, tly - 46, px, tly + 46, 12), t,
        { width: 2.4, color: ac, amp: 0.5, seed: 181, ghost: false });
      // active bar solid cyan · past hatched · future outlined
      const active = Math.floor((t / 60) * 30);
      for (let i = 0; i < 30; i++) {
        const x = tlx + (i / 29) * tlw - 10;
        const box = GEO.roundRect(x, tly - 30, 22, 60, 2, 1);
        if (i === active) {
          INK.hatchFill(ctx, skin, box, t, { color: ac, angle: -90, pitch: 5, alpha: 0.95, seed: 190 });
        } else if (i < active) {
          INK.hatchFill(ctx, skin, box, t, { color: skin.ink, angle: -55, pitch: 6, alpha: 0.35, seed: 191 + i });
        } else {
          INK.wire(ctx, box, { width: 1.6, alpha: 0.7 });
        }
      }
    }

    // mouse cursor animates into view, clicks BOUNDARY toggle at 16.5
    const curIn = spring(18, 0.4)(t - 15.95);
    const clickP = spring(30, 0.12)(t - 16.45);
    const leaveP = expIn(6, 0.4)(t - 17.1);
    if (curIn > 0.02 && leaveP < 0.999) {
      const cur = {
        x: lerp(1900, 1286, curIn) + leaveP * 400,
        y: lerp(980, 148, curIn) + leaveP * -300
      };
      const s = 1 - clickP * 0.12 + clickP * 0.12 * 0;
      ctx.save();
      ctx.translate(cur.x, cur.y);
      ctx.scale(s, s);
      const arrow = [
        { x: 0, y: 0 }, { x: 0, y: 26 }, { x: 6.2, y: 19.5 }, { x: 11, y: 30 },
        { x: 15.6, y: 27.6 }, { x: 11, y: 17.4 }, { x: 19, y: 16 }, { x: 0, y: 0 }
      ];
      INK.stroke(ctx, skin, arrow, t, { width: 2.2, color: skin.ink, amp: 0.4, seed: 200, close: true, ghost: false });
      ctx.restore();
    }

    // 16.5 chart baseline warps and curves into a circle r=110
    const warp = spring(16, 0.8)(t - 16.55);
    if (warp > 0.02) {
      const base = GEO.line(cx0, cy1, cx0 + cw, cy1, 96);
      const circ = GEO.circle(CX + 210, 640, 110, 96);
      const pts = GEO.morph(GEO.resample(base, 96), circ, warp);
      INK.stroke(ctx, skin, pts, t, { width: 3.2, color: skin.ink, amp: 0.9, seed: 201, close: true, ghost: false });
      // audit log nodes around the perimeter
      const nodeP = expOut(8, 0.5)(t - 17.05);
      for (let i = 0; i < 8; i++) {
        const p = expOut(10, 0.3)(t - 17.05 - i * 0.06);
        if (p <= 0.02) continue;
        const a = (i / 8) * TAU;
        const nx = CX + 210 + Math.cos(a) * 110 * p;
        const ny = 640 + Math.sin(a) * 110 * p;
        INK.stroke(ctx, skin, GEO.roundRect(nx - 7, ny - 7, 14, 14, 2, 1), t,
          { width: 2, color: skin.ink, amp: 0.4, seed: 210 + i, close: true, ghost: false });
      }
    }
  }

  /* =========================================================================
   * BAR 10 (18–20) BLUEPRINT · RULE — rule tree, prune, data stream, duals.
   * ====================================================================== */

  function bar10(ctx, skin, t) {
    const ac = skin.accent;
    // 18.0 circle splits into a rule tree
    const split = spring(18, 0.5)(t - 17.95);
    const prune = expOut(9, 0.5)(t - 18.55);
    const compress = spring(20, 0.4)(t - 19.05);
    const dual = spring(20, 0.35)(t - 19.55);

    // the audit circle migrates to the tree root and unravels into branches
    const unravel = spring(18, 0.5)(t - 17.95);
    if (unravel < 0.98) {
      const cx = lerp(CX + 210, 360, unravel);
      const cy = lerp(640, 540, unravel);
      const r = lerp(110, 10, unravel);
      INK.stroke(ctx, skin, GEO.circle(cx, cy, r, 64), t,
        { width: 3, color: skin.ink, amp: 0.6, seed: 219, close: true, ghost: false });
      for (let i = 0; i < 8; i++) {
        const a = (i / 8) * TAU;
        const nx = cx + Math.cos(a) * r;
        const ny = cy + Math.sin(a) * r;
        INK.stroke(ctx, skin, GEO.roundRect(nx - 7 * (1 - unravel), ny - 7 * (1 - unravel), 14 * (1 - unravel), 14 * (1 - unravel), 2, 1), t,
          { width: 2, color: skin.ink, amp: 0.4, seed: 210 + i, close: true, ghost: false });
      }
    }

    const root = { x: 360, y: 540 };
    const branches = [
      { y: 320, label: 'SCOPE', valid: true },
      { y: 440, label: 'PERMISSION', valid: true },
      { y: 560, label: 'WHITELIST', valid: true },
      { y: 680, label: 'PROBE', valid: false },
      { y: 800, label: 'EXTRA', valid: false }
    ];

    // trunk
    const trunkEnd = lerp(root.x, 760, split * (1 - compress));
    INK.stroke(ctx, skin, GEO.line(root.x, root.y, trunkEnd, root.y, 24), t,
      { width: 3.6, color: skin.ink, amp: 0.8, seed: 220, ghost: false });

    for (let i = 0; i < branches.length; i++) {
      const b = branches[i];
      const grow = expOut(8, 0.35)(t - 17.95 - i * 0.09);
      if (grow <= 0.01) continue;
      const targetY = lerp(root.y, b.y, split);
      const endX = lerp(root.x, 1180, grow);
      const midX = root.x + (endX - root.x) * 0.5;
      const pts = GEO.bezier(
        { x: trunkEnd, y: root.y },
        { x: midX, y: root.y },
        { x: midX, y: targetY },
        { x: endX, y: targetY }, 40
      );
      // prune invalid branches
      const alive = b.valid ? 1 : 1 - prune;
      if (alive <= 0.02) continue;
      const cut = Math.max(2, Math.floor(pts.length * alive));
      INK.stroke(ctx, skin, pts.slice(0, cut), t,
        { width: 3, color: b.valid ? skin.ink : ac, amp: 0.8, seed: 221 + i, ghost: false });
      if (b.valid) {
        drawWipedText(ctx, b.label, endX + 18, targetY + 8, 24, 700, 0.18, skin.ink, grow);
        // leaf node
        INK.stroke(ctx, skin, GEO.roundRect(endX - 8, targetY - 8, 16, 16, 2, 1), t,
          { width: 2.2, color: skin.ink, amp: 0.4, seed: 230 + i, close: true, ghost: false });
      }
    }

    // 19.0 compresses into a single horizontal data stream line
    if (compress > 0.02) {
      const lx1 = lerp(root.x, 222, compress), lx2 = lerp(1180, 1698, compress);
      const y = lerp(540, 540, 1);
      INK.stroke(ctx, skin, GEO.line(lx1, y, lx2, y, 64), t,
        { width: 4.4, color: skin.ink, amp: 1.1, seed: 232, ghost: false });
      // stream ticks travelling
      for (let i = 0; i < 7; i++) {
        const sp = ((t * 0.55 + i / 7) % 1);
        const x = lerp(lx1, lx2, sp);
        INK.stroke(ctx, skin, GEO.line(x, y - 12, x, y + 12, 6), t,
          { width: 2, color: ac, amp: 0.5, seed: 233 + i, ghost: false });
      }
    }

    // 19.5 splits into request/response dual lines
    if (dual > 0.02) {
      const gap = 90 * dual;
      INK.stroke(ctx, skin, GEO.line(222, 540 - gap, 1698, 540 - gap, 64), t,
        { width: 3.6, color: skin.ink, amp: 1, seed: 234, ghost: false });
      INK.stroke(ctx, skin, GEO.line(222, 540 + gap, 1698, 540 + gap, 64), t,
        { width: 3.6, color: skin.ink, amp: 1, seed: 235, ghost: false });
    }
  }

  /* =========================================================================
   * BAR 11 (20–22) BLUEPRINT · TOOL — tool icons, packets, node graph, risk.
   * ====================================================================== */

  function bar11(ctx, skin, t) {
    const ac = skin.accent;
    const yReq = 450, yRes = 630;
    INK.stroke(ctx, skin, GEO.line(222, yReq, 1698, yReq, 64), t, { width: 3.6, color: skin.ink, amp: 1, seed: 240, ghost: false });
    INK.stroke(ctx, skin, GEO.line(222, yRes, 1698, yRes, 64), t, { width: 3.6, color: skin.ink, amp: 1, seed: 241, ghost: false });

    // tool icons: HTTP, DNS, SCANNER
    const tools = ['HTTP', 'DNS', 'SCANNER'];
    const toolPos = [];
    for (let i = 0; i < 3; i++) {
      const p = expOut(8, 0.3)(t - 19.95 - i * 0.12);
      if (p <= 0.01) continue;
      const x = 420 + i * 300, y = yReq - 70;
      toolPos.push({ x, y, label: tools[i] });
      const box = GEO.roundRect(x - 80, y - 34, 160, 68, 8, 3);
      const cut = Math.max(3, Math.floor(box.length * p));
      INK.stroke(ctx, skin, box.slice(0, cut), t, { width: 3, color: skin.ink, amp: 0.7, seed: 242 + i, ghost: false });
      if (p > 0.5) drawWipedText(ctx, tools[i], x, y + 8, 24, 700, 0.18, skin.ink, (p - 0.5) * 2);
      // stem to request line
      INK.stroke(ctx, skin, GEO.line(x, y + 34, x, yReq, 10), t,
        { width: 2.2, color: skin.ink, amp: 0.5, seed: 245 + i, ghost: false });
    }

    // 20.5 response line returns data packets (outlined rectangles)
    for (let i = 0; i < 5; i++) {
      const t0 = 20.45 + i * 0.28;
      const travel = clamp((t - t0) / 1.5, 0, 1);
      if (travel <= 0 || travel >= 1) continue;
      const x = lerp(1620, 320, travel);
      const box = GEO.roundRect(x - 26, yRes - 16, 52, 32, 3, 2);
      INK.stroke(ctx, skin, box, t, { width: 2.4, color: skin.ink, amp: 0.5, seed: 250 + i, close: true, ghost: false });
      INK.hatchFill(ctx, skin, box, t, { color: ac, angle: -60, pitch: 6, alpha: 0.5, seed: 255 + i });
    }

    // 21.0 tools feed data into a central node graph
    const feed = spring(18, 0.5)(t - 20.95);
    const nodes = [
      { x: 960, y: 300 }, { x: 830, y: 380 }, { x: 1090, y: 380 },
      { x: 760, y: 500 }, { x: 1160, y: 500 }, { x: 960, y: 430 }
    ];
    if (feed > 0.02) {
      for (let i = 1; i < nodes.length; i++) {
        const a = nodes[0], b = nodes[i];
        const pts = GEO.line(a.x, a.y, b.x, b.y, 16);
        const cut = Math.max(2, Math.floor(pts.length * feed));
        INK.stroke(ctx, skin, pts.slice(0, cut), t,
          { width: 2.4, color: skin.ink, amp: 0.6, seed: 260 + i, ghost: false });
      }
      for (let i = 0; i < nodes.length; i++) {
        const p = expOut(9, 0.3)(t - 21.0 - i * 0.07);
        if (p <= 0.02) continue;
        INK.stroke(ctx, skin, GEO.circle(nodes[i].x, nodes[i].y, 12 * p, 20), t,
          { width: 2.4, color: skin.ink, amp: 0.4, seed: 270 + i, close: true, ghost: false });
      }
    }

    // 21.5 risk priority numbers float in
    const riskP = expOut(8, 0.4)(t - 21.55);
    if (riskP > 0.02) {
      const risks = [87, 64, 41, 73, 52, 95];
      for (let i = 0; i < nodes.length; i++) {
        const float = Math.sin(t * 2 + i) * 4;
        rollingNum(ctx, risks[i] * riskP, nodes[i].x + 22, nodes[i].y - 18 + float, 22, 700, 0.1, ac, t, 'left', 2);
      }
    }
  }

  /* =========================================================================
   * BAR 12 (22–24) BLUEPRINT · PRIORITY & LOG
   * ====================================================================== */

  const LOG_LINES = [
    '12:04:11  SCOPE BOUNDARY CONFIRMED',
    '12:04:12  ASSET ENDPOINT ENUMERATED',
    '12:04:14  PERMISSION GRANTED · WHITELIST',
    '12:04:17  REQUEST DISPATCHED · HTTP',
    '12:04:19  RESPONSE CAPTURED · 200 OK',
    '12:04:21  RISK SCORE COMPUTED · 87',
    '12:04:23  AUDIT TRAIL SEALED'
  ];

  function bar12(ctx, skin, t) {
    const ac = skin.accent;
    // nodes sort vertically by risk priority
    const sortP = spring(18, 0.5)(t - 21.95);
    const nodes = [
      { x: 520, y: lerp(300, 260, sortP), risk: 95 },
      { x: 520, y: lerp(380, 360, sortP), risk: 87 },
      { x: 520, y: lerp(500, 460, sortP), risk: 73 },
      { x: 520, y: lerp(430, 560, sortP), risk: 64 },
      { x: 520, y: lerp(380, 660, sortP), risk: 52 },
      { x: 520, y: lerp(500, 760, sortP), risk: 41 }
    ];
    if (t < 23.45) {
      for (let i = 0; i < nodes.length; i++) {
        const n = nodes[i];
        INK.stroke(ctx, skin, GEO.roundRect(n.x - 60, n.y - 18, 120, 36, 6, 2), t,
          { width: 2.6, color: skin.ink, amp: 0.6, seed: 280 + i, close: true, ghost: false });
        rollingNum(ctx, n.risk, n.x + 78, n.y + 8, 22, 700, 0.1, ac, t, 'left', 2);
        // connector to log column
        INK.stroke(ctx, skin, GEO.line(n.x + 60, n.y, 860, n.y, 16), t,
          { width: 1.8, color: skin.ink, amp: 0.5, seed: 285 + i, ghost: false });
      }
    }

    // log panel types line by line
    const lp = expOut(7, 0.3)(t - 22.45);
    if (lp > 0.02) {
      const box = GEO.roundRect(880, 240, 720, 560, 8, 3);
      INK.stroke(ctx, skin, box, t, { width: 3, color: skin.ink, amp: 0.8, seed: 290, close: true, ghost: false });
      drawWipedText(ctx, 'AUDIT LOG', 910, 282, 22, 700, 0.22, ac, expOut(9, 0.3)(t - 22.5), 'left');
      // terminal flow: entries scroll downward after 23.0
      const scroll = expOut(5, 1.0)(t - 22.95) * 260;
      for (let i = 0; i < LOG_LINES.length; i++) {
        const typed = expOut(16, 0.42)(t - 22.55 - i * 0.16);
        if (typed <= 0.01) continue;
        const y = 330 + i * 62 + scroll * (i / LOG_LINES.length + 0.25);
        if (y > 790) continue;
        drawWipedText(ctx, LOG_LINES[i], 910, y, 19, 600, 0.08, skin.ink, typed, 'left');
      }
    }
  }

  /* =========================================================================
   * BAR 13 (24–26) HOLOGRAM · LIGHT & VERIFY
   * ====================================================================== */

  function bar13(ctx, skin, t) {
    // node graph redrawn with cyan lines + white core (same geometry)
    const nodes = [
      { x: 520, y: 300 }, { x: 520, y: 420 }, { x: 520, y: 540 },
      { x: 520, y: 660 }, { x: 520, y: 780 }, { x: 800, y: 540 },
      { x: 1080, y: 420 }, { x: 1080, y: 660 }, { x: 1360, y: 540 }
    ];
    const redraw = expOut(8, 0.45)(t - 23.95);
    for (let i = 0; i < nodes.length - 1; i++) {
      const a = nodes[i], b = nodes[(i + 1) % nodes.length];
      const pts = GEO.line(a.x, a.y, b.x, b.y, 24);
      const cut = Math.max(2, Math.floor(pts.length * redraw));
      if (redraw <= 0.01) continue;
      INK.stroke(ctx, skin, pts.slice(0, cut), t,
        { width: 3.6, color: skin.ink, amp: 0.7, seed: 300 + i, ghost: false });
    }
    // extra spokes
    for (const [i, j] of [[0, 5], [1, 5], [2, 5], [3, 5], [4, 5], [5, 6], [5, 7], [6, 8], [7, 8]]) {
      const a = nodes[i], b = nodes[j];
      const pts = GEO.line(a.x, a.y, b.x, b.y, 16);
      const cut = Math.max(2, Math.floor(pts.length * redraw));
      if (redraw <= 0.01) continue;
      INK.stroke(ctx, skin, pts.slice(0, cut), t,
        { width: 2.6, color: skin.ink, amp: 0.6, seed: 310 + i, ghost: false });
    }
    for (let i = 0; i < nodes.length; i++) {
      const p = expOut(9, 0.3)(t - 23.95 - i * 0.05);
      if (p <= 0.02) continue;
      INK.stroke(ctx, skin, GEO.circle(nodes[i].x, nodes[i].y, 13 * p, 22), t,
        { width: 3, color: skin.ink, amp: 0.4, seed: 320 + i, close: true, ghost: false });
    }

    // 24.5 status labels
    const labels = ['REACHED', 'CONFIRMED', 'LOGGED'];
    for (let i = 0; i < 3; i++) {
      const p = expOut(10, 0.3)(t - 24.45 - i * 0.14);
      if (p <= 0.02) continue;
      drawWipedText(ctx, labels[i], 700 + i * 300, 240, 26, 700, 0.22, skin.highlight, p);
    }

    // 25.0 mascot runs along the hologram data lines
    const runIn = expOut(6, 0.9)(t - 24.95);
    const pause = spring(22, 0.3)(t - 25.45);
    const running = t < 25.45 ? 1 : 1 - pause;
    const mx = lerp(320, 1180, runIn);
    const my = 540 + Math.sin(mx * 0.01) * 60;
    if (runIn > 0.02) {
      MASCOT.draw(ctx, mx, my - 2, t, { skin, scale: 1, running, lean: 7 * running, tap: t > 25.55 ? spring(24, 0.35)(t - 25.55) * (1 - spring(18, 0.4)(t - 25.8)) : 0 });
    }

    // 25.5 pencil taps a node → validation check
    const tapP = expOut(9, 0.4)(t - 25.55);
    if (tapP > 0.02) {
      const nx = 1220, ny = 470;
      const check = [
        { x: nx - 26, y: ny + 2 }, { x: nx - 8, y: ny + 22 }, { x: nx + 30, y: ny - 26 }
      ];
      const cut = Math.max(2, Math.floor(check.length * tapP * 2));
      INK.stroke(ctx, skin, check.slice(0, Math.min(check.length, cut)), t,
        { width: 5, color: skin.highlight, amp: 0.8, seed: 330, ghost: false });
    }
  }

  /* =========================================================================
   * BAR 14 (26–28) HOLOGRAM · WAVE & GLOBE
   * ====================================================================== */

  function bar14(ctx, skin, t) {
    const form = spring(16, 0.5)(t - 25.95);       // nodes → globe
    const decay = expIn(5, 0.55)(t - 26.95);       // globe → waveform
    const fold = spring(18, 0.45)(t - 27.45);      // waveform → pipeline

    if (form > 0.02 && decay < 0.999) {
      const rings = GEO.globe(CX, 540, 300 * form, 5, 8, t * 0.35, 0.35);
      for (let i = 0; i < rings.length; i++) {
        const ring = rings[i];
        const pts = ring.pts.map((p) => ({
          x: lerp(p.x, p.x, 1 - decay) + (p.x - CX) * 0,
          y: lerp(p.y, 540, decay * (1 - decay) * 2 * 0) 
        }));
        // decompose: push points toward the waveform
        const warped = ring.pts.map((p, k) => {
          const wy = 540 + Math.sin(k * 0.24 + t * 3) * 90 * decay;
          return { x: lerp(p.x, lerp(222, 1698, k / (ring.pts.length - 1)), decay), y: lerp(p.y, wy, decay) };
        });
        INK.stroke(ctx, skin, warped, t, {
          width: ring.kind === 'lat' ? 3 : 2.4,
          color: skin.ink, amp: 0.5, seed: 340 + i, ghost: false
        });
      }
      // 26.5 ripples travel outward along globe connections
      const rip = expOut(6, 1.2)(t - 26.45);
      if (rip > 0.02 && decay < 0.6) {
        for (let k = 0; k < 3; k++) {
          const rr = ((t - 26.45 - k * 0.35) * 260) % 360;
          if (rr < 0) continue;
          const alpha = 1 - rr / 360;
          INK.beam(ctx, GEO.circle(CX, 540, 60 + rr, 48), {
            width: 2.2, color: skin.ink, core: C.white, alpha: alpha * (1 - decay), coreAlpha: alpha
          });
        }
      }
    }

    // 27.0–27.5 flowing waveform → folds into linear pipeline
    if (decay > 0.02) {
      const waves = 3;
      for (let k = 0; k < waves; k++) {
        const amp = lerp(90, 26, fold) * (1 - k * 0.18);
        const y = lerp(540 + k * 60 - 60, 540, fold);
        const pts = GEO.wave(222, 1698, y, amp, 2.75 + k * 0.3, t * 2.4 + k * 1.1, 110);
        INK.stroke(ctx, skin, pts, t, {
          width: 3.2 - k * 0.5, color: skin.ink, amp: 0.6, seed: 350 + k, ghost: false
        });
      }
    }
  }

  /* =========================================================================
   * BAR 15 (28–30) HOLOGRAM · PIPELINE & AUDIT
   * ====================================================================== */

  function bar15(ctx, skin, t) {
    const shrink = expIn(5, 0.5)(t - 28.95);
    const converge = clamp(shrink, 0, 1);
    const y = 540;
    const x1 = 222, x2 = 1698;

    if (converge < 0.97) {
      // pipeline spine
      const sy = lerp(y, 540, converge);
      INK.stroke(ctx, skin, GEO.line(lerp(x1, 540, converge), sy, lerp(x2, 1380, converge), sy, 64), t,
        { width: lerp(3.6, 2.2, converge), color: skin.ink, amp: 0.8, seed: 360, ghost: false });

      for (let i = 0; i < 5; i++) {
        const p = expOut(8, 0.32)(t - 27.95 - i * 0.11);
        if (p <= 0.02) continue;
        const x = lerp(x1 + 120, x2 - 120, i / 4);
        const w = 150 * (1 - converge), h = 64 * (1 - converge);
        if (w < 6) continue;
        const box = GEO.roundRect(x - w / 2, y - h / 2, w, h, 8, 3);
        const cut = Math.max(3, Math.floor(box.length * p));
        INK.stroke(ctx, skin, box.slice(0, cut), t,
          { width: 2.8, color: skin.ink, amp: 0.6, seed: 361 + i, close: true, ghost: false });
        if (p > 0.5 && converge < 0.35) drawWipedText(ctx, STAGES_5[i], x, y + 8, 24, 700, 0.16, skin.ink, (p - 0.5) * 2);
        // sequential pulse (scan-line fill inside the box)
        if (p > 0.8 && converge < 0.2) {
          const pulse = clamp((t - (27.95 + i * 0.11 + 0.1)) * 2 - i * 0.35, 0, 1);
          if (pulse > 0 && pulse < 1) {
            INK.hatchFill(ctx, skin, box, t, {
              color: C.white, angle: -90, pitch: 8,
              lighting: (u) => 0.8 + u * 1.8, alpha: 0.75 * (1 - Math.abs(pulse - 0.5) * 2), seed: 370 + i
            });
          }
        }
      }

      // 28.5 audit trail line runs parallel to the entire pipeline
      const auditP = expOut(7, 0.4)(t - 28.45);
      if (auditP > 0.02) {
        const ay = lerp(y + 120, 540, converge);
        const pts = GEO.line(x1, ay, lerp(x1, x2, auditP), ay, 48);
        INK.stroke(ctx, skin, pts, t, { width: lerp(2.6, 1.6, converge), color: skin.highlight, amp: 1, seed: 371, ghost: false });
        for (let i = 0; i < 5; i++) {
          const px = lerp(x1 + 120, x2 - 120, i / 4);
          if (px < lerp(x1, x2, auditP) && converge < 0.6) {
            INK.stroke(ctx, skin, GEO.circle(px, ay, 6 * (1 - converge), 14), t,
              { width: 2, color: skin.highlight, amp: 0.3, seed: 372 + i, close: true, ghost: false });
          }
        }
      }
    }

    // 29.0 pipeline shrinks, converges to one thin analytical line
    if (converge > 0.02) {
      const thin = GEO.line(lerp(x1, 540, converge), 540, lerp(x2, 1380, converge), 540, 48);
      INK.stroke(ctx, skin, thin, t, { width: 2.6, color: skin.ink, amp: 0.7, seed: 373, ghost: false });
    }
  }

  /* =========================================================================
   * BARS 16–20 (30–40) PAPER · CHECK → TOOL → REQUEST → RESPONSE → PRIORITY
   * ====================================================================== */

  function bars16to20(ctx, skin, t) {
    const y = 540;
    // the analytical line persists across the whole second paper act;
    // during REQUEST/RESPONSE it IS the dual pair, and it settles below the
    // priority stack as its ground line
    let lineY = y;
    if (t >= 34 && t < 38) lineY = null;   // dual lines only
    else if (t >= 38) {
      const sortP = spring(16, 0.55)(t - 37.95);
      lineY = lerp(540, 838, sortP);
    }
    if (lineY !== null) {
      const lineW = t < 34 ? 5 : 4.4;
      INK.stroke(ctx, skin, GEO.line(222, lineY, 1698, lineY, 80), t,
        { width: lineW, amp: 1.4, freq: 0.06, seed: 400, ghost: true });
    }

    /* BAR 16 — CHECK: verification marks land on the beats */
    if (t < 32) {
      const checks = [
        { x: 380, label: 'OK' }, { x: 620, label: 'VERIFIED' },
        { x: 880, label: 'OK' }, { x: 1140, label: 'VERIFIED' },
        { x: 1420, label: 'OK' }
      ];
      for (let i = 0; i < checks.length; i++) {
        const p = expOut(10, 0.3)(t - (29.95 + i * 0.5));
        if (p <= 0.02) continue;
        const c = checks[i];
        const mark = [
          { x: c.x - 22, y }, { x: c.x - 6, y: y + 20 }, { x: c.x + 28, y: y - 30 }
        ];
        const cut = Math.max(2, Math.floor(3 * p * 1.5));
        INK.stroke(ctx, skin, mark.slice(0, Math.min(3, cut)), t,
          { width: 5, color: skin.accent, amp: 1, seed: 401 + i, ghost: false });
        if (p > 0.6) drawWipedText(ctx, c.label, c.x, y - 52, 24, 700, 0.2, skin.ink, (p - 0.6) / 0.4);
      }
      // inspection ring travelling along the line
      const rx = 222 + ((t - 30) / 2) * 1476;
      INK.stroke(ctx, skin, GEO.circle(rx, y, 46, 40), t,
        { width: 3, color: skin.accent, amp: 1.1, seed: 410, close: true, ghost: false });
    }

    /* BAR 17 — TOOL: pencil-sketch tool chips ride the rail */
    if (t >= 32 && t < 34) {
      const tools = ['HTTP', 'DNS', 'SCANNER', 'PROBE'];
      for (let i = 0; i < tools.length; i++) {
        const p = expOut(8, 0.35)(t - (31.95 + i * 0.18));
        if (p <= 0.02) continue;
        const x = lerp(420 + i * 300, 380 + i * 320, spring(16, 1.2)(t - 31.95 - i * 0.18));
        const box = GEO.roundRect(x - 85, y - 76, 170, 64, 8, 3);
        INK.stroke(ctx, skin, box, t, { width: 3, color: skin.ink, amp: 0.9, seed: 411 + i, close: true, ghost: false });
        if (p > 0.5) drawWipedText(ctx, tools[i], x, y - 36, 24, 700, 0.18, skin.ink, (p - 0.5) * 2);
        INK.stroke(ctx, skin, GEO.line(x, y - 12, x, y, 6), t,
          { width: 2, color: skin.ink, amp: 0.5, seed: 415 + i, ghost: false });
      }
    }

    /* BAR 18 — REQUEST: dual lines, packets travel right */
    if (t >= 34 && t < 38) {
      const splitP = spring(18, 0.4)(t - 33.95);
      const gap = 95 * splitP;
      INK.stroke(ctx, skin, GEO.line(222, y - gap, 1698, y - gap, 64), t,
        { width: 3.6, color: skin.ink, amp: 1.1, seed: 420, ghost: false });
      INK.stroke(ctx, skin, GEO.line(222, y + gap, 1698, y + gap, 64), t,
        { width: 3.6, color: skin.ink, amp: 1.1, seed: 421, ghost: false });
      if (t < 36) drawWipedText(ctx, 'REQUEST', 300, y - gap - 24, 24, 700, 0.22, skin.accent, expOut(9, 0.3)(t - 34.05));
      else drawWipedText(ctx, 'RESPONSE', 300, y + gap + 44, 24, 700, 0.22, skin.accent, expOut(9, 0.3)(t - 35.95));

      for (let i = 0; i < 4; i++) {
        const t0 = 34.15 + i * 0.42;
        const travel = (t - t0) / 1.35;
        if (travel <= 0) continue;
        if (t < 36) {
          if (travel >= 1) continue;
          const x = lerp(320, 1560, travel);
          const box = GEO.roundRect(x - 30, y - gap - 18, 60, 36, 4, 2);
          INK.stroke(ctx, skin, box, t, { width: 2.4, color: skin.ink, amp: 0.6, seed: 422 + i, close: true, ghost: false });
          INK.hatchFill(ctx, skin, box, t, { angle: -60, pitch: 7, alpha: 0.5, seed: 426 + i });
        }
      }
    }

    /* BAR 19 — RESPONSE: packets come home with check stamps */
    if (t >= 36 && t < 38) {
      const splitP = spring(18, 0.4)(t - 33.95);
      const gap = 95 * splitP;
      for (let i = 0; i < 4; i++) {
        const t0 = 36.1 + i * 0.42;
        const travel = (t - t0) / 1.35;
        if (travel <= 0 || travel >= 1) continue;
        const x = lerp(1560, 320, travel);
        const box = GEO.roundRect(x - 30, y + gap - 18, 60, 36, 4, 2);
        INK.stroke(ctx, skin, box, t, { width: 2.4, color: skin.ink, amp: 0.6, seed: 430 + i, close: true, ghost: false });
        INK.hatchFill(ctx, skin, box, t, { angle: 60, pitch: 7, alpha: 0.5, seed: 434 + i });
        if (travel > 0.55) {
          const cp = expOut(10, 0.25)(t - (t0 + 0.8));
          if (cp > 0.1) {
            const mark = [{ x: x - 12, y: y + gap }, { x: x - 2, y: y + gap + 10 }, { x: x + 16, y: y + gap - 12 }];
            INK.stroke(ctx, skin, mark.slice(0, Math.ceil(3 * cp)), t,
              { width: 3.4, color: skin.accent, amp: 0.6, seed: 438 + i, ghost: false });
          }
        }
      }
    }

    /* BAR 20 — PRIORITY: packets sort into a vertical risk stack */
    if (t >= 38 && t < 39.98) {
      const sortP = spring(16, 0.55)(t - 37.95);
      const risks = [95, 87, 73, 64, 52, 41];
      for (let i = 0; i < 6; i++) {
        const p = expOut(8, 0.3)(t - 37.95 - i * 0.08);
        if (p <= 0.02) continue;
        const x = lerp(560 + i * 130, 880, sortP);
        const yy = lerp(540, 300 + i * 88, sortP);
        const w = lerp(120, 300, sortP);
        const box = GEO.roundRect(x - 60, yy - 24, w, 48, 6, 2);
        INK.stroke(ctx, skin, box, t, { width: 2.8, color: skin.ink, amp: 0.7, seed: 440 + i, close: true, ghost: false });
        if (sortP > 0.4) {
          rollingNum(ctx, risks[i], x - 40, yy + 8, 24, 700, 0.1, skin.accent, t, 'left', 2);
          drawWipedText(ctx, 'RISK', x + 130, yy + 8, 20, 700, 0.22, skin.ink, sortP);
        }
      }
    }
  }

  /* =========================================================================
   * BARS 21–24 (40–48) BLUEPRINT · LOG → LIGHT → WAVE → GLOBE
   * ====================================================================== */

  function bars21to24(ctx, skin, t) {
    /* BAR 21 — LOG */
    if (t < 42) {
      const y = 800;
      // priority stack from bar 20 slides into the log lines (morph, no cut)
      const handoff = spring(18, 0.5)(t - 39.95);
      if (handoff < 0.98) {
        for (let i = 0; i < 6; i++) {
          const bx = lerp(880, 260, handoff);
          const by = lerp(300 + i * 88, 330 + i * 70, handoff);
          const bw = lerp(300, 40, handoff);
          const bh = lerp(48, 8, handoff);
          INK.stroke(ctx, skin, GEO.roundRect(bx - bw / 2 + 150, by - bh / 2, bw, bh, 4, 2), t,
            { width: 2.4, color: skin.ink, amp: 0.6, seed: 440 + i, close: true, ghost: false });
        }
      }
      INK.stroke(ctx, skin, GEO.line(222, y, 1698, y, 80), t,
        { width: 4, color: skin.ink, amp: 1, seed: 450, ghost: false });
      const box = GEO.roundRect(222, 220, 1476, 520, 8, 3);
      INK.stroke(ctx, skin, box, t, { width: 3, color: skin.ink, amp: 0.8, seed: 451, close: true, ghost: false });
      drawWipedText(ctx, 'SYSTEM LOG', 260, 262, 22, 700, 0.24, skin.accent, expOut(9, 0.3)(t - 39.95), 'left');
      for (let i = 0; i < 6; i++) {
        const typed = expOut(14, 0.4)(t - (40.15 + i * 0.32));
        if (typed <= 0.01) continue;
        drawWipedText(ctx, LOG_LINES[i], 260, 330 + i * 70, 21, 600, 0.08, skin.ink, typed, 'left');
      }
    }

    /* BAR 22 — LIGHT: ray construction, accent shifts toward cyan */
    if (t >= 42 && t < 44) {
      const y = 540;
      INK.stroke(ctx, skin, GEO.line(222, y, 1698, y, 80), t,
        { width: 4, color: skin.ink, amp: 1, seed: 452, ghost: false });
      // emitter node
      const emitP = expOut(8, 0.3)(t - 41.95);
      INK.stroke(ctx, skin, GEO.circle(420, y, 22 * emitP, 28), t,
        { width: 3, color: skin.ink, amp: 0.5, seed: 453, close: true, ghost: false });
      // rays fan out and reflect off a mirror line
      for (let i = 0; i < 5; i++) {
        const p = expOut(7, 0.4)(t - (42.05 + i * 0.14));
        if (p <= 0.02) continue;
        const a = rad(-42 + i * 21);
        const len = 620 * p;
        const ex = 420 + Math.cos(a) * len;
        const ey = y + Math.sin(a) * len;
        const col = i % 2 === 0 ? skin.ink : skin.accent;
        INK.stroke(ctx, skin, GEO.line(420, y, ex, ey, 32), t,
          { width: 2.6, color: col, amp: 0.6, seed: 454 + i, ghost: false });
        // angle tick
        INK.stroke(ctx, skin, GEO.circle(420, y, 46 + i * 0.001, 24, a - 0.22, a + 0.22), t,
          { width: 1.8, color: skin.accent, amp: 0.3, seed: 459 + i, ghost: false });
      }
      drawWipedText(ctx, 'θ', 480, y - 70, 26, 700, 0.1, skin.accent, expOut(9, 0.3)(t - 42.3));
      // mirror line
      const mp = expOut(8, 0.3)(t - 42.45);
      if (mp > 0.02) {
        INK.stroke(ctx, skin, GEO.line(1240, 240, 1240, 840, 32), t,
          { width: 3, color: skin.ink, amp: 0.8, seed: 461, ghost: false });
      }
    }

    /* BAR 23 — WAVE */
    if (t >= 44 && t < 46) {
      for (let k = 0; k < 4; k++) {
        const p = expOut(7, 0.4)(t - (43.95 + k * 0.12));
        if (p <= 0.02) continue;
        const amp = (46 + k * 18) * p;
        const pts = GEO.wave(222, 1698, 540 + (k - 1.5) * 70, amp, 2.75 + k * 0.5, t * 2.6 + k * 1.2, 120);
        INK.stroke(ctx, skin, pts, t, {
          width: 3.4 - k * 0.35, color: k % 2 ? skin.accent : skin.ink,
          amp: 0.8, seed: 462 + k, ghost: false
        });
      }
    }

    /* BAR 24 — GLOBE */
    if (t >= 46) {
      const form = spring(15, 0.55)(t - 45.95);
      const rings = GEO.globe(CX, 540, 290 * form, 5, 8, t * 0.3, 0.35);
      for (let i = 0; i < rings.length; i++) {
        const ring = rings[i];
        const pts = ring.pts;
        const warped = pts.map((p, k) => {
          const wy = 540 + (k % 2 === 0 ? -1 : 1) * (1 - form) * 130;
          return { x: lerp(p.x, p.x, 1) , y: lerp(wy, p.y, form) };
        });
        INK.stroke(ctx, skin, warped, t, {
          width: ring.kind === 'lat' ? 3.2 : 2.4, color: skin.ink,
          amp: 0.5, seed: 470 + i, ghost: false
        });
      }
    }
  }

  /* =========================================================================
   * BARS 25–28 (48–56.4) HOLOGRAM · PIPELINE → AUDIT → VERIFY → BOUNDARY
   * ====================================================================== */

  function bars25to28(ctx, skin, t) {
    /* BAR 25 — PIPELINE (cyan + white core) */
    if (t < 50) {
      const y = 540, x1 = 300, x2 = 1620;
      const shapeP = spring(16, 0.5)(t - 47.95);
      // globe remnant folds into the pipeline spine
      const spine = GEO.line(x1, y, x2, y, 64);
      const rings = GEO.globe(CX, 540, 290 * (1 - shapeP), 5, 8, t * 0.3, 0.35);
      const mid = rings.length ? GEO.resample(rings[0].pts, 64) : spine;
      INK.stroke(ctx, skin, GEO.morph(GEO.resample(mid, 64), spine, shapeP), t,
        { width: 3.6, color: skin.ink, amp: 0.7, seed: 480, ghost: false });

      for (let i = 0; i < 5; i++) {
        const p = expOut(8, 0.3)(t - (48.05 + i * 0.13));
        if (p <= 0.02) continue;
        const x = lerp(x1 + 100, x2 - 100, i / 4);
        const box = GEO.roundRect(x - 78, y - 34, 156, 68, 8, 3);
        const cut = Math.max(3, Math.floor(box.length * p));
        INK.stroke(ctx, skin, box.slice(0, cut), t,
          { width: 2.8, color: skin.ink, amp: 0.5, seed: 481 + i, close: true, ghost: false });
        if (p > 0.5) drawWipedText(ctx, STAGES_5[i], x, y + 8, 24, 700, 0.16, skin.ink, (p - 0.5) * 2);
      }
    }

    /* BAR 26 — AUDIT: mascot runs the audit trail */
    if (t >= 50 && t < 52) {
      const y = 620;
      INK.stroke(ctx, skin, GEO.line(222, y, 1698, y, 80), t,
        { width: 3.2, color: skin.ink, amp: 0.9, seed: 482, ghost: false });
      for (let i = 0; i < 6; i++) {
        const p = expOut(9, 0.3)(t - (49.95 + i * 0.16));
        if (p <= 0.02) continue;
        const x = 380 + i * 220;
        INK.stroke(ctx, skin, GEO.circle(x, y, 8 * p, 16), t,
          { width: 2.4, color: skin.highlight, amp: 0.3, seed: 483 + i, close: true, ghost: false });
      }
      const runP = expOut(5, 1.4)(t - 49.95);
      const mx = lerp(300, 1500, runP);
      MASCOT.draw(ctx, mx, y - 10, t, { skin, scale: 1, running: 1, lean: 7 });
      drawWipedText(ctx, 'AUDIT TRAIL', 260, y - 80, 26, 700, 0.22, skin.highlight, expOut(9, 0.3)(t - 50.05), 'left');
    }

    /* BAR 27 — VERIFY: rings + status */
    if (t >= 52 && t < 54) {
      const nodes = [
        { x: 560, y: 540 }, { x: 960, y: 420 }, { x: 1360, y: 540 }
      ];
      for (let i = 0; i < 3; i++) {
        const n = nodes[i];
        INK.stroke(ctx, skin, GEO.circle(n.x, n.y, 34, 40), t,
          { width: 3, color: skin.ink, amp: 0.5, seed: 490 + i, close: true, ghost: false });
        // concentric verification rings (density = light, no glow)
        for (let k = 1; k <= 3; k++) {
          const p = expOut(8, 0.45)(t - (51.95 + i * 0.14 + k * 0.18));
          if (p <= 0.03) continue;
          const r = lerp(34, 34 + k * 46, p);
          INK.beam(ctx, GEO.circle(n.x, n.y, r, 48), {
            width: 2.2, color: skin.ink, core: C.white,
            alpha: 1 - k * 0.22, coreAlpha: 1 - k * 0.2
          });
        }
        const cp = expOut(10, 0.35)(t - (52.35 + i * 0.22));
        if (cp > 0.05) {
          const mark = [{ x: n.x - 18, y: n.y + 2 }, { x: n.x - 4, y: n.y + 16 }, { x: n.x + 22, y: n.y - 18 }];
          INK.stroke(ctx, skin, mark.slice(0, Math.ceil(3 * cp)), t,
            { width: 4.6, color: skin.highlight, amp: 0.7, seed: 500 + i, ghost: false });
        }
      }
      const labels = ['REACHED', 'CONFIRMED', 'LOGGED', 'BOUND'];
      for (let i = 0; i < 4; i++) {
        const p = expOut(10, 0.3)(t - (52.55 + i * 0.16));
        if (p <= 0.02) continue;
        drawWipedText(ctx, labels[i], 430 + i * 300, 880, 26, 700, 0.22, skin.highlight, p);
      }
    }

    /* BAR 28 — BOUNDARY: the line traces the safe-margin frame */
    if (t >= 54 && t < 55.98) {
      const traceP = expOut(6, 0.7)(t - 53.95);
      const frame = GEO.roundRect(MX, MY, W - MX * 2, H - MY * 2, 0, 1);
      const cut = Math.max(2, Math.floor(frame.length * traceP));
      INK.stroke(ctx, skin, frame.slice(0, cut), t,
        { width: 3.2, color: skin.ink, amp: 0.8, seed: 510, close: false, ghost: false });
      // dimension marks for the safe margins
      const dimP = expOut(8, 0.35)(t - 54.35);
      if (dimP > 0.02) {
        drawWipedText(ctx, '150', MX / 2, 560, 24, 700, 0.16, skin.highlight, dimP);
        drawWipedText(ctx, '150', W - MX / 2, 560, 24, 700, 0.16, skin.highlight, dimP);
        drawWipedText(ctx, '90', CX, MY / 2 + 8, 24, 700, 0.16, skin.highlight, dimP);
        drawWipedText(ctx, '90', CX, H - MY / 2 + 8, 24, 700, 0.16, skin.highlight, dimP);
      }
      // corner ticks
      for (const [cx, cy] of [[MX, MY], [W - MX, MY], [MX, H - MY], [W - MX, H - MY]]) {
        const tp = expOut(9, 0.3)(t - 54.15);
        if (tp <= 0.02) continue;
        INK.stroke(ctx, skin, [
          { x: cx - 18 * tp, y: cy }, { x: cx + 18 * tp, y: cy }
        ], t, { width: 3, color: skin.highlight, amp: 0.4, seed: 511, ghost: false });
        INK.stroke(ctx, skin, [
          { x: cx, y: cy - 18 * tp }, { x: cx, y: cy + 18 * tp }
        ], t, { width: 3, color: skin.highlight, amp: 0.4, seed: 512, ghost: false });
      }
    }
  }

  /* =========================================================================
   * BARS 29–30 (56–60) LOOP → END — the line returns to the headline rail.
   * ====================================================================== */

  function bars29to30(ctx, skin, t) {
    /* LOOP: the line coils, then unravels toward the frame-0 rail */
    const unravel = spring(16, 1.2)(t - 56.35);
    const toHero = clamp((t - 57.2) / 1.2, 0, 1);

    if (t < 58.05) {
      // coil: two looping bezier curls around the centre, shrinking away
      const coilAmt = 1 - unravel;
      for (let k = 0; k < 2; k++) {
        const cx = 960, cy = 540;
        const r = lerp(120, 30, unravel);
        const loops = GEO.circle(cx - 140 + k * 280, cy, r, 64);
        INK.stroke(ctx, skin, loops, t, {
          width: 4.4, color: skin.ink, amp: 1.4, freq: 0.08, seed: 520 + k,
          close: true, ghost: true
        });
      }
      // connecting sweeps of the single line
      const sweep = GEO.bezier(
        { x: 222, y: 540 }, { x: 620, y: 420 }, { x: 1300, y: 660 }, { x: 1698, y: 540 }, 80
      );
      const morphed = GEO.morph(GEO.resample(sweep, 80),
        GEO.resample(GEO.line(RAIL0.x1, RAIL0.y, RAIL0.x2, RAIL0.y, 80), 80), clamp(toHero * 1.25, 0, 1));
      INK.stroke(ctx, skin, morphed, t, { width: 4.8, amp: 1.5, freq: 0.06, seed: 522, ghost: true });
    }

    /* END: hero composition assembles and lands exactly on frame 0 at t = 60 */
    if (t >= 57.0) {
      const P = heroParams(t);
      // headline wipe-reveal as the line sweeps over the letter zone
      const reveal = expOut(7, 0.75)(t - 57.15);
      if (reveal > 0.01) {
        ctx.save();
        ctx.beginPath();
        ctx.rect(CX - 760, HEAD.top - 80, 1520 * reveal, HEAD_BASE - HEAD.top + 160);
        ctx.clip();
        const sy = lerp(0.55, 1, spring(22, 0.9)(t - 57.4));
        INK.hatchedText(ctx, skin, 'ARTEX', CX, HEAD_BASE, HEAD.size, 900, t, {
          trackingEm: 0.012, align: 'center', hatchAngle: -48, hatchPitch: 15,
          outlineW: 7 * spring(20, 1.1)(t - 58.1), flatScaleY: sy
        });
        ctx.restore();
      }
      // subtexts draw back in
      const subP = expOut(8, 0.8)(t - 58.25);
      const subP2 = expOut(8, 0.8)(t - 58.45);
      if (subP > 0.01) drawWipedText(ctx, SUB_TOP, CX, HEAD.top - 42, 26, 700, 0.22, skin.hud, subP);
      if (subP2 > 0.01) drawWipedText(ctx, SUB_BOT, CX, HEAD_BASE + 72, 26, 700, 0.22, skin.hud, subP2);

      // rail already morphing to RAIL0 (drawn above); mascot runs in from the left
      const runIn = expOut(4.5, 1.6)(t - 57.6);
      const stop = spring(18, 0.7)(t - 58.9);
      const mx = lerp(240, MASCOT0_X, runIn);
      const running = clamp(1 - stop, 0, 1) * (runIn > 0.02 ? 1 : 0);
      if (runIn > 0.02 && t < 59.995) {
        MASCOT.draw(ctx, mx, RAIL0.y - 2, t, {
          skin, scale: 1, running, lean: 7 * running
        });
      }
      const lblP = expOut(8, 0.6)(t - 58.7);
      if (lblP > 0.01) drawWipedText(ctx, LABEL, mx - 92, RAIL0.y - 64, 24, 700, 0.2, skin.accent, lblP, 'right');

      // ruler ticks / dimension machinery is gone by 59.2; hatching settles
      // exactly on the frame-0 state at 60.0
    }
  }

  /* =========================================================================
   * drawWorld — one stage's complete frame (bg, grid, content, HUD).
   * ====================================================================== */

  function drawWorld(ctx, skinId, t) {
    const skin = INK.SKIN[skinId];

    // background
    ctx.fillStyle = skin.bg;
    ctx.fillRect(0, 0, W, H);

    // grid + paper grain (grain laid down before strokes — masked from ink)
    INK.grid(ctx, skin);
    if (skin.id === 'paper') INK.grain(ctx, t);

    // content by time segment
    if (t < 2.0) {
      drawHero(ctx, skin, t);
      drawTicksAndDim(ctx, skin, t);
    } else if (t < 4.0) {
      bar2(ctx, skin, t);
    } else if (t < 6.0) {
      bar3(ctx, skin, t);
    } else if (t < 8.0) {
      bar4(ctx, skin, t);
    } else if (t < 10.0) {
      bar5(ctx, skin, t);
    } else if (t < 12.0) {
      bar6(ctx, skin, t);
    } else if (t < 14.0) {
      bar7(ctx, skin, t);
    } else if (t < 16.0) {
      bar8(ctx, skin, t);
      if (t >= 15.0) bar9(ctx, skin, t);   // graph line joins during panel populate
    } else if (t < 18.0) {
      bar8(ctx, skin, t);
      bar9(ctx, skin, t);
    } else if (t < 20.0) {
      bar10(ctx, skin, t);
    } else if (t < 22.0) {
      bar11(ctx, skin, t);
    } else if (t < 24.0) {
      bar12(ctx, skin, t);
    } else if (t < 26.0) {
      bar13(ctx, skin, t);
    } else if (t < 28.0) {
      bar14(ctx, skin, t);
    } else if (t < 30.0) {
      bar15(ctx, skin, t);
    } else if (t < 40.0) {
      bars16to20(ctx, skin, t);
    } else if (t < 48.0) {
      bars21to24(ctx, skin, t);
    } else if (t < 56.4) {
      bars25to28(ctx, skin, t);
    } else {
      // 56.4–60: hero reconstruction (line morphs to frame-0 rail)
      bars29to30(ctx, skin, t);
    }

    // HUD labels — visible for the entire 60 s
    hud(ctx, skin, t);
  }

  return { measureHead, drawWorld, skinAt, transitionAt, HEAD: () => HEAD };
})();
