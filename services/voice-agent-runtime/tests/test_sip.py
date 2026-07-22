"""SIP telephony inbound tests (Wave 5 #1): tenant resolution from the dialed
number, carrier caller-ID confirmation bypass, and the LiveKit SIP deploy
YAML schemas (safe_load)."""

from __future__ import annotations

from pathlib import Path
from types import SimpleNamespace

import pytest
import yaml

from app import sip
from app.session_state import PhoneConfirmationRequired, SessionState

REPO_ROOT = Path(__file__).resolve().parents[3]
SIP_DIR = REPO_ROOT / "deploy" / "livekit-sip"


def _settings(phone_map=None, default_site=""):
    return SimpleNamespace(
        tenant_phone_map=phone_map or {}, sip_default_site=default_site
    )


# --------------------------------------------------------------------------
# TENANT_PHONE_MAP parsing + tenant resolution
# --------------------------------------------------------------------------
class TestTenantPhoneMap:
    def test_parses_valid_json(self):
        m = sip.parse_tenant_phone_map('{"+15551234567": "acme", "+15557654321": "glow"}')
        assert m == {"+15551234567": "acme", "+15557654321": "glow"}

    def test_normalizes_keys(self):
        m = sip.parse_tenant_phone_map('{"+1 (555) 123-4567": "acme"}')
        assert m == {"+15551234567": "acme"}

    def test_invalid_json_yields_empty_map(self):
        assert sip.parse_tenant_phone_map("{not json") == {}

    def test_non_object_yields_empty_map(self):
        assert sip.parse_tenant_phone_map('["+15551234567"]') == {}

    def test_drops_non_e164_keys_and_empty_slugs(self):
        m = sip.parse_tenant_phone_map(
            '{"abc": "acme", "+15551234567": "", "+15557654321": "glow", "5551234": "x"}'
        )
        assert m == {"+15557654321": "glow"}

    def test_empty_input(self):
        assert sip.parse_tenant_phone_map("") == {}
        assert sip.parse_tenant_phone_map(None) == {}


class TestResolveTenant:
    MAP = {"+15551234567": "acme", "+15557654321": "glow"}

    def test_map_hit(self):
        slug, source = sip.resolve_tenant("+1 555-123-4567", self.MAP)
        assert (slug, source) == ("acme", "map")

    def test_default_site_fallback(self):
        slug, source = sip.resolve_tenant("+49999", self.MAP, default_site="front-desk")
        assert (slug, source) == ("front-desk", "default")

    def test_unmapped_without_default_raises(self):
        with pytest.raises(sip.SipTenantResolutionError):
            sip.resolve_tenant("+49999", self.MAP)


# --------------------------------------------------------------------------
# SIP participant / room detection + call info extraction
# --------------------------------------------------------------------------
class TestDetection:
    def test_is_sip_room(self):
        assert sip.is_sip_room("call-+15551234567")
        assert not sip.is_sip_room("site-acme")
        assert not sip.is_sip_room("")

    def test_is_sip_participant_by_kind(self):
        p = SimpleNamespace(kind=SimpleNamespace(name="SIP"), identity="abc", attributes={})
        assert sip.is_sip_participant(p)

    def test_is_sip_participant_by_identity(self):
        p = SimpleNamespace(kind=None, identity="sip_+15551234567_xyz", attributes={})
        assert sip.is_sip_participant(p)

    def test_is_sip_participant_by_attributes(self):
        p = SimpleNamespace(kind=None, identity="web-1", attributes={"sip.callID": "42"})
        assert sip.is_sip_participant(p)

    def test_web_participant_is_not_sip(self):
        p = SimpleNamespace(kind=None, identity="web-1", attributes={})
        assert not sip.is_sip_participant(p)
        assert not sip.is_sip_participant(None)


class TestExtractCallInfo:
    def test_attributes_take_precedence(self):
        p = SimpleNamespace(
            kind=None,
            identity="sip_+15551110000_x",
            attributes={
                "sip.phoneNumber": "+1 555-222-3333",
                "sip.trunkPhoneNumber": "+15551234567",
            },
        )
        caller, dialed, attrs = sip.extract_call_info("call-+15551234567", [p])
        assert caller == "+15552223333"
        assert dialed == "+15551234567"
        assert attrs["sip.phoneNumber"].startswith("+1")

    def test_identity_fallback_for_caller(self):
        p = SimpleNamespace(kind=None, identity="sip_+15552223333_ab12", attributes={})
        caller, dialed, _ = sip.extract_call_info("call-+15551234567", [p])
        assert caller == "+15552223333"
        assert dialed == "+15551234567"  # from the room name

    def test_room_name_only(self):
        caller, dialed, _ = sip.extract_call_info("call-+15557654321", [])
        assert caller == ""
        assert dialed == "+15557654321"


