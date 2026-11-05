import { Card, Space, Tag, Typography, theme } from 'antd';
import { useTranslation } from 'react-i18next';
import { Sparkline } from '@/components/viz';
import { SizeFormatter, TimeFormatter } from '@/utils';
import { useLiveTrafficHistory } from './useLiveTrafficHistory';
import { TRAFFIC_POLL_INTERVAL_S } from '@/lib/traffic/poll-interval';

export default function LiveTrafficCard() {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const history = useLiveTrafficHistory();
  return (
    <Card
      title={t('pages.index.liveXrayTraffic')}
      extra={
        <Tag color={history.stale ? 'default' : 'green'}>
          {t(history.stale ? 'pages.index.liveWaiting' : 'pages.index.liveNow')}
        </Tag>
      }
    >
      <Typography.Paragraph type="secondary">
        {t('pages.index.liveBrowserHistory')}
      </Typography.Paragraph>
      {history.persistenceDisabled && (
        <Typography.Paragraph type="secondary">
          {t('pages.index.liveWithoutPersistence')}
        </Typography.Paragraph>
      )}
      {(['local', 'nodes'] as const).map((scope) => {
        const samples = history[scope];
        if (scope === 'nodes' && samples.length === 0) return null;
        const current = samples.at(-1);
        const stale = scope === 'local' ? history.localStale : history.nodesStale;
        return (
          <div key={scope}>
            <Space wrap>
              <Typography.Text strong>
                {t(scope === 'local' ? 'pages.index.liveLocal' : 'menu.nodes')}
              </Typography.Text>
              <Typography.Text>{`↑ ${SizeFormatter.speedFormat(stale ? 0 : (current?.up ?? 0))} · ↓ ${SizeFormatter.speedFormat(stale ? 0 : (current?.down ?? 0))}`}</Typography.Text>
            </Space>
            <Sparkline
              data={samples.map((p) => p.up)}
              data2={samples.map((p) => p.down)}
              labels={samples.map((p) => TimeFormatter.formatClock(Math.floor(p.at / 1000)))}
              height={140}
              stroke={token.colorPrimary}
              stroke2={token.colorTextTertiary}
              name1={t('pages.index.upload')}
              name2={t('pages.index.download')}
              yFormatter={SizeFormatter.speedFormat}
              showTooltip
              valueMax={null}
            />
            <Typography.Text type="secondary">{`${t('pages.index.liveWindowVolume')}: ↑ ${SizeFormatter.sizeFormat(samples.reduce((total, p) => total + p.up * TRAFFIC_POLL_INTERVAL_S, 0))} · ↓ ${SizeFormatter.sizeFormat(samples.reduce((total, p) => total + p.down * TRAFFIC_POLL_INTERVAL_S, 0))}`}</Typography.Text>
          </div>
        );
      })}
    </Card>
  );
}
