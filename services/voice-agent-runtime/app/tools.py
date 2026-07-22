"""The six receptionist tools (SPEC §11), shared by the LiveKit voice worker,
the text chat endpoint and the ElevenLabs webhook.

EXACT tool names: get_business_info, get_availability, book_appointment,
lookup_appointment, reschedule_appointment, cancel_appointment.

- Read-only tools call booking-service public endpoints via Dapr service
  invocation (app-id `booking`).
- Mutating tools publish CloudEvents commands to Kafka topic
  `opendesk.booking.commands` via Dapr pubsub component `pubsub-kafka`;
  the CloudEvent id is reused as the idempotency key (`data.idempotency_key`).
- Phone-confirmation policy (SPEC §1/§11): book/lookup/reschedule/cancel
  refuse without a confirmed phone in session state (see session_state.py).
"""

from __future__ import annotations

import uuid
from datetime import datetime, timedelta, timezone
from typing import Any

from .config import Settings
from .dapr_client import DaprClient
from .escalation import LiveKitEscalation, escalation_room_name
from .events import new_cloudevent
from .logging import get_logger
from .plugin_tools import PluginTool
from .session_state import PhoneConfirmationRequired, SessionState
from .tenant_context import TenantContext

log = get_logger("tools")

BOOK = "com.opendesk.booking.command.BookAppointment"
RESCHEDULE = "com.opendesk.booking.command.RescheduleAppointment"
CANCEL = "com.opendesk.booking.command.CancelAppointment"
ESCALATION_REQUESTED = "com.opendesk.conversation.EscalationRequested"

