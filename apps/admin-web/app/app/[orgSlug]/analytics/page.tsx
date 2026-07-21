import { AnalyticsClient } from "./analytics-client";

export const metadata = { title: "Analytics" };

export default async function AnalyticsPage({
  params,
}: {
  params: Promise<{ orgSlug: string }>;
}) {
  const { orgSlug } = await params;
  return <AnalyticsClient orgSlug={orgSlug} />;
}
