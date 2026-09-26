export interface UserAgentSummary {
  /** "Chrome 140", "Safari 19", ...; empty when no browser was recognised. */
  browser: string;
  /** "Windows", "macOS", "iOS", ...; empty when unknown. */
  os: string;
}

const OS_RULES: [RegExp, string][] = [
  [/Windows/, 'Windows'],
  [/iPhone|iPad|iPod/, 'iOS'],
  [/Android/, 'Android'],
  [/Mac OS X/, 'macOS'],
  [/CrOS/, 'ChromeOS'],
  [/Linux/, 'Linux'],
];

// Order matters: Edge and Opera also advertise Chrome, and every WebKit
// browser advertises Safari, so the more specific tokens come first.
const BROWSER_RULES: [RegExp, string][] = [
  [/Edg(?:e|A|iOS)?\/(\d+)/, 'Edge'],
  [/OPR\/(\d+)/, 'Opera'],
  [/SamsungBrowser\/(\d+)/, 'Samsung Internet'],
  [/(?:Firefox|FxiOS)\/(\d+)/, 'Firefox'],
  [/(?:Chrome|CriOS)\/(\d+)/, 'Chrome'],
  [/Version\/(\d+)[\d.]* .*Safari\//, 'Safari'],
];

/**
 * Reduces a User-Agent string to the two facts a person checking their login
 * history actually wants: which browser and which platform. Returns null for
 * scripts and tools (curl, python-requests) so the caller can show the raw
 * string, which in that case is the useful part.
 */
export function describeUserAgent(userAgent: string): UserAgentSummary | null {
  const ua = (userAgent || '').trim();
  if (!ua) return null;
  const os = OS_RULES.find(([pattern]) => pattern.test(ua))?.[1] || '';
  let browser = '';
  for (const [pattern, name] of BROWSER_RULES) {
    const match = ua.match(pattern);
    if (match) { browser = `${name} ${match[1]}`; break; }
  }
  if (!browser && !os) return null;
  return { browser, os };
}

/** One-line label such as "Chrome 140 · Windows"; falls back to the raw string. */
export function userAgentLabel(userAgent: string): string {
  const summary = describeUserAgent(userAgent);
  if (!summary) return userAgent;
  return [summary.browser, summary.os].filter(Boolean).join(' · ');
}
