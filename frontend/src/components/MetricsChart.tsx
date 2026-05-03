import React, { useMemo } from 'react';
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts';
import { useTranslation } from 'react-i18next';
import { MetricPoint } from '../api/servers';

interface Props {
  history: MetricPoint[];
}

function MetricsChart({ history }: Props) {
  const { t } = useTranslation();

  const data = useMemo(() => {
    if (!history || history.length === 0) return [];
    return history.map((p) => ({
      time: new Date(p.recorded_at).toLocaleString(),
      CPU: Math.round(p.cpu_percent),
      Memory: Math.round((p.memory_used / p.memory_total) * 100) || 0,
      [t('metrics.networkRx')]: +(p.network_rx_bytes / 1024 / 1024).toFixed(2),
      [t('metrics.networkTx')]: +(p.network_tx_bytes / 1024 / 1024).toFixed(2),
      [t('metrics.diskRead')]: +(p.disk_rx_bytes / 1024 / 1024).toFixed(2),
      [t('metrics.diskWrite')]: +(p.disk_tx_bytes / 1024 / 1024).toFixed(2),
    }));
  }, [history, t]);

  if (data.length === 0) {
    return <div style={{ textAlign: 'center', padding: 40, color: '#999' }}>{t('metrics.noData')}</div>;
  }

  const rxKey = t('metrics.networkRx');
  const txKey = t('metrics.networkTx');
  const readKey = t('metrics.diskRead');
  const writeKey = t('metrics.diskWrite');

  return (
    <div>
      <h4>{t('metrics.cpuMemory')}</h4>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" fontSize={12} />
          <YAxis unit="%" />
          <Tooltip />
          <Area type="basis" dataKey="CPU" stroke="#ff4d4f" fill="#ff4d4f" fillOpacity={0.1} isAnimationActive={false} />
          <Area type="basis" dataKey="Memory" stroke="#1890ff" fill="#1890ff" fillOpacity={0.1} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>

      <h4 style={{ marginTop: 24 }}>{t('metrics.networkTraffic')}</h4>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" fontSize={12} />
          <YAxis unit=" MB/s" />
          <Tooltip />
          <Area type="basis" dataKey={rxKey} stroke="#52c41a" fill="#52c41a" fillOpacity={0.1} isAnimationActive={false} />
          <Area type="basis" dataKey={txKey} stroke="#722ed1" fill="#722ed1" fillOpacity={0.1} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>

      <h4 style={{ marginTop: 24 }}>{t('metrics.diskIO')}</h4>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" fontSize={12} />
          <YAxis unit=" MB/s" />
          <Tooltip />
          <Area type="basis" dataKey={readKey} stroke="#fa8c16" fill="#fa8c16" fillOpacity={0.1} isAnimationActive={false} />
          <Area type="basis" dataKey={writeKey} stroke="#eb2f96" fill="#eb2f96" fillOpacity={0.1} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}

export default React.memo(MetricsChart);
