import { auth } from "@/lib/auth";
import { NextResponse } from "next/server";

/**
 * Protects the tenant dashboard (/app/*). Unauthenticated users are sent to
 * /sign-in with a callback URL. The public booking page (/p/*), marketing
 * page and API routes stay open.
 */
export default auth((req) => {
  if (!req.auth) {
    const signIn = new URL("/sign-in", req.nextUrl.origin);
    signIn.searchParams.set(
      "callbackUrl",
      req.nextUrl.pathname + req.nextUrl.search,
    );
    return NextResponse.redirect(signIn);
  }
  return NextResponse.next();
});

export const config = {
  matcher: ["/app/:path*"],
};
