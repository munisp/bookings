import { TeamClient } from "./team-client";

export const metadata = { title: "Team" };

export default async function TeamPage({
  params,
}: {
  params: Promise<{ orgSlug: string }>;
}) {
  const { orgSlug } = await params;
  return <TeamClient orgSlug={orgSlug} />;
}
