/* =============================================================================
 * HyperFrames — deterministic frame engine for the ARTEX showreel.
 * 60 s · 120 BPM · 120 beats · 30 bars · 3600 frames @ 60 fps · 0 cuts.
 * Every visual is a pure function of time t ∈ [0, 60); render(0) ≡ render(60).
 * Movement math: critically damped spring  x(t) = 1 − (1 + w·t)·e^(−w·t),
 * w ∈ [12, 40] — the system never overshoots its target.
 * ========================================================================== */

const HF = (() => {
  'use strict';

  /* ---- clock ------------------------------------------------------------- */
  const BPM = 120;
  const BEAT = 60 / BPM;          // 0.5 s
  const BAR = BEAT * 4;           // 2.0 s
  const BEATS = 120;
  const BARS = 30;
  const FPS = 60;
  const FRAMES = 3600;
  const DURATION = 60;
  const PRE = 0.05;               // movements start 0.05 s before the beat

  /** Absolute time of beat n (0 … 120). */
  const beatTime = (n) => n * BEAT;
  /** Event start for beat n — lands perceptually on the beat. */
  const eventTime = (n) => beatTime(n) - PRE;
  /** Bar index (0-based) for absolute time t. */
  const barIndex = (t) => Math.min(BARS - 1, Math.max(0, Math.floor(t / BAR)));
  /** Local time inside a bar. */
  const barLocal = (t) => t - barIndex(t) * BAR;

  const BAR_NAMES = [
    'SCAN', 'ASSET', 'MAP', 'PORT', 'TRACE', 'CURVE', 'MORPH', 'NODE', 'GRAPH',
    'TILE', 'SOLID', 'GEAR', 'PANEL', 'DATA', 'RULE', 'CHECK', 'TOOL', 'REQUEST',
    'RESPONSE', 'PRIORITY', 'LOG', 'LIGHT', 'WAVE', 'GLOBE', 'PIPELINE', 'AUDIT',
    'VERIFY', 'BOUNDARY', 'LOOP', 'END'
  ];

  /* ---- easing ------------------------------------------------------------ */

  /** Raw critically damped spring: 1 − (1 + w·t)·e^(−w·t), w ∈ [12, 40]. */
  function springRaw(w, t) {
    if (t <= 0) return 0;
    return 1 - (1 + w * t) * Math.exp(-w * t);
  }

  /**
   * Critically damped spring eased over duration D: returns p(τ) ∈ [0,1]
   * with p(0)=0 and p(D)=1 exactly — never overshoots.
   */
  function spring(w, D) {
    const inv = 1 / springRaw(w, D);
    return (τ) => (τ <= 0 ? 0 : τ >= D ? 1 : springRaw(w, τ) * inv);
  }

  /** Exponential ease-out (wipes, floods): fast onset, slow settle. */
  function expOut(k, D) {
    const inv = 1 / (1 - Math.exp(-k * D));
    return (τ) => (τ <= 0 ? 0 : τ >= D ? 1 : (1 - Math.exp(-k * τ)) * inv);
  }

  /** Exponential ease-in (element exits): slow onset, accelerating away. */
  function expIn(k, D) {
    const inv = 1 / (Math.exp(k * D) - 1);
    return (τ) => (τ <= 0 ? 0 : τ >= D ? 1 : (Math.exp(k * τ) - 1) * inv);
  }

  /** Linear ramp. */
  function linear(D) {
    return (τ) => (τ <= 0 ? 0 : τ >= D ? 1 : τ / D);
  }

  /* ---- math helpers ------------------------------------------------------ */
  const clamp = (v, a, b) => (v < a ? a : v > b ? b : v);
  const lerp = (a, b, p) => a + (b - a) * p;
  const invLerp = (a, b, v) => (b === a ? 0 : clamp((v - a) / (b - a), 0, 1));
  const TAU = Math.PI * 2;
  const rad = (deg) => (deg * Math.PI) / 180;

  /** Map t through a spring window [t0, t0+D] → eased 0..1. */
  function seg(t, t0, D, easeFn) {
    return easeFn(t - t0);
  }

  /* ---- deterministic noise ---------------------------------------------- */

  function mulberry32(seed) {
    let a = seed >>> 0;
    return function () {
      a |= 0;
      a = (a + 0x6d2b79f5) | 0;
      let t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  /** Hash-based lattice value in [−1, 1]. */
  function hash1(i, seed) {
    let h = (i * 374761393 + seed * 668265263) | 0;
    h = Math.imul(h ^ (h >>> 13), 1274126177);
    return (((h ^ (h >>> 16)) >>> 0) / 4294967296) * 2 - 1;
  }

  /** Smooth 1-D value noise. */
  function vnoise(x, seed) {
    const i = Math.floor(x);
    const f = x - i;
    const u = f * f * (3 - 2 * f);
    return lerp(hash1(i, seed), hash1(i + 1, seed), u);
  }

  /**
   * Pencil-boil phase: the distortion phase updates 8 times per second and
   * wraps exactly at t = 60 (480 steps) so the loop is seamless.
   */
  const BOIL_HZ = 8;
  const BOIL_STEPS = BOIL_HZ * DURATION;
  function boilStep(t) {
    return Math.floor(t * BOIL_HZ + 1e-6) % BOIL_STEPS;
  }

  /* ---- formatting -------------------------------------------------------- */
  const pad = (n, w) => String(n).padStart(w, '0');

  /* ---- stage palette ----------------------------------------------------- */
  const COLOR = {
    paper: '#EDE6D5',
    blue: '#1B3ED2',
    cyan: '#45F0DD',
    black: '#07080A',
    graphite: '#16161A',
    white: '#FFFFFF'
  };

  /* ---- stage geometry constants ------------------------------------------ */
  const STAGE = {
    W: 1920,
    H: 1080,
    MX: 150,            // safe empty border left/right
    MY: 90,             // safe empty border top/bottom
    GRID: 60,
    GRID_MAJOR: 5       // every 5th grid line heavier
  };

  return {
    BPM, BEAT, BAR, BEATS, BARS, FPS, FRAMES, DURATION, PRE,
    beatTime, eventTime, barIndex, barLocal, BAR_NAMES,
    springRaw, spring, expOut, expIn, linear,
    clamp, lerp, invLerp, TAU, rad, seg,
    mulberry32, hash1, vnoise, boilStep, BOIL_HZ, BOIL_STEPS,
    pad,
    COLOR, STAGE
  };
})();
