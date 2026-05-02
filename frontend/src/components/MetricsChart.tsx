import React, { useMemo } from 'react';
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts';
import { MetricPoint } from '../api/servers';

interface Props {
  history: MetricPoint[];
}

function MetricsChart({ history }: Props) {
  const data = useMemo(() => {
    if (!history || history.length === 0) return [];
    return history.map((p) => ({
      time: new Date(p.recorded_at).toLocaleString(),
      CPU: Math.round(p.cpu_percent),
      Memory: Math.round((p.memory_used / p.memory_total) * 100) || 0,
      'Network RX (MB/s)': +(p.network_rx_bytes / 1024 / 1024).toFixed(2),
      'Network TX (MB/s)': +(p.network_tx_bytes / 1024 / 1024).toFixed(2),
      'Disk Read (MB/s)': +(p.disk_rx_bytes / 1024 / 1024).toFixed(2),
      'Disk Write (MB/s)': +(p.disk_tx_bytes / 1024 / 1024).toFixed(2),
    }));
  }, [history]);

  if (data.length === 0) {
    return <div style={{ textAlign: 'center', padding: 40, color: '#999' }}>No metric data yet</div>;
  }

  return (
    <div>
      <h4>CPU & Memory</h4>
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

      <h4 style={{ marginTop: 24 }}>Network Traffic</h4>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" fontSize={12} />
          <YAxis unit=" MB/s" />
          <Tooltip />
          <Area type="basis" dataKey="Network RX (MB/s)" stroke="#52c41a" fill="#52c41a" fillOpacity={0.1} isAnimationActive={false} />
          <Area type="basis" dataKey="Network TX (MB/s)" stroke="#722ed1" fill="#722ed1" fillOpacity={0.1} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>

      <h4 style={{ marginTop: 24 }}>Disk I/O</h4>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" fontSize={12} />
          <YAxis unit=" MB/s" />
          <Tooltip />
          <Area type="basis" dataKey="Disk Read (MB/s)" stroke="#fa8c16" fill="#fa8c16" fillOpacity={0.1} isAnimationActive={false} />
          <Area type="basis" dataKey="Disk Write (MB/s)" stroke="#eb2f96" fill="#eb2f96" fillOpacity={0.1} isAnimationActive={false} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}

export default React.memo(MetricsChart);
