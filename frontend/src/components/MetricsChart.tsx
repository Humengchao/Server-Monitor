import React, { useContext, useMemo, useState } from 'react';
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts';
import { useTranslation } from 'react-i18next';
import { MetricPoint } from '../api/servers';
import { DarkModeContext } from '../App';

interface Props {
  history: MetricPoint[];
}

interface SeriesSpec {
  key: string;
  label: string;
  color: string;
}

// Downsample data to keep chart rendering performant.
function downsample<T>(data: T[], maxPoints: number): T[] {
  if (data.length <= maxPoints) return data;
  const step = data.length / maxPoints;
  return Array.from({ length: maxPoints }, (_, i) => data[Math.floor(i * step)]);
}

/**
 * Picks a byte unit that keeps the largest sample in a readable range, so a
 * host doing 300 KB/s isn't drawn as a flat line of 0.00 MB/s.
 */
function pickRateUnit(peak: number): { divisor: number; suffix: string } {
  if (peak >= 1024 ** 3) return { divisor: 1024 ** 3, suffix: 'GB/s' };
  if (peak >= 1024 ** 2) return { divisor: 1024 ** 2, suffix: 'MB/s' };
  if (peak >= 1024) return { divisor: 1024, suffix: 'KB/s' };
  return { divisor: 1, suffix: 'B/s' };
}

function formatAxisTime(iso: string, spansMultipleDays: boolean): string {
  const date = new Date(iso);
  const time = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  if (!spansMultipleDays) return time;
  return `${date.toLocaleDateString([], { month: '2-digit', day: '2-digit' })} ${time}`;
}

interface TooltipEntry {
  dataKey: string;
  name: string;
  color: string;
  value: number;
}

interface ChartTooltipProps {
  active?: boolean;
  payload?: TooltipEntry[];
  label?: string;
  unit: string;
  dark: boolean;
  /**
   * False on the panels the pointer is not over. syncId makes recharts mark
   * every synced chart active at once, which would stack four tooltips on
   * screen; the crosshair is what those panels are for.
   */
  visible?: boolean;
}

function ChartTooltip({ active, payload, label, unit, dark, visible }: ChartTooltipProps) {
  if (!active || !visible || !payload?.length) return null;
  return (
    <div className={`chart-tooltip${dark ? ' dark' : ''}`}>
      <span className="chart-tooltip-time">{label}</span>
      {payload.map((entry) => (
        <span key={entry.dataKey} className="chart-tooltip-row">
          <i style={{ background: entry.color }} />
          <em>{entry.name}</em>
          <strong>{typeof entry.value === 'number' ? entry.value.toFixed(2) : entry.value}{unit}</strong>
        </span>
      ))}
    </div>
  );
}

/** One row of the chart dataset: a formatted timestamp plus numeric series. */
type ChartRow = { time: string } & Record<string, string | number>;

