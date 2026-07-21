"use client";

import { useEffect, useRef, useState } from "react";
import type { WsEvent } from "@/lib/types";

/**
 * Live booking events from gateway-edge, fanned out over WebSocket via APISIX:
 *   {NEXT_PUBLIC_WS_BASE}/ws?tenant={slug}&token={accessToken}
 * NEXT_PUBLIC_WS_BASE may already end in "/ws" (see docker-compose); it is
 * normalised here either way. Reconnects with capped exponential backoff.
 */

function wsUrl(tenant: string, token: string): string {
  const raw =
    process.env.NEXT_PUBLIC_WS_BASE ?? "ws://localhost:9080/ws";
  const base = raw.replace(/\/+$/, "");
  const withPath = base.endsWith("/ws") ? base : `${base}/ws`;
  const params = new URLSearchParams({ tenant, token });
  return `${withPath}?${params.toString()}`;
}

export interface BookingEventsState {
  connected: boolean;
}

/**
 * The /ws channel fans out booking events (BookingCreated etc.) and, since
 * Wave 3, EscalationRequested events from the voice runtime warm handoff.
 */
export function useBookingEvents(
  tenant: string,
  token: string | undefined,
  onEvent: (event: WsEvent) => void,
): BookingEventsState {
  const [connected, setConnected] = useState(false);
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;

  useEffect(() => {
    if (!tenant || !token) return;

    let ws: WebSocket | null = null;
    let closed = false;
    let attempts = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const connect = () => {
      if (closed) return;
      try {
        ws = new WebSocket(wsUrl(tenant, token));
      } catch {
        schedule();
        return;
      }

      ws.onopen = () => {
        attempts = 0;
        setConnected(true);
      };
      ws.onmessage = (msg) => {
        try {
          const event = JSON.parse(String(msg.data)) as WsEvent;
          handlerRef.current(event);
        } catch {
          // ignore malformed frames
        }
      };
      ws.onclose = () => {
        setConnected(false);
        schedule();
      };
      ws.onerror = () => {
        ws?.close();
      };
    };

    const schedule = () => {
      if (closed) return;
      attempts += 1;
      const delay = Math.min(15000, 500 * 2 ** attempts);
      timer = setTimeout(connect, delay);
    };

    connect();

    return () => {
      closed = true;
      setConnected(false);
      if (timer) clearTimeout(timer);
      ws?.close();
    };
  }, [tenant, token]);

  return { connected };
}
