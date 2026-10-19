import { Children, isValidElement, useRef } from 'react';
import type { ReactNode } from 'react';
import { App, Button } from 'antd';
import { CopyOutlined, FileImageOutlined } from '@ant-design/icons';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { useTranslation } from 'react-i18next';

// Model output is untrusted. In particular, images can leak task contents to
// third-party hosts and relative links can navigate the authenticated panel.
function safeURL(value: string): string {
  if (!/^(https?:\/\/|mailto:)/i.test(value)) return '';
  try { const url = new URL(value); return url.username || url.password ? '' : value; } catch { return ''; }
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const { t } = useTranslation('ai');
  const { message } = App.useApp();
  const content = useRef<HTMLPreElement>(null);
  const code = Children.toArray(children).find(isValidElement);
  const language = code && isValidElement<{ className?: string }>(code) ? code.props.className?.replace(/^language-/, '') : '';
  return <div className="ai-markdown-code"><div className="ai-code-toolbar"><span>{language || t('code')}</span><Button size="small" type="text" icon={<CopyOutlined />} aria-label={t('copyCode')} onClick={async () => {
    try { await navigator.clipboard.writeText(content.current?.textContent || ''); message.success(t('copied')); }
    catch { message.error(t('copyFailed')); }
  }}>{t('copy')}</Button></div><pre ref={content}>{children}</pre></div>;
}

export default function AIMarkdown({ children }: { children: string }) {
  const { t } = useTranslation('ai');
  return <div className="ai-markdown"><Markdown remarkPlugins={[remarkGfm]} skipHtml urlTransform={safeURL} components={{
    a: ({ href, children: label }) => href ? <a href={href} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">{label}</a> : <span>{label}</span>,
    img: ({ alt }) => <span className="ai-markdown-image"><FileImageOutlined />{alt || t('imageOmitted')}</span>,
    pre: ({ children: content }) => <CodeBlock>{content}</CodeBlock>,
    table: ({ children: content }) => <div className="ai-markdown-table"><table>{content}</table></div>,
  }}>{children}</Markdown></div>;
}
