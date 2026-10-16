/* =============================================================================
 * mascot.js — creative-director box character.
 * Body 76×54 rounded rect · arm stubs 24×16 · four legs 11×33 · eyes 8×14.
 * Right arm raised 58°, holding a drafting pencil (~250 px overall width).
 * Paper: graphite outline, sparse blue hatching, graphite eyes.
 * Hologram: cyan outline, scan-line fill, solid white eyes. Never a 4th colour.
 * Run cycle: alternate leg pairs lift, 6 steps/s, body leans 7° into motion.
 * ========================================================================== */

const MASCOT = (() => {
  'use strict';
  const { rad, lerp, clamp, boilStep, vnoise } = HF;
  const { roundRect, line } = GEO;

  const PARTS = {
    bodyW: 76, bodyH: 54,
    armW: 24, armH: 16,
    legW: 11, legH: 33,
    eyeW: 8, eyeH: 14
  };

  /**
   * Draw the mascot with feet origin at (x, y).
   * opts: skin, scale, t, running (0..1 speed factor), dir (+1 right),
   *       lean (deg, usually ±7), armLift (rad), pencilDown (0..1 tap),
   *       alpha
   */
  function draw(ctx, x, y, t, opts = {}) {
    const {
      skin = INK.SKIN.paper, scale = 1, running = 0, dir = 1,
      lean = 0, alpha = 1, tap = 0
    } = opts;

    const paper = skin.id === 'paper';
    const holo = skin.id === 'hologram';
    const inkC = paper ? HF.COLOR.graphite : holo ? HF.COLOR.cyan : HF.COLOR.white;
    const fillC = paper ? HF.COLOR.blue : holo ? HF.COLOR.cyan : HF.COLOR.white;
    const eyeC = paper ? HF.COLOR.graphite : HF.COLOR.white;

    ctx.save();
    ctx.globalAlpha = alpha;
    ctx.translate(x, y);
    ctx.scale(scale * dir, scale);
    if (lean) ctx.rotate(rad(lean));

    /* ---- legs (4 straight legs, alternate pairs) ---- */
    const stepHz = 6;
    const phase = t * stepHz * Math.PI;
    const lift = running * 9;
    // pair A = legs 0,2 · pair B = legs 1,3
    const legX = [-26, -12, 12, 26];
    for (let i = 0; i < 4; i++) {
      const pair = i % 2;
      const sw = Math.sin(phase + pair * Math.PI);
      const ly = -Math.max(0, sw) * lift;      // lift up
      const lx = Math.sin(phase + pair * Math.PI + Math.PI / 2) * lift * 0.55;
      const leg = roundRect(legX[i] - PARTS.legW / 2 + lx, -PARTS.legH + ly, PARTS.legW, PARTS.legH, 3, 3);
      fillHatch(leg);
    }

    /* ---- arm stubs ---- */
    // left arm (down, static-ish)
    const lArm = roundRect(-PARTS.bodyW / 2 - PARTS.armW + 8, -PARTS.bodyH * 0.62,
      PARTS.armW, PARTS.armH, 6, 4);
    fillHatch(lArm);

    // right arm raised 58° (upward), tap lowers it slightly toward a node
    const armAngle = rad(58) - tap * rad(26);
    ctx.save();
    ctx.translate(PARTS.bodyW / 2 - 10, -PARTS.bodyH * 0.55);
    ctx.rotate(-armAngle);
    const rArm = roundRect(0, -PARTS.armH / 2, PARTS.armW + 6, PARTS.armH, 6, 4);
    fillHatch(rArm);

    /* ---- drafting pencil held in raised hand (overall width ≈ 250 px) ---- */
    const pLen = 170;
    ctx.translate(PARTS.armW + 2, 0);
    ctx.rotate(rad(32 - tap * 18));
    const pencilBody = roundRect(0, -6, pLen * 0.72, 12, 3, 2);
    fillHatch(pencilBody);
    // graphite core tip
    const tip = [
      { x: pLen * 0.72, y: -6 }, { x: pLen * 0.86, y: -2.2 },
      { x: pLen, y: 0 }, { x: pLen * 0.86, y: 2.2 }, { x: pLen * 0.72, y: 6 }
    ];
    drawInk(tip, true, 2.4);
    ctx.restore();

    /* ---- body ---- */
    const body = roundRect(-PARTS.bodyW / 2, -PARTS.bodyH, PARTS.bodyW, PARTS.bodyH, 10, 5);
    fillHatch(body);

    /* ---- eyes: two tall rounded rects ---- */
    for (const ex of [-12, 10]) {
      const eye = roundRect(ex, -PARTS.bodyH + 13, PARTS.eyeW, PARTS.eyeH, 3, 3);
      if (paper) {
        INK.pencil(ctx, eye, t, { width: 2.6, color: eyeC, amp: 0.5, freq: 0.2, seed: 47, ghost: false });
      } else {
        ctx.save();
        INK.pathTo(ctx, eye, true);
        ctx.fillStyle = eyeC;
        ctx.fill();
        ctx.restore();
      }
    }

    ctx.restore();

    /* ---- helpers ---- */
    function drawInk(pts, close, w) {
      if (paper) {
        INK.pencil(ctx, pts, t, { width: w ?? 3, color: inkC, amp: 1.1, freq: 0.16, seed: 41 });
      } else if (holo) {
        INK.beam(ctx, pts, { width: (w ?? 3) * 1.15, color: inkC, core: HF.COLOR.white, close });
      } else {
        INK.wire(ctx, pts, { width: w ?? 3, color: inkC, close });
      }
    }

    function fillHatch(pts) {
      // body fill: sparse blue hatching on paper · scan-line fill on holo
      if (paper) {
        INK.hatchFill(ctx, skin, pts, t, {
          angle: -52, pitch: 9, color: fillC, alpha: 0.34, seed: 61, close: true
        });
      } else if (holo) {
        INK.hatchFill(ctx, skin, pts, t, {
          color: fillC, alpha: 0.55, seed: 61,
          lighting: (u) => 0.9 + u * 1.7, close: true
        });
      } else {
        INK.hatchFill(ctx, skin, pts, t, {
          angle: -52, pitch: 7, color: fillC, alpha: 0.28, seed: 61, close: true
        });
      }
      drawInk(pts, true, 3);
    }
  }

  return { draw, PARTS };
})();
