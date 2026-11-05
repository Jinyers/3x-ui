import { useCallback, useEffect, useState } from 'react';
import { useWebSocket } from '@/hooks/useWebSocket';
import { TRAFFIC_POLL_INTERVAL_S } from '@/lib/traffic/poll-interval';

export const LIVE_TRAFFIC_POINTS = 72;

interface Sample {
  at: number;
  up: number;
  down: number;
}
interface History {
  local: Sample[];
  nodes: Sample[];
  persistenceDisabled: boolean;
  stale: boolean;
  localStale: boolean;
  nodesStale: boolean;
}
interface Traffic {
  IsInbound?: boolean;
  Up?: number;
  Down?: number;
}

export function appendTrafficSample(previous: Sample[], rows: Traffic[], now: number): Sample[] {
  let up = 0;
  let down = 0;
  for (const row of rows) {
    if (!row || row.IsInbound === false) continue;
    if (typeof row.Up === 'number' && Number.isFinite(row.Up)) up += Math.max(0, row.Up);
    if (typeof row.Down === 'number' && Number.isFinite(row.Down)) down += Math.max(0, row.Down);
  }
  return [
    ...previous,
    { at: now, up: up / TRAFFIC_POLL_INTERVAL_S, down: down / TRAFFIC_POLL_INTERVAL_S },
  ].slice(-LIVE_TRAFFIC_POINTS);
}

// This window belongs to the open page only: no localStorage, IndexedDB, or
// server history writes. Local and node frames have independent poll clocks.
export function useLiveTrafficHistory(): History {
  const [history, setHistory] = useState<History>({
    local: [],
    nodes: [],
    persistenceDisabled: false,
    stale: true,
    localStale: true,
    nodesStale: true,
  });
  const traffic = useCallback((payload: unknown) => {
    if (!payload || typeof payload !== 'object') return;
    const p = payload as {
      traffics?: Traffic[];
      nodeTraffics?: Traffic[];
      persistenceDisabled?: boolean;
    };
    if (!Array.isArray(p.traffics) && !Array.isArray(p.nodeTraffics)) return;
    const now = Date.now();
    setHistory((previous) => ({
      local: Array.isArray(p.traffics)
        ? appendTrafficSample(previous.local, p.traffics, now)
        : previous.local,
      nodes: Array.isArray(p.nodeTraffics)
        ? appendTrafficSample(previous.nodes, p.nodeTraffics, now)
        : previous.nodes,
      persistenceDisabled: p.persistenceDisabled ?? previous.persistenceDisabled,
      stale: false,
      localStale: Array.isArray(p.traffics) ? false : previous.localStale,
      nodesStale: Array.isArray(p.nodeTraffics) ? false : previous.nodesStale,
    }));
  }, []);
  useWebSocket({ traffic });
  const lastLocalAt = history.local.at(-1)?.at;
  const lastNodeAt = history.nodes.at(-1)?.at;
  useEffect(() => {
    const now = Date.now();
    const timers: number[] = [];
    for (const [scope, at] of [
      ['local', lastLocalAt],
      ['nodes', lastNodeAt],
    ] as const) {
      if (at === undefined) continue;
      timers.push(
        window.setTimeout(
          () =>
            setHistory((p) => {
              if (scope === 'local')
                return p.localStale ? p : { ...p, localStale: true, stale: p.nodesStale };
              return p.nodesStale ? p : { ...p, nodesStale: true, stale: p.localStale };
            }),
          Math.max(0, at + TRAFFIC_POLL_INTERVAL_S * 2500 - now),
        ),
      );
    }
    return () => timers.forEach((timer) => window.clearTimeout(timer));
  }, [lastLocalAt, lastNodeAt]);
  return history;
}
