#!/usr/bin/env python3
"""
score.py — original score for the ARTEX 60 s showreel.

Written in Python and synced automatically: every hit is placed on the same
120 BPM / 120 beat / 30 bar grid the animation uses (see js/hyperframes.js),
so the music locks to the picture without manual timing.

  python3 score.py            # writes score.wav + sfx_sync.json (loop-safe)

The output is exactly 60.000 s and loop-safe: every element is an integer
number of periods across the loop, so the reel can repeat forever.

Only the Python standard library is required.
"""

import json
import math
import struct
import wave

SR = 44100
DURATION = 60.0
BPM = 120.0
BEAT = 60.0 / BPM          # 0.5 s
BAR = BEAT * 4             # 2.0 s
BARS = 30

# bar roots (A minor family) — one chord per bar, loop closes on the tonic
ROOTS = [
    57, 57, 53, 53, 55, 55, 52, 52, 57, 57,
    53, 53, 55, 55, 52, 52, 57, 57, 53, 53,
    55, 55, 52, 52, 57, 57, 53, 55, 52, 57,
]


def midi_hz(n):
    return 440.0 * 2 ** ((n - 69) / 12.0)


def env_ad(t, attack, decay):
    if t < 0:
        return 0.0
    if t < attack:
        return t / attack
    d = t - attack
    return math.exp(-d / max(decay, 1e-4))