# OpenAI-format tool schemas, used by the chat path and the ElevenLabs
# webhook (the LiveKit worker derives its schema from the FunctionContext
# docstrings/signatures with the same names).
TOOL_SCHEMAS: list[dict[str, Any]] = [
    {
        "type": "function",
        "function": {
            "name": "get_business_info",
            "description": "Get business information: catalog (offerings with ids, durations, prices), team members, timezone, currency and terminology.",
            "parameters": {"type": "object", "properties": {}, "required": []},
        },
    },
    {
        "type": "function",
        "function": {
            "name": "get_availability",
            "description": "Get open appointment slots for an offering with a team member in a time range.",
            "parameters": {
                "type": "object",
                "properties": {
                    "offering_id": {"type": "string", "description": "Offering UUID"},
                    "team_member_id": {"type": "string", "description": "Team member UUID"},
                    "from_iso": {"type": "string", "description": "Range start, RFC3339"},
                    "to_iso": {"type": "string", "description": "Range end, RFC3339 (max 62 days)"},
                },
                "required": ["offering_id", "team_member_id", "from_iso", "to_iso"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "book_appointment",
            "description": "Book an appointment. Requires the caller's phone number (phone-confirmation policy).",
            "parameters": {
                "type": "object",
                "properties": {
                    "offering_id": {"type": "string"},
                    "team_member_id": {"type": "string"},
                    "starts_at": {"type": "string", "description": "RFC3339 start time"},
                    "phone": {"type": "string", "description": "Caller phone number"},
                    "contact_name": {"type": "string"},
                    "email": {"type": "string"},
                },
                "required": ["offering_id", "team_member_id", "starts_at", "phone"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "lookup_appointment",
            "description": "Look up the caller's upcoming appointments by phone number.",
            "parameters": {
                "type": "object",
                "properties": {
                    "phone": {"type": "string", "description": "Caller phone number"},
                },
                "required": ["phone"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "reschedule_appointment",
            "description": "Reschedule an existing booking to a new start time.",
            "parameters": {
                "type": "object",
                "properties": {
                    "booking_id": {"type": "string", "description": "Booking UUID"},
                    "starts_at": {"type": "string", "description": "New start, RFC3339"},
                    "phone": {"type": "string", "description": "Caller phone number"},
                },
                "required": ["booking_id", "starts_at", "phone"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "cancel_appointment",
            "description": "Cancel an existing booking.",
            "parameters": {
                "type": "object",
                "properties": {
                    "booking_id": {"type": "string", "description": "Booking UUID"},
                    "phone": {"type": "string", "description": "Caller phone number"},
                    "reason": {"type": "string"},
                },
                "required": ["booking_id", "phone"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "request_human",
            "description": (
                "Escalate the conversation to a human staff member (warm "
                "handoff). Creates a LiveKit escalation room, notifies staff "
                "and confirms to the caller. Use when the caller asks for a "
                "human, is distressed, or the request cannot be resolved."
            ),
            "parameters": {
                "type": "object",
                "properties": {
                    "reason": {
                        "type": "string",
                        "description": "Short reason for the escalation",
                    },
                },
                "required": [],
            },
        },
    },
]

TOOL_NAMES = [t["function"]["name"] for t in TOOL_SCHEMAS]


def _confirmation_payload(pending_phone: str) -> dict[str, Any]:
    return {
        "status": "confirmation_required",
        "message": (
            "Phone-confirmation policy: read the phone number "
            f"{pending_phone or '(not provided)'} back to the caller and ask "
            "them to confirm it, then call this tool again with the same number."
        ),
        "pending_phone": pending_phone,
    }


class ToolLayer:
    """Implements the six tools against Dapr. One instance per session."""

    def __init__(
        self,
        *,
        dapr: DaprClient,
        settings: Settings,
        ctx: TenantContext,
        session: SessionState,
        escalation: LiveKitEscalation | None = None,
        plugin_tools: list[PluginTool] | None = None,
    ) -> None:
        self._dapr = dapr
        self._settings = settings
        self._ctx = ctx
        self._session = session
        self._escalation = escalation or LiveKitEscalation(settings)
        self._plugin_tools = {t.name: t for t in (plugin_tools or [])}

    def schemas(self) -> list[dict[str, Any]]:
        """Built-in tool schemas plus any pack plugin tool schemas."""
        return TOOL_SCHEMAS + [t.schema() for t in self._plugin_tools.values()]

    # ------------------------------------------------------------------ util
    async def _emit_tool_event(self, tool: str, status: str, detail: dict[str, Any]) -> None:
        event = new_cloudevent(
            type_="com.opendesk.conversation.ToolInvoked",
            subject=self._ctx.tenant_slug,
            tenant_uuid=self._ctx.tenant_id,
            data={
                "conversationId": self._session.conversation_id,
                "tool": tool,
                "status": status,
                "detail": detail,
            },
        )
        await self._dapr.publish_best_effort(
            self._settings.dapr_pubsub,
            self._settings.conversation_events_topic,
            event,
            kind="ToolInvoked",
        )

    def _require_phone(self, phone: str | None) -> str:
        if not self._settings.phone_confirmation_required:
            return (phone or self._session.confirmed_phone or "").strip()
        return self._session.require_confirmed_phone(phone)

    async def _publish_command(self, type_: str, data: dict[str, Any]) -> str:
        """Publish a booking command; returns the CloudEvent id (idempotency key)."""
        event_id = str(uuid.uuid4())
        data = {**data, "idempotency_key": event_id}
        event = new_cloudevent(
            type_=type_,
            subject=self._ctx.tenant_slug,  # tenant slug
            tenant_uuid=self._ctx.tenant_id,  # tenant UUID (consumer parses it)
            data=data,
            event_id=event_id,
        )
        await self._dapr.publish(
            self._settings.dapr_pubsub, self._settings.booking_commands_topic, event
        )
        log.info("booking command published", type=type_, event_id=event_id)
        return event_id

    # ------------------------------------------------------------- read-only
    async def get_business_info(self) -> dict[str, Any]:
        ctx = self._ctx
        result = {
            "business": ctx.display_name,
            "timezone": ctx.timezone,
            "currency": ctx.currency,
            "locale": ctx.locale,
            "terminology": ctx.terminology,
            "offerings": [
                {
                    "id": o.get("id"),
                    "name": o.get("name"),
                    "description": o.get("description"),
                    "duration_min": o.get("duration_min"),
                    "price_cents": o.get("price_cents"),
                    "currency": ctx.currency,
                }
                for o in ctx.offerings
            ],
            "team_members": [
                {"id": m.get("id"), "name": m.get("name"), "role": m.get("role")}
                for m in ctx.team_members
            ],
        }
        await self._emit_tool_event("get_business_info", "ok", {})
        return result

    async def get_availability(
        self, offering_id: str, team_member_id: str, from_iso: str, to_iso: str
    ) -> dict[str, Any]:
        resp = await self._dapr.invoke_get(
            self._settings.booking_app_id,
            f"public/sites/{self._ctx.site_slug}/availability",
            params={
                "offering_id": offering_id,
                "team_member_id": team_member_id,
                "from": from_iso,
                "to": to_iso,
            },
        )
        slots = (resp or {}).get("slots") or []
        await self._emit_tool_event(
            "get_availability", "ok", {"slots": len(slots), "offering_id": offering_id}
        )
        return {
            "offering_id": offering_id,
            "team_member_id": team_member_id,
            "timezone": self._ctx.timezone,
            "slots": slots,
        }

    # -------------------------------------------------------------- mutating
    async def book_appointment(
        self,
        offering_id: str,
        team_member_id: str,
        starts_at: str,
        phone: str,
        contact_name: str | None = None,
        email: str | None = None,
    ) -> dict[str, Any]:
        try:
            confirmed = self._require_phone(phone)
        except PhoneConfirmationRequired as pcr:
            await self._emit_tool_event("book_appointment", "confirmation_required", {})
            return _confirmation_payload(pcr.pending_phone)

        event_id = await self._publish_command(
            BOOK,
            {
                "offering_id": offering_id,
                "team_member_id": team_member_id,
                "starts_at": starts_at,
                "phone": confirmed,
                "contact_name": contact_name or self._session.caller_name or "",
                "email": email or "",
                "source": "voice",
                "conversation_id": self._session.conversation_id,
            },
        )
        await self._emit_tool_event(
            "book_appointment", "accepted", {"offering_id": offering_id, "starts_at": starts_at}
        )
        return {
            "status": "accepted",
            "message": "Booking request accepted and queued for confirmation.",
            "command_id": event_id,
            "offering_id": offering_id,
            "team_member_id": team_member_id,
            "starts_at": starts_at,
        }

    async def lookup_appointment(self, phone: str) -> dict[str, Any]:
        try:
            confirmed = self._require_phone(phone)
        except PhoneConfirmationRequired as pcr:
            await self._emit_tool_event("lookup_appointment", "confirmation_required", {})
            return _confirmation_payload(pcr.pending_phone)

        now = datetime.now(timezone.utc)
        resp = await self._dapr.invoke_get(
            self._settings.booking_app_id,
            "v1/bookings",
            params={
                "from": (now - timedelta(days=1)).isoformat(),
                "to": (now + timedelta(days=180)).isoformat(),
            },
            headers={"X-Tenant-Slug": self._ctx.tenant_slug},
        )
        bookings = resp if isinstance(resp, list) else (resp or {}).get("bookings") or []
        mine = [b for b in bookings if b.get("contact_phone") == confirmed]
        for b in mine:
            bid = b.get("id")
            if bid and bid not in self._session.last_booking_ids:
                self._session.last_booking_ids.append(str(bid))
        await self._emit_tool_event(
            "lookup_appointment", "ok", {"found": len(mine)}
        )
        return {"phone": confirmed, "bookings": mine, "count": len(mine)}

    async def reschedule_appointment(
        self, booking_id: str, starts_at: str, phone: str
    ) -> dict[str, Any]:
        try:
            confirmed = self._require_phone(phone)
        except PhoneConfirmationRequired as pcr:
            await self._emit_tool_event("reschedule_appointment", "confirmation_required", {})
            return _confirmation_payload(pcr.pending_phone)

        event_id = await self._publish_command(
            RESCHEDULE,
            {
                "booking_id": booking_id,
                "starts_at": starts_at,
                "phone": confirmed,
                "source": "voice",
                "conversation_id": self._session.conversation_id,
            },
        )
        await self._emit_tool_event(
            "reschedule_appointment", "accepted", {"booking_id": booking_id}
        )
        return {
            "status": "accepted",
            "message": "Reschedule request accepted and queued.",
            "command_id": event_id,
            "booking_id": booking_id,
            "starts_at": starts_at,
        }

    async def cancel_appointment(
        self, booking_id: str, phone: str, reason: str | None = None
    ) -> dict[str, Any]:
        try:
            confirmed = self._require_phone(phone)
        except PhoneConfirmationRequired as pcr:
            await self._emit_tool_event("cancel_appointment", "confirmation_required", {})
            return _confirmation_payload(pcr.pending_phone)

        event_id = await self._publish_command(
            CANCEL,
            {
                "booking_id": booking_id,
                "phone": confirmed,
                "reason": reason or "voice_command",
                "source": "voice",
                "conversation_id": self._session.conversation_id,
            },
        )
        await self._emit_tool_event(
            "cancel_appointment", "accepted", {"booking_id": booking_id}
        )
        return {
            "status": "accepted",
            "message": "Cancellation request accepted and queued.",
            "command_id": event_id,
            "booking_id": booking_id,
        }

    # ------------------------------------------------------- warm handoff
    async def request_human(self, reason: str | None = None) -> dict[str, Any]:
        """Escalate to a human operator (SPEC-W3 §4, innovation 1).

        Creates LiveKit room ``escalation-{conversation_id}``, mints a staff
        join token and publishes an EscalationRequested CloudEvent to
        ``opendesk.conversation.events``. Degrades gracefully when LiveKit
        is unreachable: the event still goes out (staff see the banner) and
        the caller still gets a spoken confirmation.
        """
        room = escalation_room_name(self._session.conversation_id)
        room_created = await self._escalation.create_room(room)
        join_token_staff = self._escalation.staff_join_token(room)

        self._session.escalation_room = room
        self._session.touch()

        event = new_cloudevent(
            type_=ESCALATION_REQUESTED,
            subject=self._ctx.tenant_slug,
            tenant_uuid=self._ctx.tenant_id,
            data={
                "conversation_id": self._session.conversation_id,
                "tenant_id": self._ctx.tenant_id,
                "site_slug": self._ctx.site_slug,
                "room": room,
                "join_token_staff": join_token_staff,
                "reason": reason or "caller_requested",
            },
        )
        await self._dapr.publish(
            self._settings.dapr_pubsub,
            self._settings.conversation_events_topic,
            event,
        )
        await self._emit_tool_event(
            "request_human", "escalated", {"room": room, "room_created": room_created}
        )
        log.info(
            "escalation requested",
            conversation_id=self._session.conversation_id,
            room=room,
            room_created=room_created,
        )
        return {
            "status": "escalated",
            "room": room,
            "room_created": room_created,
            "message": (
                "I'm connecting you with a member of our team right now. "
                "They've been notified and will join shortly; I'll stay on "
                "the line to help in the meantime."
            ),
        }

    # ----------------------------------------------------------- dispatching
    async def dispatch(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        """Dispatch a tool call by name (chat path / ElevenLabs webhook)."""
        handler = {
            "get_business_info": lambda: self.get_business_info(),
            "get_availability": lambda: self.get_availability(
                offering_id=str(arguments.get("offering_id", "")),
                team_member_id=str(arguments.get("team_member_id", "")),
                from_iso=str(arguments.get("from_iso", "")),
                to_iso=str(arguments.get("to_iso", "")),
            ),
            "book_appointment": lambda: self.book_appointment(
                offering_id=str(arguments.get("offering_id", "")),
                team_member_id=str(arguments.get("team_member_id", "")),
                starts_at=str(arguments.get("starts_at", "")),
                phone=str(arguments.get("phone", "")),
                contact_name=arguments.get("contact_name"),
                email=arguments.get("email"),
            ),
            "lookup_appointment": lambda: self.lookup_appointment(
                phone=str(arguments.get("phone", ""))
            ),
            "reschedule_appointment": lambda: self.reschedule_appointment(
                booking_id=str(arguments.get("booking_id", "")),
                starts_at=str(arguments.get("starts_at", "")),
                phone=str(arguments.get("phone", "")),
            ),
            "cancel_appointment": lambda: self.cancel_appointment(
                booking_id=str(arguments.get("booking_id", "")),
                phone=str(arguments.get("phone", "")),
                reason=arguments.get("reason"),
            ),
            "request_human": lambda: self.request_human(
                reason=arguments.get("reason"),
            ),
        }.get(name)
        if handler is None:
            plugin = self._plugin_tools.get(name)
            if plugin is not None:
                try:
                    result = await plugin.execute(arguments)
                    await self._emit_tool_event(name, result.get("status", "ok"), {})
                    return result
                except Exception as exc:  # noqa: BLE001 - surfaced to the model
                    log.warning("plugin tool failed", tool=name, error=str(exc))
                    await self._emit_tool_event(name, "error", {"error": str(exc)[:200]})
                    return {"status": "error", "message": f"{name} failed: {exc}"}
            return {"status": "error", "message": f"unknown tool {name!r}"}
        try:
            return await handler()
        except Exception as exc:  # noqa: BLE001 - surfaced to the model
            log.warning("tool call failed", tool=name, error=str(exc))
            await self._emit_tool_event(name, "error", {"error": str(exc)[:200]})
            return {"status": "error", "message": f"{name} failed: {exc}"}
