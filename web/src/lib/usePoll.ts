import { useCallback, useEffect, useRef, useState } from "react";

/** Load data now and every `ms` (0 = once); re-loads when `deps` change. */
export function usePoll<T>(load: () => Promise<T>, ms: number, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const loadRef = useRef(load);
  loadRef.current = load;

  const refresh = useCallback(async () => {
    try {
      setData(await loadRef.current());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    setData(null);
    void refresh();
    if (!ms) return;
    const id = setInterval(() => void refresh(), ms);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ms, refresh, ...deps]);

  return { data, error, refresh };
}