# --------------------------------------------------------------------------
# Caller-ID confirmation bypass (SIP caller ID IS the confirmation)
# --------------------------------------------------------------------------
class TestCallerIdBypass:
    def test_attach_sets_confirmed_phone(self):
        session = SessionState(conversation_id="c1", site_slug="acme")
        assert sip.attach_caller_id(session, "+1 (555) 222-3333")
        assert session.confirmed_phone == "+15552223333"
        assert session.pending_phone is None

    def test_bypass_skips_two_step_confirmation(self):
        """A SIP session with an attached caller ID runs mutating tools
        immediately — no confirmation_required round-trip."""
        session = SessionState(conversation_id="c1", site_slug="acme")
        sip.attach_caller_id(session, "+15552223333")
        # No phone argument needed at all: the confirmed phone applies.
        assert session.require_confirmed_phone(None) == "+15552223333"
        # Same number re-issued by the model also passes straight through.
        assert session.require_confirmed_phone("+15552223333") == "+15552223333"

    def test_anonymous_caller_keeps_normal_policy(self):
        session = SessionState(conversation_id="c1", site_slug="acme")
        assert not sip.attach_caller_id(session, "")
        with pytest.raises(PhoneConfirmationRequired):
            session.require_confirmed_phone("+15559990000")

    def test_bypass_scoped_to_sip_sessions(self):
        """Without the SIP bootstrap (web path), the two-step policy holds."""
        session = SessionState(conversation_id="c1", site_slug="acme")
        with pytest.raises(PhoneConfirmationRequired):
            session.require_confirmed_phone("+15552223333")


class TestBootstrap:
    def test_full_inbound_bootstrap(self):
        settings = _settings(phone_map={"+15551234567": "acme"})
        p = SimpleNamespace(
            kind=None,
            identity="sip_+15552223333_x",
            attributes={"sip.trunkPhoneNumber": "+15551234567"},
        )
        session = SessionState(conversation_id="c1", site_slug="")
        ctx = sip.bootstrap_inbound_call(settings, "call-+15551234567", [p], session)
        assert ctx.site_slug == "acme"
        assert ctx.tenant_source == "map"
        assert ctx.dialed_number == "+15551234567"
        assert ctx.caller_phone == "+15552223333"
        assert session.confirmed_phone == "+15552223333"

    def test_unmapped_number_raises(self):
        settings = _settings()
        with pytest.raises(sip.SipTenantResolutionError):
            sip.bootstrap_inbound_call(settings, "call-+49999000", [])

    def test_default_site_used_when_unmapped(self):
        settings = _settings(default_site="front-desk")
        ctx = sip.bootstrap_inbound_call(settings, "call-+49999000", [])
        assert ctx.site_slug == "front-desk"
        assert ctx.tenant_source == "default"


# --------------------------------------------------------------------------
# Deploy YAML schemas (safe_load)
# --------------------------------------------------------------------------
class TestDeployYaml:
    def test_dispatch_rule_schema(self):
        doc = yaml.safe_load((SIP_DIR / "dispatch-rule.yaml").read_text())
        assert doc["name"]
        rule = doc["rule"]["dispatchRuleCallee"]
        assert rule["roomPrefix"] == "call-"
        assert rule["randomize"] is False
        assert isinstance(doc.get("trunk_ids"), list)

    def test_trunk_config_schema(self):
        doc = yaml.safe_load((SIP_DIR / "trunk-config.example.yaml").read_text())
        assert doc["name"]
        numbers = doc["numbers"]
        assert isinstance(numbers, list) and numbers
        assert all(n.startswith("+") for n in numbers)

    def test_setup_script_exists_and_uses_lk(self):
        script = (SIP_DIR / "setup.sh").read_text()
        assert "sip inbound create" in script
        assert "sip dispatch create" in script
        assert "LK_URL" in script and "LK_KEY" in script and "LK_SECRET" in script
