/* =============================================================================
 * render.js — shared frame compositor (used by the player and the exporter).
 * Transitions are hard-edged only: shape flood (paper→blueprint) and a single
 * horizontal scan line (blueprint↔hologram). No cuts, no dissolves, no glow.
 * ========================================================================== */

const RENDER = (() => {
  'use strict';
  const { W, H } = HF.STAGE;
  const C = HF.COLOR;

  /** Exponential wipe progress (fast onset, hard edge). */
  function expWipe(p) {
    const k = 7;
    return (1 - Math.exp(-k * p)) / (1 - Math.exp(-k));
  }

  function frame(ctx, tRaw) {
    const t = ((tRaw % HF.DURATION) + HF.DURATION) % HF.DURATION;
    const tr = SCENES.transitionAt(t);

    if (!tr) {
      SCENES.drawWorld(ctx, SCENES.skinAt(t), t);
      return;
    }

    const p = HF.clamp((t - tr.t0) / tr.D, 0, 1);
    const eased = expWipe(p);

    if (tr.type === 'flood') {
      // old stage full frame; new stage revealed inside a hard-edged shape
      SCENES.drawWorld(ctx, tr.from, t);
      ctx.save();
      if (tr.shape === 'hex') {
        const r = HF.lerp(96, 1500, eased);
        const hex = GEO.polygon(960, 540, r, 6, true);
        INK.pathTo(ctx, hex, true);
      } else {
        const w = HF.lerp(300, 2400, eased);
        const h = HF.lerp(120, 1500, eased);
        ctx.beginPath();
        ctx.rect(960 - w / 2, 540 - h / 2, w, h);
      }
      ctx.clip();
      SCENES.drawWorld(ctx, tr.to, t);
      ctx.restore();
    } else {
      // single horizontal scan line sweeping the frame
      const lineY = tr.dir === 'down'
        ? HF.lerp(-12, H + 12, eased)
        : HF.lerp(H + 12, -12, eased);

      const top = tr.dir === 'down' ? tr.to : tr.from;
      const bot = tr.dir === 'down' ? tr.from : tr.to;

      ctx.save();
      ctx.beginPath();
      ctx.rect(0, 0, W, lineY);
      ctx.clip();
      SCENES.drawWorld(ctx, top, t);
      ctx.restore();

      ctx.save();
      ctx.beginPath();
      ctx.rect(0, lineY, W, H - lineY);
      ctx.clip();
      SCENES.drawWorld(ctx, bot, t);
      ctx.restore();

      // the scan line itself: hard, thin — light is never blur
      const ink = tr.to === 'hologram' ? C.cyan : C.white;
      ctx.fillStyle = ink;
      ctx.fillRect(0, lineY - 2, W, 3);
      ctx.fillStyle = tr.from === 'hologram' ? C.cyan : C.white;
      ctx.globalAlpha = 0.55;
      ctx.fillRect(0, lineY + 3, W, 1);
      ctx.globalAlpha = 1;
    }
  }

  return { frame };
})();
