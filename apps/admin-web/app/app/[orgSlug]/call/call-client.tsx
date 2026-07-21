"use client";

import * as React from "react";
import Link from "next/link";
import { Mic, MicOff, PhoneOff } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { ErrorNote } from "@/components/error-note";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

const LIVEKIT_URL = process.env.NEXT_PUBLIC_LIVEKIT_URL ?? "ws://localhost:7880";

type CallState = "joining" | "connected" | "ended" | "error";

/**
 * Staff warm-handoff join page (innovation 1). Reached from the
 * EscalationRequested dashboard toast with the LiveKit room name and the
 * staff join token minted by the voice runtime.
 */
export function CallClient({
  orgSlug,
  room,
  token,
}: {
  orgSlug: string;
  room: string;
  token: string;
}) {
  const [state, setState] = React.useState<CallState>("joining");
  const [muted, setMuted] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const roomRef = React.useRef<import("livekit-client").Room | null>(null);

  React.useEffect(() => {
    if (!room || !token) {
      setError("Missing room or join token — open this page from an escalation toast.");
      setState("error");
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const { Room, RoomEvent } = await import("livekit-client");
        const lk = new Room();
        roomRef.current = lk;
        lk.on(RoomEvent.Disconnected, () => setState("ended"));
        await lk.connect(LIVEKIT_URL, token);
        await lk.localParticipant.setMicrophoneEnabled(true);
        if (!cancelled) setState("connected");
      } catch (e) {
        if (!cancelled) {
          setError(
            `Could not join the escalation room: ${e instanceof Error ? e.message : String(e)}. Check microphone permissions and that LiveKit is running.`,
          );
          setState("error");
        }
      }
    })();
    return () => {
      cancelled = true;
      void roomRef.current?.disconnect();
      roomRef.current = null;
    };
  }, [room, token]);

  const toggleMute = async () => {
    const lk = roomRef.current;
    if (!lk) return;
    const next = !muted;
    await lk.localParticipant.setMicrophoneEnabled(!next);
    setMuted(next);
  };

  const hangUp = async () => {
    await roomRef.current?.disconnect();
    roomRef.current = null;
    setState("ended");
  };

  return (
    <div className="max-w-xl">
      <PageHeader
        title="Escalation call"
        description={`Warm handoff from the AI receptionist · room ${room || "—"}`}
      />
      {error ? <ErrorNote message={error} /> : null}
      <Card>
        <CardHeader>
          <CardTitle>
            {state === "connected"
              ? "You are live with the caller"
              : state === "joining"
                ? "Connecting…"
                : state === "ended"
                  ? "Call ended"
                  : "Could not join"}
          </CardTitle>
          <CardDescription>
            {state === "connected"
              ? "The AI receptionist stays on the line as whisper-copilot with suggested replies."
              : "The caller was told a human is joining."}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex items-center gap-3">
          {state === "connected" ? (
            <>
              <Button variant="outline" onClick={() => void toggleMute()}>
                {muted ? <MicOff className="h-4 w-4" /> : <Mic className="h-4 w-4" />}
                {muted ? "Unmute" : "Mute"}
              </Button>
              <Button variant="destructive" onClick={() => void hangUp()}>
                <PhoneOff className="h-4 w-4" /> End call
              </Button>
            </>
          ) : state === "ended" || state === "error" ? (
            <Link href={`/app/${orgSlug}/bookings`}>
              <Button variant="outline">Back to bookings</Button>
            </Link>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}
