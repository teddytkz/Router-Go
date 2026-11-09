import { useEffect, useState } from "react";
import { OAUTH_CHANNEL } from "../components/providers/OAuthModal";

/** Landing page of a "code" sign-in: relays the code to the dashboard tab that started it, then closes itself. */
export default function OAuthCallback() {
  const [msg, setMsg] = useState("Finishing sign-in…");
  useEffect(() => {
    const q = new URLSearchParams(location.search);
    const provider = q.get("provider") ?? "", code = q.get("code") ?? "", error = q.get("error_description") ?? q.get("error") ?? "";
    if (!code && !error) { setMsg("No sign-in code in this address."); return; }
    const ch = new BroadcastChannel(OAUTH_CHANNEL);
    ch.postMessage({ provider, code, error });
    ch.close();
    setMsg(error ? `Sign-in failed: ${error}` : "Signed in. You can close this tab and return to the dashboard.");
    if (!error) setTimeout(() => window.close(), 1200);
  }, []);
  return <main className="min-h-screen grid place-items-center p-6 text-center"><p className="max-w-md text-[15px]">{msg}</p></main>;
}
