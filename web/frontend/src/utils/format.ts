// Shared formatting helpers. These were previously duplicated across the
// dashboard card, the detail page and the public status page, which let the
// same value render three slightly different ways.

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

export function formatBytes(bytes: number, fractionDigits?: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), BYTE_UNITS.length - 1);
  const digits = fractionDigits ?? (index > 2 ? 1 : 2);
  return `${(bytes / 1024 ** index).toFixed(digits)} ${BYTE_UNITS[index]}`;
}

export function formatGB(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 GB';
  return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
}

/** Compact uptime, e.g. "1y 2m 3d". */
export function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '0d';
  const totalDays = Math.floor(seconds / 86400);
  const years = Math.floor(totalDays / 365);
  const remainingDays = totalDays % 365;
  const months = Math.floor(remainingDays / 30);
  const days = remainingDays % 30;
  const parts: string[] = [];
  if (years > 0) parts.push(`${years}y`);
  if (months > 0) parts.push(`${months}m`);
  if (days > 0 || parts.length === 0) parts.push(`${days}d`);
  return parts.join(' ');
}

/**
 * Shared severity ramp so a number reads the same everywhere: calm below 75%,
 * warm past 75%, hot past 90%.
 */
export function severityColor(percent: number, hue: 'blue' | 'green' | 'violet'): string {
  if (percent >= 90) return '#f2495c';
  if (percent >= 75) return '#f2a93b';
  return { blue: '#5d7df7', green: '#18b690', violet: '#8d6dd7' }[hue];
}

export function percentOf(used: number, total: number): number {
  if (!total || total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((used / total) * 100)));
}

/** Whole-calendar-unit difference, used for expiry countdowns. */
export function diffYMD(from: Date, to: Date): { years: number; months: number; days: number } {
  let years = to.getFullYear() - from.getFullYear();
  let months = to.getMonth() - from.getMonth();
  let days = to.getDate() - from.getDate();
  if (days < 0) {
    months--;
    days += new Date(to.getFullYear(), to.getMonth(), 0).getDate();
  }
  if (months < 0) {
    years--;
    months += 12;
  }
  return { years, months, days };
}

export interface ExpirationInfo {
  text: string;
  color: string;
  expired: boolean;
  /** Whole days remaining; negative once the date has passed. */
  daysLeft: number;
}

const MS_PER_DAY = 86400000;

export function getExpirationInfo(expiresAt?: string | null, lang: 'zh' | 'en' = 'en'): ExpirationInfo | null {
  if (!expiresAt) return null;
  const now = new Date();
  const expires = new Date(expiresAt);
  if (Number.isNaN(expires.getTime())) return null;
  const expired = expires.getTime() < now.getTime();
  const { years, months, days } = diffYMD(expired ? expires : now, expired ? now : expires);

  const parts: string[] = [];
  if (years > 0) parts.push(lang === 'zh' ? `${years}年` : `${years}y`);
  if (months > 0) parts.push(lang === 'zh' ? `${months}月` : `${months}m`);
  if (days > 0 || parts.length === 0) parts.push(lang === 'zh' ? `${days}天` : `${days}d`);
  const span = parts.join('');
  const daysLeft = Math.ceil((expires.getTime() - now.getTime()) / MS_PER_DAY);

  if (expired) {
    return { text: lang === 'zh' ? `已过期${span}` : `Expired ${span}`, color: '#ff4d4f', expired, daysLeft };
  }
  const color = daysLeft <= 30 ? '#ff4d4f' : daysLeft <= 90 ? '#faad14' : '#52c41a';
  return { text: lang === 'zh' ? `${span}后到期` : `${span} left`, color, expired, daysLeft };
}

const CYCLE_MONTHS: Record<string, number> = { month: 1, quarter: 3, half_year: 6, year: 12 };

/** Normalizes any billing cycle to a monthly figure so costs can be summed. */
export function monthlyCost(price: number, cycle: string): number {
  const months = CYCLE_MONTHS[cycle] ?? 12;
  if (!Number.isFinite(price) || price <= 0) return 0;
  return price / months;
}

export const CURRENCY_SYMBOLS: Record<string, string> = { CNY: '¥', USD: '$', EUR: '€' };

export function formatCurrency(amount: number, currency: string): string {
  const symbol = CURRENCY_SYMBOLS[currency] || '';
  return `${symbol}${amount.toFixed(2)}`;
}
