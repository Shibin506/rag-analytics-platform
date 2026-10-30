import { useEffect, useState } from "react";
import { DEMO_KEYS, setApiKey } from "./api";
import { Analytics } from "./pages/Analytics";
import { Assistant } from "./pages/Assistant";
import { Documents } from "./pages/Documents";
import { Overview } from "./pages/Overview";
import { Schemas } from "./pages/Schemas";

const PAGES = {
  overview: { label: "Overview", el: Overview },
  analytics: { label: "Analytics", el: Analytics },
  assistant: { label: "Assistant", el: Assistant },
  schemas: { label: "Schemas", el: Schemas },
  documents: { label: "Documents", el: Documents },
} as const;
type PageKey = keyof typeof PAGES;
type Tenant = keyof typeof DEMO_KEYS;

function stored<T extends string>(key: string, fallback: T, allowed: readonly string[]): T {
  try {
    const v = localStorage.getItem(key);
    return v && allowed.includes(v) ? (v as T) : fallback;
  } catch {
    return fallback;
  }
}

function remember(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // storage unavailable (private mode); the choice just isn't remembered
  }
}

export function App() {
  const [page, setPage] = useState<PageKey>(() => {
    const h = location.hash.slice(1);
    return h in PAGES ? (h as PageKey) : "overview";
  });
  const [tenant, setTenant] = useState<Tenant>(() => stored("lumen.tenant", "acme", Object.keys(DEMO_KEYS)));
  const [theme, setTheme] = useState<string>(() => stored("lumen.theme", "system", ["system", "light", "dark"]));

  setApiKey(DEMO_KEYS[tenant]);

  useEffect(() => {
    if (theme === "system") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", theme);
    remember("lumen.theme", theme);
  }, [theme]);

  useEffect(() => {
    const onHash = () => {
      const h = location.hash.slice(1);
      if (h in PAGES) setPage(h as PageKey);
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  const Page = PAGES[page].el;
  return (
    <div className="shell">
      <header className="top">
        <div className="brand">Lumen<small>RAG analytics & client integration</small></div>
        <nav className="tabs">
          {(Object.keys(PAGES) as PageKey[]).map((k) => (
            <button key={k} aria-current={k === page ? "page" : undefined}
                    onClick={() => { location.hash = k; setPage(k); }}>{PAGES[k].label}</button>
          ))}
        </nav>
        <span className="spacer" />
        <div className="controls">
          <label className="meta" htmlFor="tenant">Tenant</label>
          <select id="tenant" value={tenant} onChange={(e) => { setTenant(e.target.value as Tenant); remember("lumen.tenant", e.target.value); }}>
            {Object.keys(DEMO_KEYS).map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
          <select aria-label="Theme" value={theme} onChange={(e) => setTheme(e.target.value)}>
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </select>
        </div>
      </header>
      <Page key={tenant} tenant={tenant} />
    </div>
  );
}
