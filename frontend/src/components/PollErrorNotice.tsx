import React, { useState } from 'react';
import { Alert, App, Button, Tooltip, Typography } from 'antd';
import {
  ApiOutlined, DatabaseOutlined, DisconnectOutlined, HourglassOutlined,
  KeyOutlined, ReloadOutlined, SafetyCertificateOutlined, WarningOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { PollErrorKind, Server, serversApi } from '../api/servers';
import { pollErrorKind } from '../utils/pollError';

const { Text } = Typography;

const KIND_ICON: Record<PollErrorKind, React.ReactNode> = {
  auth: <KeyOutlined />,
  host_key: <SafetyCertificateOutlined />,
  unreachable: <DisconnectOutlined />,
  timeout: <HourglassOutlined />,
  command: <ApiOutlined />,
  storage: <DatabaseOutlined />,
  other: <WarningOutlined />,
};

/**
 * A host's last collection failure, in terms the reader can act on.
 *
 * The panel used to say only "offline". A rejected password, a firewall
 * dropping packets and a host-key change all looked identical, though the
 * first is fixed in the credential form, the second on the network and the
 * third by confirming the host really is the host. The server log knew which
 * it was; the person who could fix it did not.
 *
 * Full-width explanation for the detail page, where there is room for advice.
 */
export default function PollErrorNotice({ server, onRetried }: {
  server: Server;
  /** Called after a retry so the page can refetch and reflect the outcome. */
  onRetried?: () => void;
}) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [retrying, setRetrying] = useState(false);
  const kind = pollErrorKind(server);
  if (!kind) return null;

  const at = server.last_error_at ? new Date(server.last_error_at).toLocaleString() : null;

  const retry = async () => {
    setRetrying(true);
    try {
      const res = await serversApi.pollNow(server.id);
      if (res.data.ok) {
        message.success(t('pollError.retrySucceeded'));
      } else {
        // Still failing. Name the new reason rather than repeating the old
        // banner: the fix may have moved the problem rather than solved it.
        const nextKind = res.data.kind && res.data.kind !== kind
          ? t(`pollError.${res.data.kind}.title`)
          : t(`pollError.${kind}.title`);
        message.warning(t('pollError.retryStillFailing', { reason: nextKind }));
      }
      onRetried?.();
    } catch (err: unknown) {
      // 409 is the scheduled loop having got there first — good news, not a
      // failure, and its result will arrive on its own.
      if ((err as { response?: { status?: number } })?.response?.status === 409) {
        message.info(t('pollError.retryInFlight'));
        onRetried?.();
        return;
      }
      message.error(t('pollError.retryFailed'));
    } finally {
      setRetrying(false);
    }
  };

  return (
    <Alert
      className="poll-error-notice"
      type={kind === 'auth' || kind === 'host_key' ? 'error' : 'warning'}
      showIcon
      icon={KIND_ICON[kind]}
      title={t(`pollError.${kind}.title`)}
      // The banner says what to fix; this is how the reader finds out whether
      // the fix worked, instead of waiting out a backoff of up to an hour.
      action={
        <Button size="small" icon={<ReloadOutlined />} loading={retrying} onClick={retry}>
          {t('pollError.retry')}
        </Button>
      }
      description={
        <div className="poll-error-body">
          <span>{t(`pollError.${kind}.hint`)}</span>
          {/* The library's or host's own sentence. English-only and often
              jargon, so it is secondary to the localized advice above rather
              than a replacement for it. */}
          {server.last_error && <code>{server.last_error}</code>}
          {at && <Text type="secondary">{t('pollError.since', { time: at })}</Text>}
        </div>
      }
    />
  );
}

/** Compact badge for the fleet views, where only a glance is available. */
export function PollErrorBadge({ server }: { server: Pick<Server, 'last_error_kind' | 'last_error'> }) {
  const { t } = useTranslation();
  const kind = pollErrorKind(server);
  if (!kind) return null;
  return (
    <Tooltip title={<><strong>{t(`pollError.${kind}.title`)}</strong><br />{server.last_error}</>}>
      <span className={`poll-error-badge kind-${kind}`}>
        {KIND_ICON[kind]}
        {t(`pollError.${kind}.short`)}
      </span>
    </Tooltip>
  );
}
