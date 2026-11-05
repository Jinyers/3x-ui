import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import PolicyRoutingPage from '@/pages/policy-routing/PolicyRoutingPage';

const { get, post, confirm } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  confirm: vi.fn(),
}));
vi.mock('@/layouts/AppSidebar', () => ({ default: () => null }));
vi.mock('@/hooks/useTheme', () => ({ useTheme: () => ({ antdThemeConfig: {} }) }));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('@/utils', async (original) => ({
  ...(await original<typeof import('@/utils')>()),
  HttpUtil: { get, post },
}));
vi.mock('antd', async (original) => {
  const antd = await original<typeof import('antd')>();
  return { ...antd, Modal: { ...antd.Modal, useModal: () => [{ confirm }, null] } };
});

function view(enabledByEnv = true) {
  return {
    enabledByEnv,
    config: {
      enabled: false,
      ingress: 'eth0',
      tun: 'xray0',
      uplink: 'eth0',
      gateway: '',
      rules: [],
    },
    state: {
      ready: enabledByEnv,
      forwardingEnabled: true,
      applied: false,
      waitingForTun: false,
      interfaces: ['eth0', 'xray0'],
    },
  };
}

beforeEach(() => {
  get.mockReset();
  post.mockReset();
  confirm.mockReset();
});

it('keeps edits in the browser and applies only the policy confirmed by the user', async () => {
  get.mockResolvedValue({ success: true, obj: view() });
  post.mockResolvedValue({ success: true, obj: 'validated policy preview' });
  render(<PolicyRoutingPage />);
  await waitFor(() =>
    expect(
      (screen.getByRole('button', { name: 'pages.policy.apply' }) as HTMLButtonElement).disabled,
    ).toBe(false),
  );
  fireEvent.click(screen.getByRole('button', { name: 'pages.policy.addRule' }));
  const source = screen.getByRole('textbox', { name: 'pages.policy.source' });
  fireEvent.change(source, { target: { value: '192.0.2.0/24' } });
  for (const toggle of screen.getAllByRole('switch', { name: 'pages.policy.enabled' }))
    fireEvent.click(toggle);
  fireEvent.click(screen.getByRole('button', { name: 'pages.policy.refresh' }));
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  expect((source as HTMLInputElement).value).toBe('192.0.2.0/24');
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: 'pages.policy.apply' }));
  await waitFor(() => expect(confirm).toHaveBeenCalledOnce());
  expect(post).toHaveBeenCalledTimes(1);
  expect(post.mock.calls[0]?.[0]).toBe('/panel/api/policy-routing/preview');
  const planned = post.mock.calls[0]?.[1];
  expect(planned).toMatchObject({
    enabled: true,
    tun: 'xray0',
    rules: [{ enabled: true, source: '192.0.2.0/24', action: 'tun' }],
  });
  await act(async () => {
    await confirm.mock.calls[0]?.[0].onOk();
  });
  expect(post.mock.calls[1]).toEqual(['/panel/api/policy-routing/apply', planned]);
});

it('disables kernel application when the container feature is not enabled', async () => {
  get.mockResolvedValue({ success: true, obj: view(false) });
  render(<PolicyRoutingPage />);
  await waitFor(() => expect(get).toHaveBeenCalled());
  expect(
    (screen.getByRole('button', { name: 'pages.policy.apply' }) as HTMLButtonElement).disabled,
  ).toBe(true);
  expect(post).not.toHaveBeenCalled();
});
