import { useEffect, useState } from 'react';
import { App, Button, Empty, Select, Spin, Typography } from 'antd';
import { CloudServerOutlined, ContainerOutlined } from '@ant-design/icons';
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

  return <div>
    <div className="page-heading"><div><Typography.Title level={2}>{t('nav.files')}</Typography.Title><Typography.Text type="secondary">{t('files.description')}</Typography.Text></div></div>
    {loading ? <Spin size="large" /> : <>
      <div className="file-targets">
        <label className="file-target-field"><Typography.Text><CloudServerOutlined /> {t('files.server')}</Typography.Text><Select showSearch optionFilterProp="label" aria-label={t('files.server')} placeholder={t('files.selectServer')} value={serverId || undefined} disabled={locked} options={servers.map((item) => ({ value: item.id, label: item.name + ' · ' + item.host }))} onChange={(value) => setSearchParams({ server: value })} /></label>
        <label className="file-target-field"><Typography.Text><ContainerOutlined /> {t('files.target')}</Typography.Text><Select aria-label={t('files.target')} value={containerId} disabled={locked || !server} loading={containersLoading} options={[{ value: '', label: t('files.host') }, ...containers.map((container) => ({ value: container.id, label: container.name + ' · ' + t('docker.stateValue.' + container.state, { defaultValue: container.state }), disabled: container.state !== 'running' }))]} onChange={(value) => setSearchParams(value ? { server: serverId, container: value } : { server: serverId })} /></label>
      </div>
      {loadError ? <Empty description={t('server.loadFailed')}><Button onClick={() => setReload((value) => value + 1)}>{t('common.refresh')}</Button></Empty> : server ? <FileManager key={serverId + ':' + containerId} serverId={serverId} containerId={containerId || undefined} onLockedChange={setLocked} /> : <Empty description={t('files.selectServer')} />}
    </>}
  </div>;
}