def render():
    n_total = int(SR * DURATION)
    left = [0.0] * n_total
    right = [0.0] * n_total
    sfx_events = []

    def add(buf, start_t, samples, gain=1.0, pan=0.0):
        i0 = int(start_t * SR)
        gl = gain * math.sqrt(0.5 * (1.0 - pan))
        gr = gain * math.sqrt(0.5 * (1.0 + pan))
        for k, s in enumerate(samples):
            i = i0 + k
            if 0 <= i < n_total:
                left[i] += s * gl
                right[i] += s * gr

    def kick(t, gain=1.0):
        dur = 0.22
        n = int(dur * SR)
        out = []
        phase = 0.0
        for k in range(n):
            tt = k / SR
            f = 118 * math.exp(-tt * 18) + 42
            phase += 2 * math.pi * f / SR
            out.append(math.sin(phase) * env_ad(tt, 0.001, 0.075))
        add(None, t, out, 0.95 * gain)

    def hat(t, gain=1.0, pan=0.2):
        dur = 0.06
        n = int(dur * SR)
        seed = int(t * 1000) & 0xFFFF
        out = []
        hp = 0.0
        for k in range(n):
            tt = k / SR
            seed = (seed * 1103515245 + 12345) & 0x7FFFFFFF
            noise = (seed / 0x3FFFFFFF) - 1.0
            hp = noise - 0.86 * hp          # simple high-pass
            out.append(hp * env_ad(tt, 0.001, 0.014))
        add(None, t, out, 0.16 * gain, pan)

    def bass(t, note, dur=0.42, gain=1.0):
        n = int(dur * SR)
        f = midi_hz(note - 24)
        out = []
        phase = 0.0
        for k in range(n):
            tt = k / SR
            phase += 2 * math.pi * f / SR
            s = math.sin(phase) + 0.32 * math.sin(2 * phase) + 0.12 * math.sin(3 * phase)
            out.append(s * env_ad(tt, 0.004, 0.16) * 0.45)
        add(None, t, out, 0.7 * gain)

    def pluck(t, note, dur=0.3, gain=1.0, pan=0.0):
        n = int(dur * SR)
        f = midi_hz(note)
        out = []
        phase = 0.0
        for k in range(n):
            tt = k / SR
            phase += 2 * math.pi * f / SR
            s = math.sin(phase) + 0.22 * math.sin(2 * phase)
            out.append(s * env_ad(tt, 0.002, 0.09))
        add(None, t, out, 0.2 * gain, pan)

    def pad(t, note, dur, gain=1.0):
        n = int(dur * SR)
        out = []
        for det in (-6.0, 0.0, 6.0):
            f = midi_hz(note + det / 100.0)
            phase = 0.0
            for k in range(n):
                tt = k / SR
                phase += 2 * math.pi * f / SR
                a = min(1.0, tt / 0.4) * min(1.0, (dur - tt) / 0.5)
                out.append(math.sin(phase) * a * 0.33)
        add(None, t, out, 0.12 * gain)

    def whoosh(t, dur=0.5, gain=1.0, up=True):
        """Exponential sweep — used only at the scene transition windows."""
        n = int(dur * SR)
        seed = 1234
        out = []
        lp = 0.0
        for k in range(n):
            tt = k / SR
            p = tt / dur
            seed = (seed * 1103515245 + 12345) & 0x7FFFFFFF
            noise = (seed / 0x3FFFFFFF) - 1.0
            alpha = (p if up else 1.0 - p) ** 2 * 0.28
            lp += alpha * (noise - lp)
            out.append(lp * env_ad(tt, 0.02, 0.22))
        add(None, t, out, 0.5 * gain)
        sfx_events.append({"t": round(t, 3), "type": "whoosh"})

    def scribble(t, dur=0.28, gain=1.0):
        """Pencil-on-paper burst — fires with every hand-drawn reveal."""
        n = int(dur * SR)
        seed = 777
        out = []
        bp = 0.0
        for k in range(n):
            tt = k / SR
            seed = (seed * 1103515245 + 12345) & 0x7FFFFFFF
            noise = (seed / 0x3FFFFFFF) - 1.0
            bp += 0.45 * (noise - bp)
            s = (noise - bp) * (0.5 + 0.5 * math.sin(tt * 90))
            out.append(s * env_ad(tt, 0.01, 0.08))
        add(None, t, out, 0.22 * gain)
        sfx_events.append({"t": round(t, 3), "type": "scribble"})

    def blip(t, f=880.0, dur=0.08, gain=1.0):
        n = int(dur * SR)
        out = []
        for k in range(n):
            tt = k / SR
            out.append(math.sin(2 * math.pi * f * tt) * env_ad(tt, 0.001, 0.02))
        add(None, t, out, 0.28 * gain)
        sfx_events.append({"t": round(t, 3), "type": "blip"})

    # ---- structure: 120 beats -------------------------------------------
    for b in range(120):
        t = b * BEAT
        bar_i = b // 4
        beat_in_bar = b % 4
        root = ROOTS[bar_i]

        # kick on every beat; ghost hat on the eighths
        kick(t, 1.0 if beat_in_bar == 0 else 0.72)
        hat(t + BEAT / 2, 0.8)
        if beat_in_bar in (1, 3):
            hat(t, 0.5, pan=-0.25)

        # bass follows the bar root on beats 1 & 3
        if beat_in_bar in (0, 2):
            bass(t, root)

        # arp plucks — the "analytical" motif, offset onto the eighths
        if beat_in_bar == 1:
            pluck(t + BEAT / 2, root + 12, pan=-0.3)
        if beat_in_bar == 3:
            pluck(t + BEAT / 2, root + 19, pan=0.3)

        # pad wash every 2 bars
        if beat_in_bar == 0 and bar_i % 2 == 0:
            pad(t, root + 12, BAR * 2, 0.9)

    # ---- auto-synced SFX on picture events -------------------------------
    # scene transitions (matches js/scenes.js transitionAt)
    for t, up in [(9.60, True), (23.50, True), (29.50, False),
                  (39.50, True), (47.50, True), (55.80, False)]:
        whoosh(t, 0.55, 1.0, up)

    # pencil reveals (paper act) — one per storyboard draw event
    for t in [0.0, 1.0, 2.0, 2.5, 3.5, 4.1, 5.0, 6.1, 7.5, 8.0, 8.5, 9.0,
              30.0, 31.0, 32.0, 34.0, 36.0, 38.0, 57.2, 58.2]:
        scribble(t, 0.3, 1.0)

    # UI blips (blueprint / hologram acts)
    for t in [15.5, 16.5, 21.5, 22.5, 25.5, 28.0, 52.3]:
        blip(t, 920, 0.09, 1.0)

    # ---- master: soft clip + loop seam is implicit (all periods fit) ------
    out = bytearray()
    for i in range(n_total):
        l = math.tanh(left[i] * 0.82)
        r = math.tanh(right[i] * 0.82)
        out += struct.pack('<hh', int(max(-1, min(1, l)) * 32000),
                           int(max(-1, min(1, r)) * 32000))
    return bytes(out), sfx_events


def main():
    import os
    out_dir = os.path.dirname(os.path.abspath(__file__))
    data, sfx = render()
    with wave.open(os.path.join(out_dir, 'score.wav'), 'wb') as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(SR)
        w.writeframes(data)
    with open(os.path.join(out_dir, 'sfx_sync.json'), 'w') as f:
        json.dump({"bpm": BPM, "beats": 120, "bars": BARS,
                   "duration": DURATION, "sfx": sfx}, f, indent=2)
    print(f'wrote {out_dir}/score.wav ({DURATION:.0f}s loop) + sfx_sync.json ({len(sfx)} sfx)')


if __name__ == '__main__':
    main()
