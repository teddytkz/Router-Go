import { Activity, Gauge, Hourglass, Terminal, KeyRound, Layers, Boxes, LayoutDashboard, Network, LogOut, Menu, Moon, Server, Settings, Sun, X } from "lucide-react";
import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useLogout } from "../lib/hooks";
import { cn, Button } from "./ui";

const sections = [
  { title: "", items: [
    { to: "/dashboard", label: "Dashboard", icon: LayoutDashboard, end: true },
    { to: "/dashboard/providers", label: "Providers", icon: Server },
    { to: "/dashboard/combos", label: "Combos", icon: Layers },
    { to: "/dashboard/usage", label: "Usage", icon: Activity },
    { to: "/dashboard/token-saver", label: "Token Saver", icon: Gauge },
    { to: "/dashboard/quota", label: "Quota Tracker", icon: Hourglass },
  ] },
  { title: "System", items: [
    { to: "/dashboard/model-masking", label: "Model Masking", icon: Boxes },
    { to: "/dashboard/proxy-pools", label: "Proxy Pools", icon: Network },
    { to: "/dashboard/api-keys", label: "API Keys", icon: KeyRound },
    { to: "/dashboard/console-log", label: "Console Log", icon: Terminal },
    { to: "/dashboard/settings", label: "Settings", icon: Settings },
  ] },
];

function useTheme() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const toggle = () => {
    const next = !dark;
    document.documentElement.classList.toggle("dark", next);
    try { localStorage.setItem("theme", next ? "dark" : "light"); } catch { /* private mode */ }
    setDark(next);
  };
  return [dark, toggle] as const;
}

export function Logo() {
  return (
    <div className="flex items-center gap-2.5">
      <div className="grid size-8 place-items-center rounded-lg bg-gradient-to-br from-[#8b6dff] to-[#5b3df5] text-white shadow-md">
        <svg viewBox="0 0 32 32" className="size-5" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round"><path d="M8 22V10h6.5a3.5 3.5 0 010 7H8m8 0l5 5" /></svg>
      </div>
      <span className="font-semibold tracking-tight text-[15px]">Router<span className="text-accent">-Go</span></span>
    </div>
  );
}

export default function Layout() {
  const [open, setOpen] = useState(false);
  const [dark, toggle] = useTheme();
  const logout = useLogout();
  const loc = useLocation();
  useEffect(() => setOpen(false), [loc.pathname]);

  const sidebar = (
    <div className="flex h-full flex-col">
      <div className="h-14 px-4 flex items-center border-b border-border"><Logo /></div>
      <nav className="flex-1 overflow-y-auto p-3 space-y-4" aria-label="Main">
        {sections.map((sec) => (
          <div key={sec.title || "main"} className="space-y-0.5">
            {sec.title && <div className="px-3 pb-1 text-[11px] font-semibold uppercase tracking-wider text-muted">{sec.title}</div>}
            {sec.items.map(({ to, label, icon: Icon, end }) => (
              <NavLink key={to} to={to} end={end}
                className={({ isActive }) => cn("flex items-center gap-3 rounded-lg px-3 h-9 text-sm font-medium transition-colors",
                  isActive ? "bg-accent-soft text-accent" : "text-muted hover:bg-surface-2 hover:text-text")}>
                <Icon className="size-[18px]" />{label}
              </NavLink>
            ))}
          </div>
        ))}
      </nav>
      <div className="p-3 border-t border-border flex items-center gap-1">
        <Button variant="ghost" size="sm" className="flex-1 justify-start" onClick={() => logout.mutate()}><LogOut className="size-4" />Sign out</Button>
        <Button variant="ghost" size="sm" onClick={toggle} aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}>
          {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
        </Button>
      </div>
    </div>
  );

  return (
    <div className="min-h-full lg:grid lg:grid-cols-[232px_1fr]">
      <aside className="hidden lg:block sticky top-0 h-screen border-r border-border bg-surface">{sidebar}</aside>
      {open && (
        <div className="lg:hidden fixed inset-0 z-40">
          <div className="absolute inset-0 bg-black/50" onClick={() => setOpen(false)} />
          <aside className="pop relative h-full w-64 bg-surface border-r border-border">{sidebar}</aside>
        </div>
      )}
      <div className="min-w-0">
        <header className="lg:hidden sticky top-0 z-30 h-14 flex items-center justify-between px-4 border-b border-border bg-surface/90 backdrop-blur">
          <Logo />
          <Button variant="ghost" size="sm" onClick={() => setOpen((o) => !o)} aria-label="Toggle menu">{open ? <X className="size-5" /> : <Menu className="size-5" />}</Button>
        </header>
        <main className="mx-auto max-w-6xl px-4 sm:px-6 py-6 sm:py-8"><Outlet /></main>
      </div>
    </div>
  );
}
