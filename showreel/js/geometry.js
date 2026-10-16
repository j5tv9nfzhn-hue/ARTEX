/* =============================================================================
 * geometry.js — vector primitives for the showreel.
 * Everything is plain point-lists so shapes can morph, resample and ink freely.
 * ========================================================================== */

const GEO = (() => {
  'use strict';
  const { lerp, clamp, TAU, rad } = HF;

  /* ---- points & polylines ----------------------------------------------- */

  /** Uniform resampling of a polyline to n points (arc-length parameterised). */
  function resample(pts, n) {
    if (pts.length < 2) return new Array(n).fill(pts[0] || { x: 0, y: 0 });
    const segLen = [];
    let total = 0;
    for (let i = 1; i < pts.length; i++) {
      const d = Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y);
      segLen.push(d);
      total += d;
    }
    const out = [];
    let si = 0, acc = 0;
    for (let k = 0; k < n; k++) {
      const target = (k / (n - 1)) * total;
      while (si < segLen.length - 1 && acc + segLen[si] < target) {
        acc += segLen[si];
        si++;
      }
      const p = segLen[si] > 0 ? (target - acc) / segLen[si] : 0;
      out.push({
        x: lerp(pts[si].x, pts[si + 1].x, p),
        y: lerp(pts[si].y, pts[si + 1].y, p)
      });
    }
    return out;
  }

  /** Point-wise morph between two equal-length point arrays. */
  function morph(a, b, p) {
    const n = Math.min(a.length, b.length);
    const out = [];
    for (let i = 0; i < n; i++) {
      out.push({ x: lerp(a[i].x, b[i].x, p), y: lerp(a[i].y, b[i].y, p) });
    }
    return out;
  }

  /** Rotate points around a centre. */
  function rotate(pts, cx, cy, angle) {
    const c = Math.cos(angle), s = Math.sin(angle);
    return pts.map((p) => {
      const x = p.x - cx, y = p.y - cy;
      return { x: cx + x * c - y * s, y: cy + x * s + y * c };
    });
  }

  /** Translate points. */
  function translate(pts, dx, dy) {
    return pts.map((p) => ({ x: p.x + dx, y: p.y + dy }));
  }

  /** Scale points about a centre. */
  function scale(pts, cx, cy, sx, sy = sx) {
    return pts.map((p) => ({ x: cx + (p.x - cx) * sx, y: cy + (p.y - cy) * sy }));
  }

  /** Total polyline length. */
  function length(pts) {
    let L = 0;
    for (let i = 1; i < pts.length; i++) {
      L += Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y);
    }
    return L;
  }

  /* ---- shape constructors ------------------------------------------------ */

  /** Straight line as a resampled point list. */
  function line(x1, y1, x2, y2, n = 64) {
    const pts = [];
    for (let i = 0; i < n; i++) {
      const p = i / (n - 1);
      pts.push({ x: lerp(x1, x2, p), y: lerp(y1, y2, p) });
    }
    return pts;
  }

  /** Circle as closed polyline (last point = first). */
  function circle(cx, cy, r, n = 96, arcFrom = 0, arcTo = TAU) {
    const pts = [];
    for (let i = 0; i < n; i++) {
      const a = lerp(arcFrom, arcTo, i / (n - 1));
      pts.push({ x: cx + Math.cos(a) * r, y: cy + Math.sin(a) * r });
    }
    return pts;
  }

  /** Regular polygon; pointUp=true puts a vertex at the top. */
  function polygon(cx, cy, r, sides, pointUp = false, phase = 0) {
    const pts = [];
    const off = pointUp ? -Math.PI / 2 : -Math.PI / 2 + Math.PI / sides;
    for (let i = 0; i <= sides; i++) {
      const a = off + (i / sides) * TAU + phase;
      pts.push({ x: cx + Math.cos(a) * r, y: cy + Math.sin(a) * r });
    }
    return pts;
  }

  /** Rounded rectangle as closed polyline. */
  function roundRect(x, y, w, h, r, n = 12) {
    r = Math.min(r, w / 2, h / 2);
    const pts = [];
    const arc = (cx, cy, a0, a1) => {
      for (let i = 0; i <= n; i++) {
        const a = lerp(a0, a1, i / n);
        pts.push({ x: cx + Math.cos(a) * r, y: cy + Math.sin(a) * r });
      }
    };
    arc(x + w - r, y + r, -Math.PI / 2, 0);
    arc(x + w - r, y + h - r, 0, Math.PI / 2);
    arc(x + r, y + h - r, Math.PI / 2, Math.PI);
    arc(x + r, y + r, Math.PI, Math.PI * 1.5);
    pts.push(pts[0]);
    return pts;
  }

  /**
   * Travelling sine wave along a horizontal span.
   * amplitude A, cycles c, per-sample phase φ.
   */
  function wave(x1, x2, y, amp, cycles, phase = 0, n = 128) {
    const pts = [];
    for (let i = 0; i < n; i++) {
      const p = i / (n - 1);
      pts.push({
        x: lerp(x1, x2, p),
        y: y + Math.sin(p * TAU * cycles + phase) * amp
      });
    }
    return pts;
  }

  /** Cubic bezier sampled to points. */
  function bezier(p0, p1, p2, p3, n = 64) {
    const pts = [];
    for (let i = 0; i < n; i++) {
      const t = i / (n - 1), u = 1 - t;
      pts.push({
        x: u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x,
        y: u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y
      });
    }
    return pts;
  }

  /** S-curve (two mirrored beziers) — the pen-tool "EASE" shape. */
  function sCurve(x1, y1, x2, y2, bow, n = 96) {
    const mx = (x1 + x2) / 2, my = (y1 + y2) / 2;
    const a = bezier({ x: x1, y: y1 }, { x: x1 + (mx - x1) * 0.55, y: y1 - bow },
      { x: mx - (mx - x1) * 0.15, y: my + bow * 0.2 }, { x: mx, y: my }, Math.ceil(n / 2));
    const b = bezier({ x: mx, y: my }, { x: mx + (x2 - mx) * 0.15, y: my - bow * 0.2 },
      { x: x2 - (x2 - mx) * 0.55, y: y2 + bow }, { x: x2, y: y2 }, Math.ceil(n / 2));
    return a.concat(b.slice(1));
  }

  /** Trapezoidal-tooth gear profile. */
  function gear(cx, cy, teeth, pitchR, toothH, phase = 0) {
    const pts = [];
    const rootR = pitchR - toothH * 0.55;
    const tipR = pitchR + toothH * 0.45;
    const step = TAU / teeth;
    for (let i = 0; i < teeth; i++) {
      const a0 = i * step + phase;
      const gap = step * 0.22, mid = step * 0.16;
      pts.push({ x: cx + Math.cos(a0) * rootR, y: cy + Math.sin(a0) * rootR });
      pts.push({ x: cx + Math.cos(a0 + gap) * tipR, y: cy + Math.sin(a0 + gap) * tipR });
      pts.push({ x: cx + Math.cos(a0 + gap + mid) * tipR, y: cy + Math.sin(a0 + gap + mid) * tipR });
      pts.push({ x: cx + Math.cos(a0 + gap + mid + gap) * rootR, y: cy + Math.sin(a0 + gap + mid + gap) * rootR });
    }
    pts.push(pts[0]);
    return pts;
  }

  /** Hexagonal honeycomb centres: ring 0 (centre), ring 1 (6), ring 2 (12) — 19 cells. */
  function honeycomb(cx, cy, r) {
    const out = [{ x: cx, y: cy, ring: 0 }];
    // axial coordinates (q, rr), cell centre = r·√3·(q + rr/2), r·1.5·rr
    for (let q = -2; q <= 2; q++) {
      for (let rr = -2; rr <= 2; rr++) {
        const s = -q - rr;
        const ring = Math.max(Math.abs(q), Math.abs(rr), Math.abs(s));
        if (ring === 0 || ring > 2) continue;
        out.push({
          x: cx + r * Math.sqrt(3) * (q + rr / 2),
          y: cy + r * 1.5 * rr,
          ring
        });
      }
    }
    return out;
  }

  /**
   * Isometric cube edges from a hex silhouette.
   * visible: 3 outer silhouette + 3 inner "Y"; hidden: 3 back edges (dashed).
   * Rotation `spin` rotates the inner spoke so the cube appears to turn.
   */
  function cubeEdges(cx, cy, r, spin = 0) {
    const hex = polygon(cx, cy, r, 6, true);
    hex.pop(); // 6 vertices
    // vertex angles: −90°, −30°, 30°, 90°, 150°, 210° (point-up)
    // Silhouette = hexagon; visible inner spokes from centre to vertices 0,2,4.
    const c = { x: cx, y: cy };
    const vis = [
      [hex[0], hex[1]], [hex[1], hex[2]], [hex[2], hex[3]],
      [hex[3], hex[4]], [hex[4], hex[5]], [hex[5], hex[0]]
    ];
    const spokes = [
      [c, hex[0]], [c, hex[2]], [c, hex[4]]
    ];
    const back = [
      [c, hex[1]], [c, hex[3]], [c, hex[5]]
    ];
    return { vis, spokes, back, hex };
  }

  /** Orthographic wireframe globe: latitude + longitude polylines. */
  function globe(cx, cy, r, latLines = 5, lonLines = 8, spin = 0, tilt = 0.35) {
    const rings = [];
    const ct = Math.cos(tilt), st = Math.sin(tilt);
    const project = (lon, lat) => {
      const x3 = Math.cos(lat) * Math.cos(lon + spin);
      const y3 = Math.sin(lat);
      const z3 = Math.cos(lat) * Math.sin(lon + spin);
      const y2 = y3 * ct - z3 * st;
      return { x: cx + x3 * r, y: cy + y2 * r, z: y3 * st + z3 * ct };
    };
    // latitudes
    for (let i = 1; i < latLines; i++) {
      const lat = -Math.PI / 2 + (i / latLines) * Math.PI;
      const pts = [];
      for (let k = 0; k <= 72; k++) {
        pts.push(project((k / 72) * TAU, lat));
      }
      rings.push({ pts, kind: 'lat' });
    }
    // longitudes
    for (let i = 0; i < lonLines; i++) {
      const lon = (i / lonLines) * Math.PI;
      const pts = [];
      for (let k = 0; k <= 48; k++) {
        pts.push(project(lon, -Math.PI / 2 + (k / 48) * Math.PI));
      }
      rings.push({ pts, kind: 'lon' });
    }
    return rings;
  }

  /* ---- text -------------------------------------------------------------- */

  /**
   * Per-character layout with em tracking (letter-spacing).
   * Returns [{ch, x, y, w}] with x = left edge, y = baseline.
   */
  function layoutText(ctx, text, x, y, size, weight, trackingEm, align = 'left') {
    ctx.save();
    ctx.font = `${weight} ${size}px Geist, sans-serif`;
    const tracking = trackingEm * size;
    let total = 0;
    const widths = [];
    for (const ch of text) {
      const w = ctx.measureText(ch).width;
      widths.push(w);
      total += w + tracking;
    }
    total -= tracking;
    let cx = align === 'center' ? x - total / 2 : align === 'right' ? x - total : x;
    const out = [];
    const chars = [...text];
    for (let i = 0; i < chars.length; i++) {
      out.push({ ch: chars[i], x: cx, y, w: widths[i], size });
      cx += widths[i] + tracking;
    }
    ctx.restore();
    return { chars: out, width: total };
  }

  /** Measure helper without drawing. */
  function measureText(ctx, text, size, weight, trackingEm) {
    ctx.save();
    ctx.font = `${weight} ${size}px Geist, sans-serif`;
    const tracking = trackingEm * size;
    let total = 0;
    for (const ch of text) total += ctx.measureText(ch).width + tracking;
    total -= tracking;
    ctx.restore();
    return total;
  }

  return {
    resample, morph, rotate, translate, scale, length,
    line, circle, polygon, roundRect, wave, bezier, sCurve, gear,
    honeycomb, cubeEdges, globe,
    layoutText, measureText
  };
})();
