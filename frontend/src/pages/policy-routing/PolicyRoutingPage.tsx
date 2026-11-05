import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  AutoComplete,
  Button,
  Card,
  Collapse,
  ConfigProvider,
  Input,
  Layout,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
  message,
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons';

import AppSidebar from '@/layouts/AppSidebar';
import { useTheme } from '@/hooks/useTheme';
import { HttpUtil, SizeFormatter } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';

interface Rule {
  enabled: boolean;
  name: string;
  source: string;
  destination: string;
  interface: string;
  protocol: string;
  sourcePort: string;
  destinationPort: string;
  action: string;
}

interface Policy {
  enabled: boolean;
  ingress: string;
  tun: string;
  uplink: string;
  gateway: string;
  rules: Rule[];
}

interface PolicyView {
  enabledByEnv: boolean;
  config: Policy;
  state: {
    ready: boolean;
    error?: string;
    applied: boolean;
    waitingForTun: boolean;
    forwardingEnabled: boolean;
    interfaces?: string[];
    ipRules?: unknown;
    ipRoutes?: unknown;
    nft?: unknown;
  };
}

const defaults: Policy = {
  enabled: false,
  ingress: 'eth0',
  tun: 'xray0',
  uplink: 'eth0',
  gateway: '',
  rules: [],
};
const emptyRule: Rule = {
  enabled: false,
  name: '',
  source: '',
  destination: '',
  interface: '',
  protocol: 'any',
  sourcePort: '',
  destinationPort: '',
  action: 'tun',
};

function packetCounters(raw: unknown): Map<number, { packets: number; bytes: number }> {
  const result = new Map<number, { packets: number; bytes: number }>();
  if (!raw || typeof raw !== 'object') return result;
  const entries = (raw as { nftables?: unknown[] }).nftables;
  if (!Array.isArray(entries)) return result;
  for (const entry of entries) {
    if (!entry || typeof entry !== 'object') continue;
    const rule = (
      entry as {
        rule?: { comment?: string; expr?: { counter?: { packets?: number; bytes?: number } }[] };
      }
    ).rule;
    if (!rule || !/^xui-rule:\d+$/.test(rule.comment ?? '') || !Array.isArray(rule.expr)) continue;
    for (const expr of rule.expr) {
      if (expr.counter)
        result.set(Number(rule.comment!.slice('xui-rule:'.length)), {
          packets: expr.counter.packets ?? 0,
          bytes: expr.counter.bytes ?? 0,
        });
    }
  }
  return result;
}

