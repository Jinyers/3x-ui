import { act, renderHook } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { LIVE_TRAFFIC_POINTS, useLiveTrafficHistory } from '@/pages/index/useLiveTrafficHistory';

const { handlers } = vi.hoisted(() => ({ handlers: { traffic: (_payload: unknown) => {} } }));
vi.mock('@/hooks/useWebSocket', () => ({
  useWebSocket: (next: typeof handlers) => {
    handlers.traffic = next.traffic;
  },
}));

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

it('streams local and node speed into separate bounded browser windows without storage writes', () => {
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  const { result } = renderHook(useLiveTrafficHistory);
  act(() => {
    for (let n = 0; n < LIVE_TRAFFIC_POINTS + 20; n++) {
      handlers.traffic({
        persistenceDisabled: true,
        traffics: [
          { IsInbound: true, Up: 100, Down: 200 },
          { IsInbound: false, Up: 100, Down: 200 },
        ],
      });
    }
    handlers.traffic({ nodeTraffics: [{ IsInbound: true, Up: 50, Down: 100 }] });
  });
  expect(result.current.local).toHaveLength(LIVE_TRAFFIC_POINTS);
  expect(result.current.nodes).toHaveLength(1);
  expect(result.current.local.at(-1)).toMatchObject({ up: 20, down: 40 });
  expect(result.current.nodes.at(-1)).toMatchObject({ up: 10, down: 20 });
  expect(result.current.persistenceDisabled).toBe(true);
  expect(storage).not.toHaveBeenCalled();
  act(() => handlers.traffic({ traffics: [] }));
  expect(result.current.local.at(-1)).toMatchObject({ up: 0, down: 0 });
  expect(result.current.nodes).toHaveLength(1);
  act(() => handlers.traffic({ traffics: [], persistenceDisabled: false }));
  expect(result.current.persistenceDisabled).toBe(false);
});

it('marks a lost live stream stale and clears its window on page unmount', () => {
  vi.useFakeTimers();
  const hook = renderHook(useLiveTrafficHistory);
  act(() => handlers.traffic({ traffics: [{ Up: 100, Down: 200 }] }));
  expect(hook.result.current.stale).toBe(false);
  act(() => {
    vi.advanceTimersByTime(13000);
  });
  expect(hook.result.current.stale).toBe(true);
  hook.unmount();
  const next = renderHook(useLiveTrafficHistory);
  expect(next.result.current.local).toEqual([]);
});