function MetricPanel({
  title, data, series, unit, dark, domain, syncId, hovered, onHover,
}: {
  title: string;
  data: ChartRow[];
  series: SeriesSpec[];
  unit: string;
  dark: boolean;
  domain?: [number, number];
  syncId: string;
  hovered: boolean;
  onHover: (hovered: boolean) => void;
}) {
  const grid = dark ? 'rgba(255,255,255,.08)' : 'rgba(24,32,51,.07)';
  const axis = dark ? 'rgba(226,232,248,.5)' : 'rgba(85,95,120,.75)';

  return (
    <section className="chart-panel">
      <header className="chart-panel-head">
        <h4>{title}</h4>
        <div className="chart-legend">
          {series.map((s) => (
            <span key={s.key}><i style={{ background: s.color }} />{s.label}</span>
          ))}
        </div>
      </header>
      {/* Hover is tracked on the wrapper rather than on the chart: recharts'
          own onMouseLeave fires when the pointer crosses internal elements. */}
      <div
        className="chart-canvas"
        onMouseEnter={() => onHover(true)}
        onMouseLeave={() => onHover(false)}
      >
        <ResponsiveContainer width="100%" height={210}>
          {/* The top margin leaves room for the highest Y tick label, which
              recharts otherwise clips against the container edge. */}
          <AreaChart data={data} syncId={syncId} margin={{ top: 12, right: 10, left: 0, bottom: 0 }}>
            <defs>
              {series.map((s) => (
                <linearGradient key={s.key} id={`grad-${s.key}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={s.color} stopOpacity={0.34} />
                  <stop offset="100%" stopColor={s.color} stopOpacity={0.02} />
                </linearGradient>
              ))}
            </defs>
            <CartesianGrid stroke={grid} vertical={false} />
            <XAxis
              dataKey="time"
              tick={{ fontSize: 11, fill: axis }}
              tickLine={false}
              axisLine={{ stroke: grid }}
              minTickGap={44}
            />
            <YAxis
              tick={{ fontSize: 11, fill: axis }}
              tickLine={false}
              axisLine={false}
              // Wide enough for the longest label a byte-rate unit produces
              // ("450 KB/s"), so ticks never wrap onto a clipped second line.
              width={unit.length > 2 ? 74 : 46}
              domain={domain}
              tickFormatter={(value: number) => `${value}${unit}`}
            />
            <Tooltip
              cursor={{ stroke: dark ? 'rgba(255,255,255,.22)' : 'rgba(24,32,51,.16)', strokeWidth: 1 }}
              wrapperStyle={{ position: 'absolute', pointerEvents: 'none', outline: 'none' }}
              content={<ChartTooltip unit={unit} dark={dark} visible={hovered} />}
            />
            <Legend content={() => null} />
            {series.map((s) => (
              <Area
                key={s.key}
                type="monotone"
                dataKey={s.key}
                name={s.label}
                stroke={s.color}
                strokeWidth={1.8}
                fill={`url(#grad-${s.key})`}
                isAnimationActive={false}
                dot={false}
                activeDot={{ r: 3, strokeWidth: 0 }}
              />
            ))}
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </section>
  );
}

// One sync group for the whole page: hovering any panel puts the crosshair on
// the same instant in all of them, which is how you tell whether a CPU spike
// and a disk-I/O spike were the same event. Every panel plots the same `rows`,
// so index-based syncing lines up exactly.
const SYNC_ID = 'server-metrics';

function MetricsChart({ history }: Props) {
  const { t } = useTranslation();
  const dark = useContext(DarkModeContext);
  const [hoveredPanel, setHoveredPanel] = useState<string | null>(null);

  const hoverProps = (id: string) => ({
    syncId: SYNC_ID,
    hovered: hoveredPanel === id,
    onHover: (isOver: boolean) => setHoveredPanel((current) => {
      if (isOver) return id;
      // Only clear if this panel is still the one recorded: the enter of the
      // next panel can land before this one's leave.
      return current === id ? null : current;
    }),
  });

  const { rows, netUnit, diskUnit, hasLatency } = useMemo(() => {
    if (!history || history.length === 0) {
      return { rows: [], netUnit: { divisor: 1, suffix: 'B/s' }, diskUnit: { divisor: 1, suffix: 'B/s' }, hasLatency: false };
    }
    const sampled = downsample(history, 300);
    const first = new Date(sampled[0].recorded_at);
    const last = new Date(sampled[sampled.length - 1].recorded_at);
    const spansMultipleDays = last.getTime() - first.getTime() > 24 * 3600 * 1000;

    const netPeak = Math.max(...sampled.map((p) => Math.max(p.network_rx_bytes, p.network_tx_bytes)), 0);
    const diskPeak = Math.max(...sampled.map((p) => Math.max(p.disk_rx_bytes, p.disk_tx_bytes)), 0);
    const net = pickRateUnit(netPeak);
    const disk = pickRateUnit(diskPeak);

    return {
      netUnit: net,
      diskUnit: disk,
      hasLatency: sampled.some((p) => p.latency_ms > 0),
      rows: sampled.map((p) => ({
        time: formatAxisTime(p.recorded_at, spansMultipleDays),
        cpu: Math.round(p.cpu_percent * 10) / 10,
        memory: p.memory_total ? Math.round((p.memory_used / p.memory_total) * 1000) / 10 : 0,
        rx: Math.round((p.network_rx_bytes / net.divisor) * 100) / 100,
        tx: Math.round((p.network_tx_bytes / net.divisor) * 100) / 100,
        read: Math.round((p.disk_rx_bytes / disk.divisor) * 100) / 100,
        write: Math.round((p.disk_tx_bytes / disk.divisor) * 100) / 100,
        load1: Math.round(p.load_1 * 100) / 100,
        load5: Math.round(p.load_5 * 100) / 100,
        latency: p.latency_ms,
      })),
    };
  }, [history]);

  if (rows.length === 0) {
    return <div className="chart-empty">{t('metrics.noData')}</div>;
  }

  return (
    <div className="metrics-charts">
      <MetricPanel
        title={t('metrics.cpuMemory')}
        data={rows}
        dark={dark}
        {...hoverProps('cpu')}
        unit="%"
        domain={[0, 100]}
        series={[
          { key: 'cpu', label: t('metrics.cpu'), color: '#5d7df7' },
          { key: 'memory', label: t('metrics.memory'), color: '#18b690' },
        ]}
      />
      <MetricPanel
        title={t('metrics.networkTraffic')}
        data={rows}
        dark={dark}
        {...hoverProps('net')}
        unit={` ${netUnit.suffix}`}
        series={[
          { key: 'rx', label: t('metrics.download'), color: '#39b8a4' },
          { key: 'tx', label: t('metrics.upload'), color: '#8d6dd7' },
        ]}
      />
      <MetricPanel
        title={t('metrics.diskIO')}
        data={rows}
        dark={dark}
        {...hoverProps('disk')}
        unit={` ${diskUnit.suffix}`}
        series={[
          { key: 'read', label: t('metrics.read'), color: '#e8944a' },
          { key: 'write', label: t('metrics.write'), color: '#d9639b' },
        ]}
      />
      <MetricPanel
        title={t('metrics.loadAverage')}
        data={rows}
        dark={dark}
        {...hoverProps('load')}
        unit=""
        series={[
          { key: 'load1', label: t('metrics.load1'), color: '#6f8cf5' },
          { key: 'load5', label: t('metrics.load5'), color: '#c07ce0' },
        ]}
      />
      {hasLatency && (
        <MetricPanel
          title={t('metrics.latency')}
          data={rows}
          dark={dark}
          {...hoverProps('latency')}
          unit=" ms"
          series={[{ key: 'latency', label: t('metrics.latency'), color: '#4bb3d6' }]}
        />
      )}
    </div>
  );
}

export default React.memo(MetricsChart);
