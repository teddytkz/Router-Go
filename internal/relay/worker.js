// Router-Go relay: forwards a request to x-relay-target (+ x-relay-path) so the upstream sees this host's IP.
// Every call must carry the shared key; nothing is logged or stored here.
const KEY = "__RELAY_KEY__";
const DROP = /^(x-relay-|host$|content-length$|x-forwarded-|x-real-ip$|x-vercel-|cf-|cdn-loop$|forwarded$|via$)/i;

async function relay(req) {
  if (req.headers.get("x-relay-key") !== KEY) return new Response("forbidden", { status: 403 });
  if (new URL(req.url).pathname === "/health") return new Response("ok");
  const target = req.headers.get("x-relay-target"), path = req.headers.get("x-relay-path") || "/";
  let origin;
  try { origin = new URL(target).origin; } catch { return new Response("bad x-relay-target", { status: 400 }); }
  if (!/^https?:/.test(origin) || !path.startsWith("/") || path.startsWith("//")) return new Response("bad target", { status: 400 });
  const headers = new Headers();
  for (const [k, v] of req.headers) if (!DROP.test(k)) headers.set(k, v);
  const hasBody = req.method !== "GET" && req.method !== "HEAD";
  try {
    const res = await fetch(origin + path, { method: req.method, headers, body: hasBody ? await req.arrayBuffer() : undefined, redirect: "manual" });
    const out = new Headers(res.headers);
    out.delete("content-encoding"); out.delete("content-length"); // fetch already decoded the body
    return new Response(res.body, { status: res.status, headers: out });
  } catch (e) {
    return new Response(JSON.stringify({ error: "relay fetch failed", details: String(e && e.message || e) }), { status: 502, headers: { "content-type": "application/json" } });
  }
}
