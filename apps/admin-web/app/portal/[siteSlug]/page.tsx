import { notFound } from "next/navigation";
import { serverApi } from "@/lib/server-api";
import { ApiError } from "@/lib/api";
import { PortalClient } from "./portal-client";
import type { PublicSite } from "@/lib/types";

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
    return { title: `My Bookings · ${site.business_name}` };
  } catch {
    return { title: "Customer Portal" };
  }
}

export default async function PortalPage({
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

  return <PortalClient site={site} />;
}
