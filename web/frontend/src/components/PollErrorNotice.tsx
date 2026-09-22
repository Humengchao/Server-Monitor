import React from 'react';
import { Alert, App, Button, Tooltip, Typography } from 'antd';
import { ApiOutlined, DatabaseOutlined, DisconnectOutlined, HourglassOutlined, KeyOutlined, ReloadOutlined, SafetyCertificateOutlined, WarningOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import type { PollErrorKind, Server } from '../api/servers';
import { serversApi } from '../api/servers';
import { pollErrorKind } from '../utils/pollError';

const { Text } = Typography;
const ICON: Record<PollErrorKind, React.ReactNode> = {
  auth: <KeyOutlined />, host_key: <SafetyCertificateOutlined />, unreachable: <DisconnectOutlined />,
  timeout: <HourglassOutlined />, command: <ApiOutlined />, storage: <DatabaseOutlined />, other: <WarningOutlined />,
};

export function PollErrorBadge({ server }: { server: Pick<Server, 'last_error_kind' | 'last_error'> }) {
  const { t } = useTranslation();
  const kind = pollErrorKind(server);
  if (!kind) return null;
  return (
    <Tooltip title={<><strong>{t(`pollError.${kind}.title`)}</strong>{server.last_error && <><br />{server.last_error}</>}</>}>
      <span className={`poll-error-badge kind-${kind}`}>{ICON[kind]}{t(`pollError.${kind}.short`)}</span>
    </Tooltip>
  );
}

export default function PollErrorNotice({ server, onRetried }: { server: Server; onRetried?: () => void }) {
  const { t, i18n } = useTranslation();
  const { message } = App.useApp();
  const [retrying, setRetrying] = React.useState(false);
  const kind = pollErrorKind(server);
  if (!kind) return null;
  const at = server.last_error_at ? new Date(server.last_error_at).toLocaleString(i18n.language?.startsWith('zh') ? 'zh-CN' : 'en-US') : null;
  const retry = async () => {
    setRetrying(true);
    try {
      const response = await serversApi.pollNow(server.id);
      if (response.data.ok) message.success(t('pollError.retrySucceeded'));
      else message.warning(t('pollError.retryStillFailing', { reason: t(`pollError.${response.data.kind || kind}.title`) }));
      onRetried?.();
    } catch (error: unknown) {
      if ((error as { response?: { status?: number } })?.response?.status === 409) message.info(t('pollError.retryInFlight'));
      else message.error(t('pollError.retryFailed'));
      onRetried?.();
    } finally { setRetrying(false); }
  };
  return (
    <Alert
      className="poll-error-notice"
      type={kind === 'auth' || kind === 'host_key' ? 'error' : 'warning'}
      showIcon
      icon={ICON[kind]}
      title={t(`pollError.${kind}.title`)}
      action={<Button size="small" icon={<ReloadOutlined />} loading={retrying} onClick={retry}>{t('pollError.retry')}</Button>}
      description={<div className="poll-error-body"><span>{t(`pollError.${kind}.hint`)}</span>{server.last_error && <code>{server.last_error}</code>}{at && <Text type="secondary">{t('pollError.since', { time: at })}</Text>}</div>}
    />
  );
}
