import { BillingClient } from "./billing-client";

export const metadata = { title: "Billing" };

export default async function BillingPage({
  params,
}: {
  params: Promise<{ orgSlug: string }>;
}) {
  const { orgSlug } = await params;
  return <BillingClient orgSlug={orgSlug} />;
}
