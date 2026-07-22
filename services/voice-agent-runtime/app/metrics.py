"""Hand-rolled Prometheus text exposition for the voice inference plane
(VOICE-SCALING §3): per-call STT/LLM/TTS latency, LLM token usage, tool-call
outcomes and active voice sessions, served on the control plane `/metrics`.

No prometheus-client dependency: the registry is a small thread-safe in-memory
store with a `render()` producing the standard text exposition format, so the
worker job processes and the control plane can both record at low overhead.

Series:
- voice_stt_latency_seconds   (histogram)
- voice_llm_latency_seconds   (histogram)
- voice_llm_tokens_total      (counter, label kind=prompt|completion)
- voice_tts_latency_seconds   (histogram)
- voice_tool_calls_total      (counter, labels tool,result)
- voice_active_sessions       (gauge)
"""

from __future__ import annotations

import threading
import time

DEFAULT_BUCKETS = (0.05, 0.1, 0.25, 0.5, 1.0, 2.0, 4.0, 8.0)


def _labels_key(labels: dict[str, str]) -> tuple[tuple[str, str], ...]:
    return tuple(sorted(labels.items()))


def _format_labels(labels: tuple[tuple[str, str], ...]) -> str:
    if not labels:
        return ""
    inner = ",".join(f'{k}="{v}"' for k, v in labels)
    return "{" + inner + "}"


class Histogram:
    def __init__(self, name: str, description: str, buckets=DEFAULT_BUCKETS) -> None:
        self.name = name
        self.description = description
        self.buckets = tuple(sorted(buckets))
        self._counts = [0] * len(self.buckets)
        self._sum = 0.0
        self._count = 0
        self._lock = threading.Lock()

    def observe(self, value: float) -> None:
        with self._lock:
            for i, bound in enumerate(self.buckets):
                if value <= bound:
                    self._counts[i] += 1
            self._sum += value
            self._count += 1

    def time(self) -> "_Timer":
        return _Timer(self)

    def render(self) -> list[str]:
        lines = [
            f"# HELP {self.name} {self.description}",
            f"# TYPE {self.name} histogram",
        ]
        with self._lock:
            counts = list(self._counts)
            total = self._count
            sum_ = self._sum
        for bound, count in zip(self.buckets, counts):
            lines.append(f'{self.name}_bucket{{le="{bound:g}"}} {count}')
        lines.append(f'{self.name}_bucket{{le="+Inf"}} {total}')
        lines.append(f"{self.name}_sum {sum_:.6f}")
        lines.append(f"{self.name}_count {total}")
        return lines


class _Timer:
    """Context manager: `with hist.time(): ...` records wall-clock seconds."""

    def __init__(self, hist: Histogram) -> None:
        self._hist = hist
        self._start = 0.0

    def __enter__(self) -> "_Timer":
        self._start = time.perf_counter()
        return self

    def __exit__(self, *exc) -> None:
        self._hist.observe(time.perf_counter() - self._start)


class Counter:
    def __init__(self, name: str, description: str, label_names: tuple[str, ...]) -> None:
        self.name = name
        self.description = description
        self.label_names = label_names
        self._values: dict[tuple[tuple[str, str], ...], float] = {}
        self._lock = threading.Lock()

    def inc(self, amount: float = 1.0, **labels: str) -> None:
        key = _labels_key({k: str(v) for k, v in labels.items()})
        with self._lock:
            self._values[key] = self._values.get(key, 0.0) + amount

    def render(self) -> list[str]:
        lines = [
            f"# HELP {self.name} {self.description}",
            f"# TYPE {self.name} counter",
        ]
        with self._lock:
            items = sorted(self._values.items())
        for key, value in items:
            lines.append(f"{self.name}{_format_labels(key)} {value:g}")
        return lines


class Gauge:
    def __init__(self, name: str, description: str) -> None:
        self.name = name
        self.description = description
        self._value = 0.0
        self._lock = threading.Lock()

    def set(self, value: float) -> None:
        with self._lock:
            self._value = value

    def inc(self, amount: float = 1.0) -> None:
        with self._lock:
            self._value += amount

    def dec(self, amount: float = 1.0) -> None:
        with self._lock:
            self._value -= amount

    def render(self) -> list[str]:
        with self._lock:
            value = self._value
        return [
            f"# HELP {self.name} {self.description}",
            f"# TYPE {self.name} gauge",
            f"{self.name} {value:g}",
        ]


class Registry:
    def __init__(self) -> None:
        self.stt_latency = Histogram(
            "voice_stt_latency_seconds", "STT transcription latency per call."
        )
        self.llm_latency = Histogram(
            "voice_llm_latency_seconds", "LLM chat-completion latency per call."
        )
        self.llm_tokens = Counter(
            "voice_llm_tokens_total",
            "LLM tokens consumed.",
            ("kind",),  # kind=prompt|completion
        )
        self.tts_latency = Histogram(
            "voice_tts_latency_seconds", "TTS synthesis latency per call."
        )
        self.tool_calls = Counter(
            "voice_tool_calls_total",
            "Tool calls executed by the voice tool layer.",
            ("tool", "result"),  # result=ok|error|timeout
        )
        self.active_sessions = Gauge(
            "voice_active_sessions", "Currently active voice/chat sessions."
        )

    def render(self) -> str:
        lines: list[str] = []
        for metric in (
            self.stt_latency,
            self.llm_latency,
            self.llm_tokens,
            self.tts_latency,
            self.tool_calls,
            self.active_sessions,
        ):
            lines.extend(metric.render())
        return "\n".join(lines) + "\n"


# Process-wide default registry. Call sites record directly into it; tests
# swap it via `set_registry`.
_registry = Registry()


def get_registry() -> Registry:
    return _registry


def set_registry(registry: Registry) -> None:
    global _registry
    _registry = registry


def reset_registry() -> Registry:
    """Fresh registry (test isolation); returns it."""
    registry = Registry()
    set_registry(registry)
    return registry


def render() -> str:
    return _registry.render()
