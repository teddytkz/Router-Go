import { CustomLimits } from "../components/quota/CustomLimits";
import { FreeTierList } from "../components/quota/FreeTierList";
import { ProviderLimits } from "../components/quota/ProviderLimits";
import { PageHeader } from "../components/ui";

// Same single-scroll layout as XRouter's Quota Tracker (provider limits, then free tiers), plus Router-Go's own budgets at the bottom.
export default function Quota() {
  return (
    <>
      <PageHeader title="Quota Tracker" subtitle="Live account limits per provider, free tiers, and your own budgets" />
      <div className="flex flex-col gap-6">
        <ProviderLimits />
        <FreeTierList />
        <section aria-label="Custom limits"><CustomLimits /></section>
      </div>
    </>
  );
}
