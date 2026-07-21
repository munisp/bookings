import { auth } from "@/lib/auth";
import { BookingsClient } from "./bookings-client";

export const metadata = { title: "Bookings" };

export default async function BookingsPage({
  params,
}: {
  params: Promise<{ orgSlug: string }>;
}) {
  const { orgSlug } = await params;
  const session = await auth();
  return (
    <BookingsClient orgSlug={orgSlug} token={session?.accessToken} />
  );
}
