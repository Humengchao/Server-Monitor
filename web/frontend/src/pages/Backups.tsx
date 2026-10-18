import { useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Card, Checkbox, Descriptions, Form, Input, Space, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import client from '../api/client';
import '../i18n/operations';

interface Preview { backup_id: string; servers: number; credentials: number; tags: number; rules: number; notification_channels: number; network_checks: number; agent_servers: number; includes_credentials: boolean; already_imported: boolean; server_names: string[] }
interface PreviewResponse { preview: Preview; digest: string }
function encodeFile(buffer: ArrayBuffer) {
  const bytes = new Uint8Array(buffer); let raw = '';
  for (let i = 0; i < bytes.length; i += 8192) raw += String.fromCharCode(...bytes.subarray(i, i + 8192));
  return btoa(raw);
}
async function backupError(error: unknown, fallback: string): Promise<string> {
  let body = (error as { response?: { data?: unknown } })?.response?.data;
  if (body instanceof Blob) {
    if (body.size > 65536) return fallback;
    try { body = JSON.parse(await body.text()); } catch { return fallback; }
  }
  return typeof body === 'object' && body !== null && 'error' in body && typeof body.error === 'string' ? body.error : fallback;
}
export default function Backups() {
  const { t } = useTranslation('operations');
  const { message, modal } = App.useApp();
  const [account, setAccount] = useState(''), [password, setPassword] = useState('');
  const [include, setInclude] = useState(true), [data, setData] = useState(''), [filename, setFilename] = useState('');
  const [preview, setPreview] = useState<(PreviewResponse & { revision: number }) | null>(null);
  const [busy, setBusy] = useState(false), [reading, setReading] = useState(false);
  const mounted = useRef(false);
  const operation = useRef<AbortController | null>(null);
  const revision = useRef(0), fileSequence = useRef(0);
  const confirmation = useRef<ReturnType<typeof modal.confirm> | null>(null);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false; operation.current?.abort(); operation.current = null;
      confirmation.current?.destroy(); confirmation.current = null;
    };
  }, []);
  const invalidate = () => {
    revision.current++; setPreview(null);
    confirmation.current?.destroy(); confirmation.current = null;
  };
  const begin = () => {
    if (operation.current || !mounted.current) return null;
    const controller = new AbortController(); operation.current = controller; setBusy(true); return controller;
  };
  const end = (controller: AbortController) => {
    if (operation.current === controller) operation.current = null;
    if (mounted.current) setBusy(false);
  };
  const failure = async (error: unknown, controller: AbortController) => {
    const detail = await backupError(error, t('error'));
    if (mounted.current && !controller.signal.aborted) message.error(detail, 8);
  };
  const request = { current_password: account, password };
  const valid = account.length > 0 && password.length >= 12
    && new TextEncoder().encode(password).length <= 1024 && new TextEncoder().encode(account).length <= 1024;
  const exportBackup = async () => {
    if (!valid || reading) return;
    const controller = begin(); if (!controller) return;
    try {
      const response = await client.post<Blob>('/backups/export', { ...request, include_credentials: include }, { responseType: 'blob', timeout: 60000, signal: controller.signal });
      if (!mounted.current || controller.signal.aborted) return;
      const url = URL.createObjectURL(response.data), link = document.createElement('a');
      link.href = url; link.download = `server-monitor-${new Date().toISOString().slice(0, 10)}.smbak`;
      document.body.appendChild(link); link.click(); link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 5000); message.success(t('exportReady'));
    } catch (error) { await failure(error, controller); } finally { end(controller); }
  };
  const previewBackup = async () => {
    if (!valid || !data || reading) return;
    const controller = begin(); if (!controller) return;
    setPreview(null); const submittedRevision = revision.current;
    try {
      const response = await client.post<PreviewResponse>('/backups/preview', { ...request, data }, { timeout: 60000, signal: controller.signal });
      if (mounted.current && !controller.signal.aborted && submittedRevision === revision.current) setPreview({ ...response.data, revision: submittedRevision });
    } catch (error) { await failure(error, controller); } finally { end(controller); }
  };
  const restore = () => {
    const snapshot = preview;
    if (!snapshot || snapshot.preview.already_imported || !valid || reading || operation.current || confirmation.current) return;
    confirmation.current = modal.confirm({
      title: t('restoreConfirm'),
      content: <Typography.Text>{snapshot.preview.server_names.join(' · ')}</Typography.Text>,
      afterClose: () => { confirmation.current = null; },
      onOk: async () => {
        if (snapshot.revision !== revision.current) { message.warning(t('previewChanged')); return; }
        const controller = begin(); if (!controller) return;
        try {
          await client.post('/backups/import', { ...request, data, digest: snapshot.digest }, { timeout: 60000, signal: controller.signal });
          if (!mounted.current || controller.signal.aborted) return;
          revision.current++; setPreview(null); setData(''); setFilename(''); setAccount(''); setPassword('');
          message.success(t('restoreReady'), 10);
        } catch (error) {
          // A 409 may mean this very file was restored elsewhere while the preview
          // was open. Require another preview; never retry imports automatically.
          if ((error as { response?: { status?: number } }).response?.status === 409 && mounted.current) setPreview(null);
          await failure(error, controller);
        } finally { end(controller); }
      },
    });
  };
  const readFile = async (file?: File) => {
    invalidate(); setData(''); setFilename('');
    const sequence = ++fileSequence.current;
    if (!file) { setReading(false); return; }
    if (file.size > 17 * 1024 * 1024) { setReading(false); message.error(t('fileLimit')); return; }
    setReading(true);
    try {
      const bytes = await file.arrayBuffer();
      if (mounted.current && sequence === fileSequence.current) { setData(encodeFile(bytes)); setFilename(file.name); }
    } catch { if (mounted.current && sequence === fileSequence.current) message.error(t('fileReadFailed')); }
    finally { if (mounted.current && sequence === fileSequence.current) setReading(false); }
  };
  return <div className="page-content"><Typography.Title level={3}>{t('backupTitle')}</Typography.Title>
    <Form layout="vertical" style={{ maxWidth: 560 }} disabled={busy}>
      <Form.Item label={t('accountPassword')}><Input.Password aria-label={t('accountPassword')} autoComplete="current-password" value={account} onChange={event => { invalidate(); setAccount(event.target.value); }} /></Form.Item>
      <Form.Item label={t('backupPassword')}><Input.Password aria-label={t('backupPassword')} autoComplete="new-password" value={password} onChange={event => { invalidate(); setPassword(event.target.value); }} /></Form.Item>
    </Form>
    <Space orientation="vertical" style={{ width: '100%' }} size="large">
      <Card title={t('export')}><Typography.Paragraph>{t('exportHint')}</Typography.Paragraph>
        <Space wrap><Checkbox checked={include} disabled={busy} onChange={event => setInclude(event.target.checked)}>{t('includeCredentials')}</Checkbox>
          <Button type="primary" onClick={() => { void exportBackup(); }} disabled={!valid || busy || reading} loading={busy}>{t('export')}</Button></Space>
      </Card>
      <Card title={t('import')}><Alert type="info" title={t('restoreHint')} />
        <Form.Item label={t('file')} style={{ marginTop: 16 }}><input type="file" accept=".smbak" aria-label={t('chooseFile')} disabled={busy} onChange={event => {
          const file = event.currentTarget.files?.[0]; event.currentTarget.value = ''; void readFile(file);
        }} /></Form.Item>
        <Space wrap><Typography.Text>{filename}</Typography.Text>{reading && <Typography.Text>{t('readingFile')}</Typography.Text>}
          <Button disabled={!valid || !data || busy || reading} onClick={() => { void previewBackup(); }}>{t('preview')}</Button></Space>
        {preview && <div style={{ marginTop: 16 }}>
          <Descriptions column={{ xs: 1, sm: 2, md: 3 }} bordered items={[
            ['servers', 'serverCount'], ['credentials', 'credentialCount'], ['tags', 'tagCount'], ['rules', 'ruleCount'],
            ['notification_channels', 'channelCount'], ['network_checks', 'networkCount'], ['agent_servers', 'agentCount'],
          ].map(([key, label]) => ({ key, label: t(label), children: preview.preview[key as keyof Preview] as number }))} />
          <Typography.Paragraph style={{ marginTop: 8 }}>{preview.preview.server_names.join(' · ')}</Typography.Paragraph>
          {!preview.preview.includes_credentials && <Alert type="warning" title={t('noSecrets')} />}
          {preview.preview.already_imported && <Alert type="warning" title={t('alreadyImported')} />}
          <Button type="primary" disabled={busy || reading || !valid || preview.preview.already_imported} onClick={restore} style={{ marginTop: 12 }}>{t('restore')}</Button>
        </div>}
      </Card>
    </Space>
  </div>;
}
