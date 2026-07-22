# Voice load tests (VOICE-SCALING §7)

`voice_ramp.py` answers one question: **which layer of the voice stack fails
next, and at what concurrency?** A voice call is a stack of independent
layers (SFU room, worker job process, STT stream, LLM rate share, TTS stream,
Dapr-invoked tools) — real capacity is the lowest ceiling.

## Requirements

This is **not a unit test** — it drives a live stack:

1. The full voice profile running:
   ```sh
   make up-voice
   ```
   (livekit server, voice control plane on :7006, voice worker(s), ollama,
   piper sidecar, dapr sidecars, booking/identity/knowledge services)
2. Python deps for the harness itself:
   ```sh
   pip install "livekit-api>=0.6,<0.7" livekit
   ```

## Usage

```sh
make loadtest-voice                       # defaults below
make loadtest-voice LOADTEST_ARGS="--ramp 5,10 --hold-s 30"
# or directly:
python3 tests/load/voice_ramp.py \
    --livekit-url ws://localhost:7880 --api-key devkey --api-secret secret \
    --ramp 5,10,25,50 --hold-s 60 \
    --metrics-url http://localhost:7006/metrics \
    --prometheus-url http://localhost:9090   # optional cross-check
```

What it does per stage:

1. Pre-creates `loadtest-{i}` rooms via livekit-api.
2. Adds simulated callers (one room each) publishing a looped, programmatically
   generated speech-like WAV (440 Hz tone + amplitude wobble + noise, 16 kHz
   s16le) through the rtc SDK with real-time pacing — each room triggers one
   worker agent job.
3. Holds the stage (default 60 s) and diffs the voice `/metrics` counters
   before/after.
4. Prints a per-layer saturation report and the **first-degrading layer**.

## Tunables

| Flag | Default | Meaning |
|---|---|---|
| `--ramp` | `5,10,25,50` | concurrency stages (callers) |
| `--hold-s` | `60` | hold per stage |
| `--warmup-s` | `10` | settle time before sampling a stage |
| `--thresh-stt` | `2.0` | avg STT latency (s) above which the STT layer is degrading |
| `--thresh-llm` | `4.0` | avg LLM latency (s) |
| `--thresh-tts` | `1.5` | avg TTS latency (s) |
| `--thresh-tool-ratio` | `0.05` | tool error+timeout ratio for the application layer |
| `--skip-room-create` | off | skip livekit-api room pre-creation (join auto-creates) |

## Reading the report

Layers and their signal source (voice control plane `:7006/metrics`):

- **worker** — `voice_active_sessions` gauge vs expected concurrency (a gap
  means jobs were rejected by `load_fnc`/`LOAD_THRESHOLD` gating).
- **stt / llm / tts** — avg latency per stage from the
  `voice_*_latency_seconds` histograms (sum/count deltas).
- **application (tools)** — `voice_tool_calls_total{result=...}` error and
  timeout ratio; the 4 s `TOOL_TIMEOUT_SECONDS` apology fallback shows up
  here as `result="timeout"` instead of dead air.

Exit code: `0` when a layer degraded (the useful result — you found the next
ceiling), `1` when nothing degraded (raise the ramp), `2` when the
environment/deps are missing.
