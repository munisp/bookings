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
