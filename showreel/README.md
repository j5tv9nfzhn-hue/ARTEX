# ARTEX — 60 s Looping Motion Design Showreel

> **Artex modified open-source reel, full motion graphic + original score included.**
>
> It coded every frame (HyperFrames + GSAP), wrote the background music in
> Python and synced all sound effects automatically.
>
> The audit panel and visualization art in "Creating" scene is real generated
> output: Artex exported assessment logs rendered inside DaVinci Resolve on the
> same day.
>
> Details ⤵️

## Run it

```bash
# any static server from this directory — then open the page
python3 -m http.server 8000
# → http://localhost:8000
```

Player controls: `SPACE` play/pause · `←/→` step one frame · `R` restart ·
scrub bar to seek. Query params for deterministic stills: `?t=12.5`,
`?frame=750` (frame index @ 60 fps), `?paused=1`.

## The film

One single analytical line evolves three times and never leaves the screen for
the whole 60 seconds:

| Stage | Ink | Look |
| --- | --- | --- |
| PAPER | graphite `#16161A`, accent blue `#1B3ED2` | cream `#EDE6D5` paper, 60 px grid (13 % / 26 % every 5th), sine-distorted double pencil strokes (phase boils at 8 Hz), diagonal hand hatching, multiply grain masked out from strokes |
| BLUEPRINT | white, accent cyan `#45F0DD` | solid blue `#1B3ED2`, identical grid in white, clean 3–4 px lines, thin dashed hidden edges, fine neat hatching |
| HOLOGRAM | cyan `#45F0DD` + white core (30 % width) | black `#07080A`, grid in cyan, fills from 8 px pitch scan lines whose thickness is the lighting. **No blur, no glow** — light is line density |

Rule obeyed throughout: *the accent color of the current stage becomes the
primary color of the next* — blue pencil on paper → cyan lines on blueprint →
white highlights on hologram.

**Type** — Geist only. Headlines 900 with hatching clipped to letterforms and a
doubled outline; labels 600–800 at 24–28 px with 0.14–0.3 em tracking.

**Camera** — locked front orthographic, top-down drafting table. Objects move;
the camera never does. Static objects respect the 150 px / 90 px safe border
(moving geometry may cross it).

**Timing** — 120 BPM · 120 beats · 30 bars × 2 s. An event lands on every beat
and starts 0.05 s before its marker. Movement easing is the critically damped
spring `1 − (1 + w·t)·e^(−w·t)` with `w ∈ [12, 40]` — nothing ever overshoots.
Wipes, floods and exits use exponential ease-in/out.

**Transitions** — Paper → Blueprint: hard-edged shape flood. Blueprint →
Hologram and Hologram → Paper: a single horizontal scan line. No cuts, no
crossfades, no dissolves, no banned effects. **First frame ≡ final frame** —
the 3600-frame loop is seamless in both directions.

**Permanent HUD** — `FIG. [BAR] — [BAR NAME]` top left (30 names: SCAN, ASSET,
MAP, PORT, TRACE, CURVE, MORPH, NODE, GRAPH, TILE, SOLID, GEAR, PANEL, DATA,
RULE, CHECK, TOOL, REQUEST, RESPONSE, PRIORITY, LOG, LIGHT, WAVE, GLOBE,
PIPELINE, AUDIT, VERIFY, BOUNDARY, LOOP, END) and `BEAT [n] / 120` bottom right.

**Mascot** — the creative-director box character (76 × 54 body, 24 × 16 arm
stubs, 11 × 33 legs, 8 × 14 eyes, ~250 px overall with its drafting pencil,
right arm raised 58°) runs at 6 steps/s leaning 7° into its motion. Paper:
graphite outline + sparse blue hatching. Hologram: cyan outline + scan-line
fill + solid white eyes.

## Storyboard (30 bars)

| Bar | Stage | Beat |
| --- | --- | --- |
| 1 | PAPER | HOOK — ARTEX headline, mascot exits, rail presses letters flat, splits, dimension line |
| 2 | PAPER | SCAN — ASSET DISCOVERY → SCAN → ENDPOINT → PORT MAP between rails |
| 3 | PAPER | CURVE — pen-tool handles, 8-copy leaf fan, travelling sine waves |
| 4 | PAPER | MORPH — circle → triangle (120° spin) → square → hexagon + hatching |
| 5 | PAPER | NODE — honeycomb graph, solid fill, hexagon flood-out |
| 6 | BLUEPRINT | 3D NODE — corner-view cubes merge, perspective turn |
| 7 | BLUEPRINT | GEAR — 16T/10T/12T meshing gears, stepped 45° eighths |
| 8 | BLUEPRINT | PANEL — cube net unfolds into the ARTEX assessment UI |
| 9 | BLUEPRINT | DATA — graph line, live playhead, cursor click, warp to audit circle |
| 10 | BLUEPRINT | RULE — SCOPE / PERMISSION / WHITELIST tree, pruned, compressed |
| 11 | BLUEPRINT | TOOL — HTTP · DNS · SCANNER, packets, risk numbers |
| 12 | BLUEPRINT | LOG — priority sort, typed audit log, terminal flow |
| 13 | HOLOGRAM | VERIFY — cyan node graph, REACHED · CONFIRMED · LOGGED, mascot |
| 14 | HOLOGRAM | GLOBE — wireframe globe, ripples, waveform |
| 15 | HOLOGRAM | PIPELINE — COLLECT → ANALYZE → PLAN → EXECUTE → AUDIT |
| 16–20 | PAPER | CHECK · TOOL · REQUEST · RESPONSE · PRIORITY |
| 21–24 | BLUEPRINT | LOG · LIGHT · WAVE · GLOBE |
| 25–28 | HOLOGRAM | PIPELINE · AUDIT · VERIFY · BOUNDARY |
| 29–30 | PAPER | LOOP · END — the line returns to the headline rail, landing exactly on frame 0 |

## Code map

```
js/hyperframes.js   deterministic frame engine — clock, spring/exp easing, noise
js/geometry.js      polylines, morphing, gears, cubes, globes, honeycomb, type
js/ink.js           the three inks: pencil / wire / beam + hatching + grain + grid
js/mascot.js        the creative-director box character
js/scenes.js        all 30 bars of choreography as pure functions of t
js/render.js        hard-edged transition compositor (flood + scan line)
js/main.js          GSAP master transport + player UI
tools/render_frames.js  offline PNG frame exporter (npm i @napi-rs/canvas)
test/smoke.js       renders all 3600 frames headlessly (node test/smoke.js)
audio/score.py      original 120 BPM score + auto-synced SFX (python3 score.py)
```

## Score

```bash
cd audio && python3 score.py     # → score.wav (60 s loop) + sfx_sync.json
```

Pure stdlib synthesis: kick / hats / bass / arp / pad on the shared beat grid,
pencil scribbles on every draw event, whooshes inside the six transition
windows, UI blips on interaction beats. The page plays `audio/score.wav`
automatically when present (press PLAY once if the browser blocks autoplay) and
runs silently without it.

## Frame export (e.g. for DaVinci Resolve)

```bash
npm i @napi-rs/canvas
# copy Geist-*.ttf from the `geist` npm package into vendor/
node tools/render_frames.js --fonts vendor --out out --frames 0:3599:1
```
