"use client";

import * as React from "react";
import { Mic, PhoneOff } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import type { VoiceSession } from "@/lib/types";

type CallState = "idle" | "joining" | "connected" | "error";

/**
 * "Talk to receptionist" voice button.
 *
 * STUB (clearly bounded): obtains a LiveKit token from the voice runtime via
 * POST /voice/session (same-origin via the Next.js /voice/* rewrite) and
 * connects a `livekit-client` Room with the mic enabled. The surrounding UX
 * (waveform, transcripts, mute, volume) is intentionally minimal — this is
 * the integration seam for the full voice UI.
 */
export function VoiceSessionButton({
  tenant,
  siteSlug,
  accent = "#7c5b3e",
}: {
  tenant: string;
  siteSlug: string;
  accent?: string;
}) {
  const [state, setState] = React.useState<CallState>("idle");
  const [error, setError] = React.useState<string | null>(null);
  const roomRef = React.useRef<import("livekit-client").Room | null>(null);

  const hangUp = React.useCallback(async () => {
    try {
      await roomRef.current?.disconnect();
    } finally {
      roomRef.current = null;
      setState("idle");
    }
  }, []);

  React.useEffect(() => {
    return () => {
      void roomRef.current?.disconnect();
    };
  }, []);

  const start = async () => {
    setState("joining");
    setError(null);
    try {
      // 1. Mint a LiveKit access token via the voice runtime (through APISIX).
      const session = await api.post<VoiceSession>("/voice/session", {
        tenant,
        site_slug: siteSlug,
      });

      // 2. Connect the LiveKit room (livekit-client, dynamic import so the
      //    SDK stays out of the initial public-page bundle).
      const { Room, RoomEvent } = await import("livekit-client");
      const room = new Room();
      roomRef.current = room;
      room.on(RoomEvent.Disconnected, () => setState("idle"));
      await room.connect(session.url, session.token);
      await room.localParticipant.setMicrophoneEnabled(true);
      setState("connected");
    } catch (e) {
      setError(
        e instanceof ApiError
          ? e.message
          : "Could not start the voice session. Check microphone permissions.",
      );
      setState("error");
      roomRef.current = null;
    }
  };

  if (state === "connected" || state === "joining") {
    return (
      <button
        onClick={() => void hangUp()}
        disabled={state === "joining"}
        className="inline-flex items-center gap-2 rounded-full bg-destructive px-4 py-2 text-sm font-medium text-destructive-foreground cursor-pointer disabled:opacity-60"
      >
        <PhoneOff className="h-4 w-4" />
        {state === "joining" ? "Connecting…" : "End call"}
      </button>
    );
  }

  return (
    <span className="inline-flex flex-col items-start gap-1">
      <button
        onClick={() => void start()}
        className="inline-flex items-center gap-2 rounded-full px-4 py-2 text-sm font-medium text-white cursor-pointer"
        style={{ backgroundColor: accent }}
      >
        <Mic className="h-4 w-4" />
        Talk to receptionist
      </button>
      {state === "error" && error ? (
        <span className="text-xs text-destructive">{error}</span>
      ) : null}
    </span>
  );
}
