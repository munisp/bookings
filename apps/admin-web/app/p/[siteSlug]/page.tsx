import { notFound } from "next/navigation";
import { serverApi, unwrapList } from "@/lib/server-api";
import { ApiError } from "@/lib/api";
import { PublicBookingClient } from "./public-booking-client";
import type { Offering, PublicSite } from "@/lib/types";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ siteSlug: string }>;
}) {
  const { siteSlug } = await params;
  try {
    const site = await serverApi<PublicSite>(
      `/api/bookings/public/sites/${siteSlug}`,
      { anonymous: true },
    );
    const brand =
      site.theme?.brandName ?? site.theme?.brand_name ?? site.business_name;
    return { title: `Book · ${brand}` };
  } catch {
    return { title: "Booking" };
  }
}

export default async function PublicBookingPage({
  params,
}: {
  params: Promise<{ siteSlug: string }>;
}) {
  const { siteSlug } = await params;

  let site: PublicSite;
  try {
    site = await serverApi<PublicSite>(
      `/api/bookings/public/sites/${siteSlug}`,
      { anonymous: true },
    );
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound();
    throw e;
  }
  if (!site.published) notFound();

  let offerings: Offering[] = [];
  try {
    const data = await serverApi<unknown>(
      `/api/bookings/public/sites/${siteSlug}/offerings`,
      { anonymous: true },
    );
    offerings = unwrapList<Offering>(data).filter((o) => o.bookable);
  } catch {
    offerings = [];
  }

  return <PublicBookingClient site={site} offerings={offerings} />;
}
