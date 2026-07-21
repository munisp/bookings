import { NextRequest, NextResponse } from "next/server";
import { auth } from "@/lib/auth";

/**
 * BFF proxy: forwards /api/* to the APISIX gateway with the caller's Keycloak
 * access token attached. The gateway routes /api/bookings/*, /api/payments/*,
 * /api/knowledge/*, /voice/* etc. to the backing services (SPEC §12).
 *
 * Requests without a session are forwarded anonymously, which is what the
 * public booking endpoints (/api/bookings/public/*) and /voice/chat expect.
 */

const API_BASE =
  process.env.API_BASE_URL ??
  process.env.NEXT_PUBLIC_API_BASE ??
  "http://localhost:9080";

/**
 * APISIX has no /api/analytics route (the analytics-pipeline service is not
 * exposed through the gateway). Browser code therefore calls
 * /api/analytics/<rest> and the BFF forwards it directly to the service
 * (docker network name `analytics`, port 7009) — see SPEC-W3 §5.
 */
const ANALYTICS_BASE =
  process.env.ANALYTICS_BASE_URL ?? "http://localhost:7009";

const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "transfer-encoding",
  "upgrade",
  "host",
  "content-length",
]);

async function proxy(
  req: NextRequest,
  ctx: { params: Promise<{ path?: string[] }> },
): Promise<NextResponse> {
  const { path = [] } = await ctx.params;
  const directToAnalytics = path[0] === "analytics";
  const target = new URL(
    directToAnalytics
      ? `${ANALYTICS_BASE}/${path.slice(1).join("/")}`
      : `${API_BASE}/${path.join("/")}`,
  );
  target.search = req.nextUrl.search;

  const headers = new Headers();
  req.headers.forEach((value, key) => {
    const lower = key.toLowerCase();
    if (HOP_BY_HOP.has(lower) || lower === "authorization" || lower === "cookie") return;
    headers.set(key, value);
  });
  if (!headers.has("accept")) headers.set("accept", "application/json");

  const session = await auth();
  if (session?.accessToken) {
    headers.set("authorization", `Bearer ${session.accessToken}`);
  }

  const hasBody = !["GET", "HEAD"].includes(req.method);
  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: req.method,
      headers,
      body: hasBody ? await req.text() : undefined,
      cache: "no-store",
      // @ts-expect-error -- Node fetch supports duplex for streaming bodies
      duplex: hasBody ? "half" : undefined,
    });
  } catch (err) {
    return NextResponse.json(
      {
        error: "gateway_unreachable",
        message: `Could not reach ${directToAnalytics ? `analytics service at ${ANALYTICS_BASE}` : `API gateway at ${API_BASE}`}: ${String(err)}`,
      },
      { status: 502 },
    );
  }

  const body = await upstream.arrayBuffer();
  const res = new NextResponse(body, { status: upstream.status });
  const contentType = upstream.headers.get("content-type");
  if (contentType) res.headers.set("content-type", contentType);
  return res;
}

export const GET = proxy;
export const POST = proxy;
export const PUT = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
export const dynamic = "force-dynamic";
