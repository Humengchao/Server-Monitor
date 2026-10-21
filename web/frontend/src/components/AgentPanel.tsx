import { formatDateTime } from '../utils/format';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Card, Space, Tag, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import client from '../api/client';
import { usePolling } from '../hooks/usePolling';
import { responseClock, useResponseClock } from '../hooks/useResponseClock';
import type { ResponseClock } from '../hooks/useResponseClock';
import { copyToClipboard } from '../utils/clipboard';
import '../i18n/operations';

interface AgentStatus { enrolled: boolean; online: boolean; version: string; username: string; hostname?: string }
interface Enrollment { command: string; windows_command?: string; expires_at: string }
interface Scope { serverId: string; read: AbortController | null; mutation: AbortController | null }
function errorMessage(error: unknown, fallback: string) {
  const failure = error as { response?: { data?: { error?: string } }; message?: string };
  return failure.response?.data?.error || failure.message || fallback;
}
export default function AgentPanel({ serverId, serverType, autoEnroll = false, onBound }: { serverId: string; serverType: string; autoEnroll?: boolean; onBound?: () => void }) {
  const { t, i18n } = useTranslation('operations');
  const { message, modal } = App.useApp();
  const [status, setStatus] = useState<{ serverId: string; value: AgentStatus } | null>(null);
  const [enrollment, setEnrollment] = useState<{ serverId: string; value: Enrollment; clock: ResponseClock } | null>(null);
  const [busy, setBusy] = useState(false);
  const [readError, setReadError] = useState('');
  const [operationError, setOperationError] = useState('');
  const scope = useRef<Scope | null>(null);
  const confirmation = useRef<ReturnType<typeof modal.confirm> | null>(null);
  const reportedBinding = useRef<string | null>(null);
  const currentStatus = status?.serverId === serverId ? status.value : null;
  const currentEnrollment = enrollment?.serverId === serverId ? enrollment.value : null;
  const observedAt = useResponseClock(enrollment?.serverId === serverId ? enrollment.clock : null);
  const expired = !!currentEnrollment && (!Number.isFinite(Date.parse(currentEnrollment.expires_at)) || Date.parse(currentEnrollment.expires_at) <= observedAt);
  const bound = autoEnroll && !!currentStatus?.enrolled;
  const error = operationError || readError;
  const load = useCallback(async () => {
    const active = scope.current;
    if (!active || active.serverId !== serverId || active.read || active.mutation) return;
    const controller = new AbortController(); active.read = controller;
    try {
      const response = await client.get<AgentStatus>(`/servers/${serverId}/agent`, { signal: controller.signal });
      if (scope.current === active && !controller.signal.aborted) { setStatus({ serverId, value: response.data }); setReadError(''); }
    } catch (failure) {
      if (scope.current === active && !controller.signal.aborted) setReadError(errorMessage(failure, ''));
    } finally { if (active.read === controller) active.read = null; }
  }, [serverId]);
  usePolling(load, autoEnroll && !bound ? 3000 : 10000, { leading: false });
  const mutate = async (action: 'enroll' | 'revoke') => {
    const active = scope.current;
    if (!active || active.serverId !== serverId || active.mutation) return;
    active.read?.abort(); active.read = null;
    const controller = new AbortController(); active.mutation = controller; setBusy(true); setOperationError('');
    try {
      if (action === 'enroll') {
        const response = await client.post<Enrollment>(`/servers/${serverId}/agent/enroll`, undefined, { signal: controller.signal });
        if (scope.current !== active || controller.signal.aborted) return;
        setEnrollment({ serverId, value: response.data, clock: responseClock(response.headers?.date) });
      } else {
        await client.delete(`/servers/${serverId}/agent`, { signal: controller.signal });
        if (scope.current !== active || controller.signal.aborted) return;
        setEnrollment(null); setStatus({ serverId, value: { enrolled: false, online: false, version: '', username: '' } });
      }
      setOperationError('');
    } catch (failure) {
      if (scope.current === active && !controller.signal.aborted) { const detail = errorMessage(failure, t('error')); setOperationError(detail); message.error(detail); }
    } finally {
      if (active.mutation === controller) active.mutation = null;
      if (scope.current === active) { setBusy(false); void load(); }
    }
  };
  const mutateRef = useRef(mutate);
  useEffect(() => { mutateRef.current = mutate; });
  useEffect(() => {
    const active: Scope = { serverId, read: null, mutation: null }; scope.current = active;
    const timer = window.setTimeout(() => {
      setReadError(''); setOperationError(''); setBusy(false);
      if (autoEnroll) void mutateRef.current('enroll');
      else void load();
    }, 0);
    return () => {
      window.clearTimeout(timer); scope.current = null; active.read?.abort(); active.mutation?.abort();
      confirmation.current?.destroy(); confirmation.current = null;
    };
  }, [autoEnroll, load, serverId]);
  useEffect(() => {
    if (bound && reportedBinding.current !== serverId) {
      reportedBinding.current = serverId;
      onBound?.();
    }
  }, [bound, onBound, serverId]);
  const revoke = () => {
    if (confirmation.current || busy) return;
    confirmation.current = modal.confirm({ title: t('revokeConfirm'), okButtonProps: { danger: true },
      afterClose: () => { confirmation.current = null; }, onOk: () => mutate('revoke') });
  };
  const command = serverType === 'windows' ? currentEnrollment?.windows_command : currentEnrollment?.command;
  const generateLabel = currentEnrollment || currentStatus?.enrolled ? t('regenerate') : t('generate');
  return <Card title={t('agentTitle')} className="agent-panel">
    <Space wrap><Tag color={currentStatus?.online ? 'green' : 'default'}>{!currentStatus ? t('checking') : currentStatus.online ? t('online') : currentStatus.enrolled ? t('offline') : t('notEnrolled')}</Tag>
      {currentStatus?.version && <Typography.Text>{t('agentVersion')}: {currentStatus.version}</Typography.Text>}
      {currentStatus?.username && <Typography.Text>{t('runUser')}: {currentStatus.username}</Typography.Text>}
      {!bound && <Button aria-label={generateLabel} aria-busy={busy} disabled={busy} onClick={() => { void mutate('enroll'); }} loading={busy}>{generateLabel}</Button>}
      {!autoEnroll && (currentStatus?.enrolled || currentEnrollment) && <Button danger disabled={busy} onClick={revoke}>{t('revoke')}</Button>}
    </Space>
    {error && <Alert type="warning" title={autoEnroll && operationError ? t('agentCommandFailed') : error} description={autoEnroll && operationError ? error : undefined} />}
    {autoEnroll && <Alert style={{ marginTop: 12 }} type={bound ? 'success' : 'info'} title={t(bound ? 'agentBound' : 'agentWaiting')} description={bound ? currentStatus?.hostname : undefined} />}
    {currentEnrollment && !bound && <div style={{ marginTop: 12 }}><Alert type={expired ? 'warning' : 'info'} title={t(expired ? 'ticketExpired' : 'ticketNotice')} description={t('privilege')} />
      <Typography.Paragraph>{t('expiresAt')}: {formatDateTime(currentEnrollment.expires_at, i18n.language)}</Typography.Paragraph>
      {!expired && <><Typography.Paragraph>{serverType === 'windows' ? t('windowsInstall') : t('linuxInstall')}</Typography.Paragraph>
        <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{command}</pre></>}
      <Button disabled={!command || expired || busy} onClick={async () => { try { await copyToClipboard(command!); message.success(t('copied')); } catch { message.error(t('copyFailed')); } }}>{t('copy')}</Button>
    </div>}
  </Card>;
}
