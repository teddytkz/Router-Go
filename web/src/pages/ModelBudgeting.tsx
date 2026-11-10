import { CustomLimits } from "../components/quota/CustomLimits";
import { PageHeader } from "../components/ui";

export default function ModelBudgeting() {
  return (
    <>
      <PageHeader title="Model Budgeting" subtitle="Your own request and token budgets per provider or API key" />
      <CustomLimits />
    </>
  );
}
