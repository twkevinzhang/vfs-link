import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

export function useFileSelection(paths: string[]) {
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const anchorRef = useRef<string | undefined>(undefined);

  useEffect(() => {
    const visible = new Set(paths);
    setSelected(
      (current) => new Set([...current].filter((path) => visible.has(path)))
    );
    if (anchorRef.current && !visible.has(anchorRef.current))
      anchorRef.current = undefined;
  }, [paths]);

  const clear = useCallback(() => {
    anchorRef.current = undefined;
    setSelected(new Set());
  }, []);

  const select = useCallback(
    (path: string, options: { toggle?: boolean; range?: boolean } = {}) => {
      if (options.range && anchorRef.current) {
        const start = paths.indexOf(anchorRef.current);
        const end = paths.indexOf(path);
        if (start >= 0 && end >= 0) {
          const range = paths.slice(
            Math.min(start, end),
            Math.max(start, end) + 1
          );
          setSelected(
            options.toggle
              ? (current) => new Set([...current, ...range])
              : new Set(range)
          );
          return;
        }
      }
      anchorRef.current = path;
      if (options.toggle) {
        setSelected((current) => {
          const next = new Set(current);
          if (next.has(path)) next.delete(path);
          else next.add(path);
          return next;
        });
      } else {
        setSelected(new Set([path]));
      }
    },
    [paths]
  );

  const selectAll = useCallback(() => {
    if (paths.length > 0) anchorRef.current = paths[0];
    setSelected(new Set(paths));
  }, [paths]);

  return useMemo(
    () => ({
      selected,
      selectedPaths: [...selected],
      clear,
      select,
      selectAll,
      setSelected,
    }),
    [clear, select, selectAll, selected]
  );
}
