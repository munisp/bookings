"""LLM stage: interface + OpenAI-compatible implementation.

Targets any OpenAI-compatible endpoint (env LLM_BASE_URL): Ollama by default
(http://ollama:11434/v1), vLLM or a hosted provider by configuration. Used by
the text chat path; the LiveKit worker uses the equivalent
`livekit-plugins-openai` LLM node with the same settings.
"""

from __future__ import annotations

import json
from typing import Any, Protocol

from openai import AsyncOpenAI

from ..logging import get_logger

log = get_logger("llm")


class LLMInterface(Protocol):
    async def chat_with_tools(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]],
    ) -> Any:
        """One chat-completion round with tool definitions.

        Returns the raw assistant message object (with optional `tool_calls`).
        """
        ...


class OpenAICompatibleLLM:
    def __init__(self, base_url: str, model: str, api_key: str = "ollama") -> None:
        self._client = AsyncOpenAI(base_url=base_url, api_key=api_key)
        self.model = model

    async def chat_with_tools(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]],
    ) -> Any:
        resp = await self._client.chat.completions.create(
            model=self.model,
            messages=messages,
            tools=tools or None,
            tool_choice="auto" if tools else None,
            temperature=0.3,
        )
        return resp.choices[0].message

    async def complete(self, messages: list[dict[str, Any]]) -> str:
        msg = await self.chat_with_tools(messages, tools=[])
        return msg.content or ""

    async def stream_with_tools(
        self,
        messages: list[dict[str, Any]],
        tools: list[dict[str, Any]],
    ) -> Any:
        """One STREAMING chat-completion round (SSE chat path, SPEC-W3 §3).

        Returns the OpenAI AsyncStream; each chunk's `choices[0].delta` may
        carry `content` text and/or incremental `tool_calls` fragments that
        must be assembled by index before execution.
        """
        return await self._client.chat.completions.create(
            model=self.model,
            messages=messages,
            tools=tools or None,
            tool_choice="auto" if tools else None,
            temperature=0.3,
            stream=True,
        )


async def run_tool_loop(
    llm: OpenAICompatibleLLM,
    *,
    messages: list[dict[str, Any]],
    tools: list[dict[str, Any]],
    dispatch,
    max_rounds: int = 6,
) -> tuple[str, list[dict[str, Any]]]:
    """Text-in/text-out agent loop with function calling.

    `dispatch(name, arguments)` is awaited for each tool call and must return
    a JSON-serialisable result. Returns (final_text, tool_trace).
    """
    trace: list[dict[str, Any]] = []
    for _round in range(max_rounds):
        msg = await llm.chat_with_tools(messages, tools)
        tool_calls = getattr(msg, "tool_calls", None) or []
        if not tool_calls:
            return msg.content or "", trace

        # Append the assistant message (with tool calls) then each tool result.
        messages.append(
            {
                "role": "assistant",
                "content": msg.content or "",
                "tool_calls": [
                    {
                        "id": tc.id,
                        "type": "function",
                        "function": {
                            "name": tc.function.name,
                            "arguments": tc.function.arguments,
                        },
                    }
                    for tc in tool_calls
                ],
            }
        )
        for tc in tool_calls:
            name = tc.function.name
            try:
                arguments = json.loads(tc.function.arguments or "{}")
            except json.JSONDecodeError:
                arguments = {}
            result = await dispatch(name, arguments)
            trace.append({"tool": name, "arguments": arguments, "result": result})
            messages.append(
                {
                    "role": "tool",
                    "tool_call_id": tc.id,
                    "content": json.dumps(result, ensure_ascii=False),
                }
            )
    log.warning("tool loop hit max rounds", rounds=max_rounds)
    return (
        "I'm sorry, I couldn't complete that request just now. "
        "Could you rephrase or try again?"
    ), trace


_MAX_ROUNDS_FALLBACK = (
    "I'm sorry, I couldn't complete that request just now. "
    "Could you rephrase or try again?"
)


async def run_tool_loop_stream(
    llm: OpenAICompatibleLLM,
    *,
    messages: list[dict[str, Any]],
    tools: list[dict[str, Any]],
    dispatch,
    max_rounds: int = 6,
):
    """Streaming variant of run_tool_loop (SPEC-W3 §3 SSE chat).

    Async generator yielding events through the SAME tool layer:
      - {"type": "delta", "text": ...} for every LLM content chunk,
      - {"type": "tool", "tool": name, "arguments": ..., "result": ...}
        when a streamed tool call completes and is executed,
      - {"type": "final", "text": full_reply, "trace": [...]} exactly once
        as the last event (callers use it for history bookkeeping; the
        terminal SSE `done` frame is emitted by the HTTP layer).

    Tool-call rounds append the assistant + tool messages to `messages`
    exactly like run_tool_loop, so history stays consistent between the
    streaming and non-streaming paths.
    """
    trace: list[dict[str, Any]] = []
    final_text = ""
    for _round in range(max_rounds):
        stream = await llm.stream_with_tools(messages, tools)
        content_parts: list[str] = []
        # Assemble incremental tool-call fragments by their `index` slot.
        tc_acc: dict[int, dict[str, Any]] = {}
        async for chunk in stream:
            choices = getattr(chunk, "choices", None) or []
            if not choices:
                continue
            delta = choices[0].delta
            content = getattr(delta, "content", None)
            if content:
                content_parts.append(content)
                yield {"type": "delta", "text": content}
            for tc in getattr(delta, "tool_calls", None) or []:
                slot = tc_acc.setdefault(
                    tc.index, {"id": "", "name": "", "arguments": ""}
                )
                if tc.id:
                    slot["id"] = tc.id
                fn = getattr(tc, "function", None)
                if fn is not None:
                    if fn.name:
                        slot["name"] += fn.name
                    if fn.arguments:
                        slot["arguments"] += fn.arguments

        text = "".join(content_parts)
        if not tc_acc:
            final_text = text
            break

        tool_calls = [tc_acc[i] for i in sorted(tc_acc)]
        messages.append(
            {
                "role": "assistant",
                "content": text,
                "tool_calls": [
                    {
                        "id": tc["id"] or f"call_{i}",
                        "type": "function",
                        "function": {"name": tc["name"], "arguments": tc["arguments"]},
                    }
                    for i, tc in enumerate(tool_calls)
                ],
            }
        )
        for i, tc in enumerate(tool_calls):
            name = tc["name"]
            try:
                arguments = json.loads(tc["arguments"] or "{}")
            except json.JSONDecodeError:
                arguments = {}
            result = await dispatch(name, arguments)
            trace.append({"tool": name, "arguments": arguments, "result": result})
            yield {"type": "tool", "tool": name, "arguments": arguments, "result": result}
            messages.append(
                {
                    "role": "tool",
                    "tool_call_id": tc["id"] or f"call_{i}",
                    "content": json.dumps(result, ensure_ascii=False),
                }
            )
    else:
        log.warning("streaming tool loop hit max rounds", rounds=max_rounds)
        final_text = _MAX_ROUNDS_FALLBACK
        yield {"type": "delta", "text": final_text}

    yield {"type": "final", "text": final_text, "trace": trace}