export default function PolicyRoutingPage() {
  const { t } = useTranslation();
  const { antdThemeConfig } = useTheme();
  const [draft, setDraft] = useState<Policy>(defaults);
  const [view, setView] = useState<PolicyView | null>(null);
  const [busy, setBusy] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const [messageApi, messageContext] = message.useMessage();
  const [modal, modalContext] = Modal.useModal();
  const refreshing = useRef(false);
  const latestLoad = useRef(0);
  const initialized = useRef(false);
  const counters = packetCounters(view?.state.nft);
  const countersCurrent = !!view && JSON.stringify(draft) === JSON.stringify(view.config);
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);

  const load = useCallback((replaceDraft = false) => {
    if (refreshing.current && !replaceDraft) return;
    refreshing.current = true;
    const id = ++latestLoad.current;
    return HttpUtil.get<PolicyView>('/panel/api/policy-routing', undefined, { silent: true })
      .then((res) => {
        if (id !== latestLoad.current) return;
        setLoadError(!res?.success);
        if (res?.success && res.obj) {
          setView(res.obj);
          if (replaceDraft || !initialized.current) {
            setDraft({ ...res.obj.config, rules: res.obj.config.rules ?? [] });
            initialized.current = true;
          }
        }
      })
      .catch(() => {
        if (id === latestLoad.current) setLoadError(true);
      })
      .finally(() => {
        if (id === latestLoad.current) refreshing.current = false;
      });
  }, []);

  useEffect(() => {
    void load(true);
    const timer = window.setInterval(() => {
      void load();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  const editRule = (index: number, update: Partial<Rule>) =>
    setDraft((prev) => ({
      ...prev,
      rules: prev.rules.map((r, i) => (i === index ? { ...r, ...update } : r)),
    }));
  const move = (index: number, offset: number) =>
    setDraft((prev) => {
      if (index < 0 || index + offset < 0 || index + offset >= prev.rules.length) return prev;
      const rules = [...prev.rules];
      [rules[index], rules[index + offset]] = [rules[index + offset]!, rules[index]!];
      return { ...prev, rules };
    });

  async function preview(apply: boolean) {
    const config = structuredClone(draft);
    setBusy(true);
    try {
      const res = await HttpUtil.post<string>('/panel/api/policy-routing/preview', config);
      if (!res?.success) return;
      modal.confirm({
        title: t(apply ? 'pages.policy.applyTitle' : 'pages.policy.preview'),
        width: 850,
        content: (
          <Space orientation="vertical" style={{ width: '100%' }}>
            <Alert
              type="info"
              showIcon
              title={t('pages.policy.scope')}
              description={t('pages.policy.tables')}
            />
            <Typography.Paragraph>{t('pages.policy.applyDetails')}</Typography.Paragraph>
            <Typography.Paragraph>{`${config.ingress} → TUN: ${config.tun}; direct: ${config.gateway || '—'} / ${config.uplink}`}</Typography.Paragraph>
            <pre style={{ maxHeight: 350, overflow: 'auto', whiteSpace: 'pre-wrap' }}>
              {res.obj || t('pages.policy.disableDetails')}
            </pre>
          </Space>
        ),
        okText: t(apply ? 'pages.policy.apply' : 'close'),
        cancelButtonProps: { style: apply ? undefined : { display: 'none' } },
        onOk: apply
          ? async () => {
              const saved = await HttpUtil.post('/panel/api/policy-routing/apply', config);
              if (!saved?.success) throw new Error(t('pages.policy.applyFailed'));
              messageApi.success(t('pages.policy.applied'));
              await load(true);
            }
          : undefined,
      });
    } finally {
      setBusy(false);
    }
  }

  const columns: ColumnsType<Rule> = [
    { title: '#', width: 55, render: (_value, _rule, index) => index + 1 },
    {
      title: t('pages.policy.enabled'),
      width: 90,
      render: (_v, r, i) => (
        <Switch
          size="small"
          checked={r.enabled}
          onChange={(enabled) => editRule(i, { enabled })}
          aria-label={t('pages.policy.enabled')}
        />
      ),
    },
    ...(['name', 'source', 'destination', 'interface'] as const).map((field) => ({
      title: t(`pages.policy.${field}`),
      width: field === 'name' ? 180 : 160,
      render: (_v: unknown, rule: Rule, index: number) => (
        <Input
          value={rule[field]}
          placeholder={field === 'interface' ? draft.ingress : field === 'name' ? '' : '0.0.0.0/0'}
          aria-label={t(`pages.policy.${field}`)}
          onChange={(event) => editRule(index, { [field]: event.target.value })}
        />
      ),
    })),
    {
      title: t('pages.policy.protocol'),
      width: 110,
      render: (_v, r, i) => (
        <Select
          style={{ width: '100%' }}
          value={r.protocol || 'any'}
          options={['any', 'tcp', 'udp', 'icmp'].map((value) => ({ value, label: value }))}
          onChange={(protocol) => editRule(i, { protocol })}
          aria-label={t('pages.policy.protocol')}
        />
      ),
    },
    ...(['sourcePort', 'destinationPort'] as const).map((field) => ({
      title: t(`pages.policy.${field}`),
      width: 140,
      render: (_v: unknown, rule: Rule, index: number) => (
        <Input
          value={rule[field]}
          placeholder="443 / 1000-2000"
          aria-label={t(`pages.policy.${field}`)}
          onChange={(event) => editRule(index, { [field]: event.target.value })}
        />
      ),
    })),
    {
      title: t('pages.policy.action'),
      width: 150,
      render: (_v, r, i) => (
        <Select
          style={{ width: '100%' }}
          value={r.action}
          options={['tun', 'direct', 'bypass'].map((value) => ({
            value,
            label: t(`pages.policy.${value}`),
          }))}
          onChange={(action) => editRule(i, { action })}
          aria-label={t('pages.policy.action')}
        />
      ),
    },
    {
      title: t('pages.policy.packets'),
      width: 100,
      render: (_v, r, i) => (countersCurrent && r.enabled ? (counters.get(i)?.packets ?? 0) : '—'),
    },
    {
      title: t('pages.policy.bytes'),
      width: 110,
      render: (_v, r, i) =>
        countersCurrent && r.enabled ? SizeFormatter.sizeFormat(counters.get(i)?.bytes ?? 0) : '—',
    },
    {
      title: '',
      width: 140,
      fixed: 'right',
      render: (_v, _r, i) => (
        <Space>
          <Button
            size="small"
            icon={<ArrowUpOutlined aria-hidden />}
            disabled={i === 0}
            onClick={() => move(i, -1)}
            aria-label={t('pages.policy.moveUp')}
          />
          <Button
            size="small"
            icon={<ArrowDownOutlined aria-hidden />}
            disabled={i === draft.rules.length - 1}
            onClick={() => move(i, 1)}
            aria-label={t('pages.policy.moveDown')}
          />
          <Button
            size="small"
            danger
            icon={<DeleteOutlined aria-hidden />}
            onClick={() => setDraft((p) => ({ ...p, rules: p.rules.filter((_r, n) => n !== i) }))}
            aria-label={t('delete')}
          />
        </Space>
      ),
    },
  ];

  return (
    <ConfigProvider theme={antdThemeConfig}>
      {messageContext}
      {modalContext}
      <Layout style={{ minHeight: '100vh' }}>
        <AppSidebar />
        <Layout.Content style={{ padding: 24, minWidth: 0 }}>
          <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
            <Typography.Title level={3}>{t('menu.policyRouting')}</Typography.Title>
            <Alert
              type="info"
              showIcon
              title={t('pages.policy.scope')}
              description={t('pages.policy.overview')}
            />
            {loadError && <Alert type="error" showIcon title={t('pages.policy.loadFailed')} />}
            {view?.state.error && (
              <Alert
                type={view.state.ready ? 'error' : 'warning'}
                showIcon
                title={view.state.error}
              />
            )}
            {view?.enabledByEnv && !view.state.forwardingEnabled && (
              <Alert type="warning" showIcon title={t('pages.policy.forwarding')} />
            )}
            {view?.state.waitingForTun && (
              <Alert type="warning" showIcon title={t('pages.policy.waitingTun')} />
            )}
            <Card
              title={t('pages.policy.interfaces')}
              extra={
                <Tag color={view?.state.applied ? 'green' : 'default'}>
                  {t(view?.state.applied ? 'pages.policy.applied' : 'pages.policy.inactive')}
                </Tag>
              }
            >
              <Space wrap size="large" align="end">
                <label>
                  {t('pages.policy.enabled')}
                  <br />
                  <Switch
                    aria-label={t('pages.policy.enabled')}
                    checked={draft.enabled}
                    onChange={(enabled) => setDraft((p) => ({ ...p, enabled }))}
                  />
                </label>
                {(['ingress', 'tun', 'uplink', 'gateway'] as const).map((key) => (
                  <label key={key}>
                    {t(`pages.policy.${key}`)}
                    <br />
                    <AutoComplete
                      style={{ width: 180 }}
                      value={draft[key]}
                      options={
                        key === 'gateway'
                          ? []
                          : (view?.state.interfaces ?? []).map((value) => ({ value }))
                      }
                      onChange={(value) => setDraft((p) => ({ ...p, [key]: value }))}
                      placeholder={key === 'gateway' ? '172.17.0.1' : defaults[key]}
                    />
                  </label>
                ))}
              </Space>
            </Card>
            <Card
              title={t('pages.policy.rules')}
              extra={
                <Button
                  icon={<PlusOutlined aria-hidden />}
                  disabled={draft.rules.length >= 128}
                  onClick={() => setDraft((p) => ({ ...p, rules: [...p.rules, { ...emptyRule }] }))}
                >
                  {t('pages.policy.addRule')}
                </Button>
              }
            >
              <Typography.Paragraph type="secondary">
                {t('pages.policy.firstMatch')}
              </Typography.Paragraph>
              <Typography.Paragraph type="secondary">
                {t('pages.policy.counterWindow')}
              </Typography.Paragraph>
              <Table<Rule>
                size="small"
                rowKey="key"
                dataSource={draft.rules.map((rule, key) => ({ ...rule, key }))}
                columns={columns}
                pagination={false}
                scroll={{ x: 1950 }}
              />
            </Card>
            <Space wrap>
              <Button
                loading={busy}
                onClick={() => {
                  void preview(false);
                }}
              >
                {t('pages.policy.preview')}
              </Button>
              <Button
                type="primary"
                disabled={!view?.enabledByEnv || !view?.state.ready}
                loading={busy}
                onClick={() => {
                  void preview(true);
                }}
              >
                {t('pages.policy.apply')}
              </Button>
              <Button
                icon={<ReloadOutlined aria-hidden />}
                onClick={() => {
                  void load();
                }}
              >
                {t('pages.policy.refresh')}
              </Button>
              <Button
                onClick={() => {
                  void load(true);
                }}
              >
                {t('pages.policy.revert')}
              </Button>
            </Space>
            <Collapse
              items={[
                {
                  key: 'kernel',
                  label: t('pages.policy.kernelState'),
                  children: (
                    <>
                      <Typography.Paragraph>{t('pages.policy.tables')}</Typography.Paragraph>
                      <pre style={{ maxHeight: 450, overflow: 'auto' }}>
                        {JSON.stringify(
                          {
                            rules: view?.state.ipRules,
                            routes: view?.state.ipRoutes,
                            nft: view?.state.nft,
                          },
                          null,
                          2,
                        )}
                      </pre>
                    </>
                  ),
                },
              ]}
            />
          </Space>
        </Layout.Content>
      </Layout>
    </ConfigProvider>
  );
}
