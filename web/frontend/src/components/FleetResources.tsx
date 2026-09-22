import { Progress, Typography } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DatabaseOutlined, HddOutlined, SwapOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { summarizeFleet } from '../utils/fleet';
import { formatBytes, severityColor } from '../utils/format';

const { Text } = Typography;

export default function FleetResources({ fleet }: { fleet: ReturnType<typeof summarizeFleet> }) {
  const { t } = useTranslation();
  const network = fleet.capacity.network;
  return (
    <section className="fleet-resources" aria-label={t('dashboard.onlineResources')}>
      <div className="fleet-resources-heading">
        <Text strong>{t('dashboard.onlineResources')}</Text>
        <Text type="secondary">{t('dashboard.resourceScope', { online: fleet.online, total: fleet.total })}</Text>
      </div>
      <div className="fleet-resource-grid">
        {(['memory', 'disk'] as const).map(key => {
          const item = fleet.capacity[key];
          const percent = item.total > 0 ? item.used / item.total * 100 : null;
          return (
            <div className="fleet-resource" key={key}>
              <div className="fleet-resource-label">
                <span>{key === 'memory' ? <DatabaseOutlined /> : <HddOutlined />}{t(`card.${key}`)}</span>
                <strong>{percent === null ? '—' : `${Math.round(percent)}%`}</strong>
              </div>
              <div className="fleet-resource-value">{item.count ? `${formatBytes(item.used)} / ${formatBytes(item.total)}` : '—'}</div>
              <Progress percent={percent ?? 0} showInfo={false} strokeColor={severityColor(percent ?? 0, key === 'memory' ? 'green' : 'violet')} />
              <Text type="secondary">{t('dashboard.resourceSamples', { count: item.count, online: fleet.online })}</Text>
            </div>
          );
        })}
        <div className="fleet-resource">
          <div className="fleet-resource-label"><span><SwapOutlined />{t('dashboard.networkTotal')}</span></div>
          <div className="fleet-network-rates">
            <div><span><ArrowDownOutlined />{t('dashboard.download')}</span><strong>{network.count ? `${formatBytes(network.rx)}/s` : '—'}</strong></div>
            <div><span><ArrowUpOutlined />{t('dashboard.upload')}</span><strong>{network.count ? `${formatBytes(network.tx)}/s` : '—'}</strong></div>
          </div>
          <Text type="secondary">{t('dashboard.resourceSamples', { count: network.count, online: fleet.online })}</Text>
        </div>
      </div>
    </section>
  );
}
