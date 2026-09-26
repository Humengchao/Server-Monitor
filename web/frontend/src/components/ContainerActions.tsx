import type { ReactNode } from 'react';
import { Button, Space, Tooltip } from 'antd';
import { CaretRightOutlined, CodeOutlined, FileTextOutlined, FolderOpenOutlined, PauseOutlined, SyncOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import type { DockerContainer } from '../api/servers';

export type ContainerAction = 'start' | 'stop' | 'restart';

interface Props {
  container: DockerContainer;
  /** `${containerId}:${action}` of the action in flight, so only that button spins. */
  busy: string | null;
  onAction: (action: ContainerAction) => void;
  onLogs: () => void;
  onExec: () => void;
  onFiles: () => void;
}

/**
 * One row's worth of container controls as named icon buttons. Five labelled
 * buttons per row turned a host with a dozen containers into a wall of text;
 * the names live on as tooltips and accessible labels.
 */
export default function ContainerActions({ container, busy, onAction, onLogs, onExec, onFiles }: Props) {
  const { t } = useTranslation();
  const running = container.state === 'running';
  const control = (action: ContainerAction, icon: ReactNode, primary = false) => (
    <Tooltip title={t(`docker.${action}`)}>
      <Button
        size="small"
        type={primary ? 'primary' : 'text'}
        icon={icon}
        aria-label={t(`docker.${action}`)}
        loading={busy === `${container.id}:${action}`}
        onClick={() => onAction(action)}
      />
    </Tooltip>
  );
  return (
    <Space size={2} className="container-actions">
      {running ? (
        <>
          {control('stop', <PauseOutlined />)}
          {control('restart', <SyncOutlined />)}
        </>
      ) : control('start', <CaretRightOutlined />, true)}
      <Tooltip title={t('docker.logs')}><Button size="small" type="text" icon={<FileTextOutlined />} aria-label={t('docker.logs')} onClick={onLogs} /></Tooltip>
      <Tooltip title={t('docker.exec')}><Button size="small" type="text" icon={<CodeOutlined />} aria-label={t('docker.exec')} onClick={onExec} /></Tooltip>
      <Tooltip title={t('nav.files')}><Button size="small" type="text" icon={<FolderOpenOutlined />} aria-label={t('nav.files')} disabled={!running} onClick={onFiles} /></Tooltip>
    </Space>
  );
}
