import { useEffect, useState } from 'react';
import { App, Button, Empty, Select, Spin, Typography } from 'antd';
import { CloudServerOutlined, ContainerOutlined, FolderOpenOutlined, LockOutlined } from '@ant-design/icons';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { serversApi } from '../api/servers';
import type { Server, DockerContainer } from '../api/servers';
import FileManager from '../components/FileManager';

export default function Files() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [searchParams, setSearchParams] = useSearchParams();
  const [servers, setServers] = useState<Server[]>([]);
  const [containers, setContainers] = useState<DockerContainer[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);
  const [reload, setReload] = useState(0);
  const [containersLoading, setContainersLoading] = useState(false);
  const [locked, setLocked] = useState(false);
  const serverId = searchParams.get('server') || '';
  const containerId = searchParams.get('container') || '';
  const server = servers.find((item) => item.id === serverId);
  const container = containers.find((item) => item.id === containerId);

  useEffect(() => {
    let cancelled = false;
    void serversApi.list().then((response) => {
      if (cancelled) return;
      const available = response.data || [];
      setServers(available);
      setLoadError(false);
      if (available.length) setSearchParams((previous) => previous.has('server') ? previous : new URLSearchParams({ server: available[0].id }), { replace: true });
    }).catch(() => { if (!cancelled) { setLoadError(true); message.error(t('server.loadFailed')); } })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [message, reload, setSearchParams, t]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setContainers([]);
      if (!server?.has_docker) { setContainersLoading(false); return; }
      setContainersLoading(true);
      void serversApi.getContainers(server.id, controller.signal).then((response) => {
        if (!controller.signal.aborted) setContainers(response.data || []);
      }).catch(() => { if (!controller.signal.aborted) message.error(t('docker.loadFailed')); })
        .finally(() => { if (!controller.signal.aborted) setContainersLoading(false); });
    }, 0);
    return () => { window.clearTimeout(timer); controller.abort(); };
  }, [server?.id, server?.has_docker, message, t]);

  return <div className="files-page">
    <div className="page-heading files-page-heading">
      <span className="files-page-icon" aria-hidden="true"><FolderOpenOutlined /></span>
      <div className="files-page-copy">
        <Typography.Text className="eyebrow">{t('files.eyebrow')}</Typography.Text>
        <Typography.Title level={2}>{t('nav.files')}</Typography.Title>
        <Typography.Text type="secondary">{t('files.description')}</Typography.Text>
      </div>
    </div>
    {loading ? <div className="files-page-loading"><Spin size="large" /></div> : <>
      <section className="file-targets file-workspace-context" aria-label={t('files.workspaceContext')}>
        <div className="file-context-summary">
          <span className="file-context-icon" aria-hidden="true">{containerId ? <ContainerOutlined /> : <CloudServerOutlined />}</span>
          <div className="file-context-copy">
            <span className="file-context-label">{t('files.workspaceContext')}</span>
            <strong className="file-context-name" title={server?.name}>{server?.name || t('files.selectServer')}</strong>
            {server && <div className="file-context-meta">
              <span title={containerId ? container?.name || containerId : undefined}>{containerId ? container?.name || containerId.slice(0, 12) : t('files.host')}</span>
              <span className="file-context-platform">{server.server_type === 'windows' ? 'Windows' : 'Linux'}</span>
            </div>}
          </div>
        </div>
        <div className="file-context-selectors">
          <label className="file-target-field">
            <Typography.Text><CloudServerOutlined aria-hidden="true" /> {t('files.server')}</Typography.Text>
            <Select showSearch optionFilterProp="label" aria-label={t('files.server')} placeholder={t('files.selectServer')} value={serverId || undefined} disabled={locked} options={servers.map((item) => ({ value: item.id, label: item.name + ' · ' + item.host }))} onChange={(value) => setSearchParams({ server: value })} />
          </label>
          <label className="file-target-field">
            <Typography.Text><ContainerOutlined aria-hidden="true" /> {t('files.target')}</Typography.Text>
            <Select aria-label={t('files.target')} value={containerId} disabled={locked || !server} loading={containersLoading} options={[{ value: '', label: t('files.host') }, ...containers.map((container) => ({ value: container.id, label: container.name + ' · ' + t('docker.stateValue.' + container.state, { defaultValue: container.state }), disabled: container.state !== 'running' }))]} onChange={(value) => setSearchParams(value ? { server: serverId, container: value } : { server: serverId })} />
          </label>
        </div>
        {locked && <span className="file-context-lock"><LockOutlined aria-hidden="true" />{t('files.workspaceLocked')}</span>}
      </section>
      {loadError ? <Empty description={t('server.loadFailed')}><Button onClick={() => setReload((value) => value + 1)}>{t('common.refresh')}</Button></Empty> : server ? <FileManager key={serverId + ':' + containerId} serverId={serverId} containerId={containerId || undefined} onLockedChange={setLocked} /> : <Empty description={t('files.selectServer')} />}
    </>}
  </div>;
}
