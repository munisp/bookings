import { NextRequest, NextResponse } from "next/server";
import { auth } from "@/lib/auth";

const API_BASE = process.env.API_BASE_URL ?? "http://localhost:9080";

async function proxy(req: NextRequest): Promise<NextResponse> {
  return NextResponse.json({ base: API_BASE, method: req.method });
}

export const GET = proxy;
