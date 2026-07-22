# Eval harness (SPEC-W3 §4, innovation 5)

Replays synthetic conversations (`scenarios/*.yaml`) against the live
`POST /voice/chat` endpoint, asserts expected tool calls per turn, and scores
each turn 1-5 with an LLM-as-judge (correctness / tool-call accuracy /
persona adherence per the turn's `judge` criterion) through the same
OpenAI-compatible env as the runtime (`LLM_BASE_URL`/`LLM_MODEL`/`LLM_API_KEY`
— Ollama `qwen3:8b` by default). Writes `report.md` and, when
`OPENSEARCH_ADDR` is set and reachable, indexes one document per scenario
into the `evals` index (best-effort).

Scenarios: salon booking happy path, clinic intake question, cancel flow,
escalation request (`request_human`).

```bash
pip install httpx pyyaml            # or: pip install -e .[dev]
python3 eval/eval.py                # full run with judge
python3 eval/eval.py --no-judge     # tool-call assertions only
make eval                           # from repo root
```

Exit code is non-zero when any turn fails (missing tool call, HTTP error, or
judge score < 3). Replaying archived conversations from the OpenSearch
`conversations` index is future work; synthetic scenarios are the v1 corpus.

## A/B prompt testing (Wave 5 #8)

`ab_test.py` pits two persona variants across **all** scenarios and lets the
LLM judge pick a winner:

- **A** — the tenant's current persona, resolved server-side from the
  industry pack (production behavior, no override).
- **B** — a candidate persona from `personas/*.md`, sent per request as
  `persona_override`. The runtime only honors that field when started with
  `EVAL_PERSONA_OVERRIDE=true` (off by default — enabling it on a public
  endpoint is a prompt-injection surface).

```bash
# 1. run the voice runtime with the override gate open (eval only!)
EVAL_PERSONA_OVERRIDE=true python -m app.main   # or docker compose

# 2. compare A (incumbent) vs B (candidate)
python3 eval/ab_test.py --tenant acme --persona-b eval/personas/salon_warm_concise.md
make ab-test AB_ARGS="--tenant acme --persona-b eval/personas/salon_warm_concise.md"

# 3. promote the winner (writes eval/promoted/acme.md + prints recommendation)
python3 eval/ab_test.py --tenant acme --persona-b ... --promote
```

Scoring: each turn of each scenario is judged 1-5 per variant. The summary
reports the **mean score per variant** and a **per-scenario win rate** (the
variant with the higher scenario mean wins it; ties count ½). Promotion is
conservative: B is recommended only when its overall mean beats A by ≥ 0.25
AND it wins strictly more scenarios; otherwise the incumbent stays. The full
comparison lands in `ab_report.md`; `--promote` writes the winning persona to
`eval/promoted/{tenant}.md`, ready to be copied into the pack's
`agentPersona`. `--no-judge` runs the mechanics without scores (means are
0.0 and A always keeps the crown — useful for smoke-testing the harness).
