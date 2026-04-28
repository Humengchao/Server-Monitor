import React from 'react';
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts';
import { MetricPoint } from '../api/servers';

interface Props {
  history: MetricPoint[];
}

export default function MetricsChart({ history }: Props) {
  if (!history || history.length === 0) {
    return <div style={{ textAlign: 'center', padding: 40, color: '#999' }}>No metric data yet</div>;
  }

  const data = history.map((p) => ({
    time: new Date(p.recorded_at).toLocaleTimeString(),
    CPU: Math.round(p.cpu_percent),
    Memory: Math.round((p.memory_used / p.memory_total) * 100) || 0,
    'Network RX (MB/s)': +(p.network_rx_bytes / 1024 / 1024).toFixed(2),
    'Network TX (MB/s)': +(p.network_tx_bytes / 1024 / 1024).toFixed(2),
  }));

  return (
    <div>
      <h4>CPU & Memory</h4>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" fontSize={12} />
          <YAxis unit="%" />
          <Tooltip />
          <Area type="monotone" dataKey="CPU" stroke="#ff4d4f" fill="#ff4d4f" fillOpacity={0.1} />
          <Area type="monotone" dataKey="Memory" stroke="#1890ff" fill="#1890ff" fillOpacity={0.1} />
        </AreaChart>
      </ResponsiveContainer>

      <h4 style={{ marginTop: 24 }}>Network Traffic</h4>
      <ResponsiveContainer width="100%" height={200}>
        <AreaChart data={data}>
          <CartesianGrid strokeDasharray="3 3" />
          <XAxis dataKey="time" fontSize={12} />
          <YAxis unit=" MB/s" />
          <Tooltip />
          <Area type="monotone" dataKey="Network RX (MB/s)" stroke="#52c41a" fill="#52c41a" fillOpacity={0.1} />
          <Area type="monotone" dataKey="Network TX (MB/s)" stroke="#722ed1" fill="#722ed1" fillOpacity={0.1} />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
