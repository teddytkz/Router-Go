import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "./components/Layout";
import { Skeleton } from "./components/ui";
import { useAuthStatus } from "./lib/hooks";
import Combos from "./pages/Combos";
import ApiKeys from "./pages/ApiKeys";
import OAuthCallback from "./pages/OAuthCallback";
import Login from "./pages/Login";
import Models from "./pages/Models";
import Dashboard from "./pages/Dashboard";
import ProviderDetail from "./pages/ProviderDetail";
import Providers from "./pages/Providers";
import SettingsPage from "./pages/Settings";
import TokenSaver from "./pages/TokenSaver";
import ConsoleLog from "./pages/ConsoleLog";
import ProxyPools from "./pages/ProxyPools";
import Quota from "./pages/Quota";
import Usage from "./pages/Usage";

export default function App() {
  const { data, isLoading } = useAuthStatus();
  if (isLoading) return <div className="p-10"><Skeleton className="h-8 w-48" /></div>;
  const authed = data?.authenticated ?? false;
  return (
    <Routes>
      <Route path="/oauth/callback" element={<OAuthCallback />} />
      <Route path="/login" element={authed ? <Navigate to="/dashboard" replace /> : <Login />} />
      <Route path="/dashboard" element={authed ? <Layout /> : <Navigate to="/login" replace />}>
        <Route index element={<Dashboard />} />
        <Route path="providers" element={<Providers />} />
        <Route path="providers/:id" element={<ProviderDetail />} />
        <Route path="combos" element={<Combos />} />
        <Route path="model-masking" element={<Models />} />
        <Route path="models" element={<Navigate to="/dashboard/model-masking" replace />} />
        <Route path="token-saver" element={<TokenSaver />} />
        <Route path="api-keys" element={<ApiKeys />} />
        <Route path="console-log" element={<ConsoleLog />} />
        <Route path="usage" element={<Usage />} />
        <Route path="proxy-pools" element={<ProxyPools />} />
        <Route path="quota" element={<Quota />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
