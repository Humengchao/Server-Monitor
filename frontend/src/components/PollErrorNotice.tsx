import React from 'react';
import { Alert, Tooltip, Typography } from 'antd';
import {
  ApiOutlined, DatabaseOutlined, DisconnectOutlined, HourglassOutlined,
  KeyOutlined, SafetyCertificateOutlined, WarningOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { PollErrorKind, Server } from '../api/servers';
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
export default function PollErrorNotice({ server }: { server: Server }) {
  const { t } = useTranslation();
  const kind = pollErrorKind(server);
  if (!kind) return null;

  const at = server.last_error_at ? new Date(server.last_error_at).toLocaleString() : null;
  return (
    <Alert
      className="poll-error-notice"
      type={kind === 'auth' || kind === 'host_key' ? 'error' : 'warning'}
      showIcon
      icon={KIND_ICON[kind]}
      title={t(`pollError.${kind}.title`)}
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
