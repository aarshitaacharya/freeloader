import { useCallback, useEffect, useRef, useState } from "react";

export interface AsyncState<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  reload: () => void;
}

/** Runs `fn` on mount, when `deps` change, and every `intervalMs`; ignores stale results. */
export function useAsync<T>(fn: () => Promise<T>, deps: readonly unknown[], intervalMs?: number): AsyncState<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<Error>();
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  const latest = useRef(0);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    const id = ++latest.current;
    setLoading(true);
    fnRef
      .current()
      .then((v) => {
        if (id !== latest.current) return;
        setData(v);
        setError(undefined);
      })
      .catch((e: unknown) => {
        if (id !== latest.current) return;
        setError(e instanceof Error ? e : new Error(String(e)));
      })
      .finally(() => {
        if (id === latest.current) setLoading(false);
      });
  }, [...deps, tick]);

  useEffect(() => {
    if (!intervalMs) return;
    const t = setInterval(() => {
      setTick((n) => n + 1);
    }, intervalMs);
    return () => {
      clearInterval(t);
    };
  }, [intervalMs]);

  const reload = useCallback(() => {
    setTick((n) => n + 1);
  }, []);
  return { data, error, loading, reload };
}
