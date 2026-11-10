import { ProviderLimits } from "../components/quota/ProviderLimits";
import { PageHeader } from "../components/ui";

export default function Quota() {
  return (
    <>
      <PageHeader title="Quota Tracker" subtitle="Live account limits per provider" />
      <ProviderLimits />
    </>
  );
}
