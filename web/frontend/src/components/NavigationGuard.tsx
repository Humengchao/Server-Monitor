import { useEffect } from 'react';
import { Button, Modal, Space, Typography } from 'antd';
import { Outlet, useBlocker, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/authStore';
import { downloadDraft, useFileSessionStore } from '../store/fileSessionStore';

export default function NavigationGuard() {
  const { t } = useTranslation();
  const { locked, draft } = useFileSessionStore();
  const expired = useAuthStore(state => state.expired);
  const navigate = useNavigate();
  const blocker = useBlocker(({ currentLocation, nextLocation }) => useFileSessionStore.getState().locked && (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search || currentLocation.hash !== nextLocation.hash));
  useEffect(() => {
    if (!locked) return;
    const guard = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, [locked]);
  const login = () => {
    useFileSessionStore.setState({ locked: false, draft: null });
    if (blocker.state === 'blocked') blocker.reset();
    useAuthStore.getState().logout();
    navigate('/login', { replace: true });
  };
  return <>
    <Outlet />
    <Modal open={blocker.state === 'blocked' && !expired} title={t('files.discardTitle')} zIndex={2100} onCancel={() => blocker.state === 'blocked' && blocker.reset()} onOk={() => { if (blocker.state === 'blocked') blocker.proceed(); }} okText={t('files.discard')} okButtonProps={{ danger: true }}>
      {t('files.discardPrompt')}
    </Modal>
    <Modal open={expired} title={t('files.sessionExpired')} closable={false} keyboard={false} mask={{ closable: false }} zIndex={2200} footer={<Space>{draft && <Button onClick={() => downloadDraft(draft)}>{t('files.exportDraft')}</Button>}<Button danger={!!draft} type="primary" onClick={login}>{draft ? t('files.discardAndLogin') : t('files.loginAgain')}</Button></Space>}>
      <Typography.Paragraph>{t('files.sessionExpiredHint')}</Typography.Paragraph>
      {draft && <Typography.Text code>{draft.path}</Typography.Text>}
    </Modal>
  </>;
}
