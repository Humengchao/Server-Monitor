import React, { Suspense, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { Layout as AntLayout, Menu, Button, Avatar, Badge, Drawer, Space, Spin, Tooltip, Typography } from 'antd';
import {
  DashboardOutlined,
  SunOutlined,
  MoonOutlined,
  SettingOutlined,
  LogoutOutlined,
  CloudServerOutlined,
  DockerOutlined,
  HistoryOutlined,
  KeyOutlined,
  BellOutlined,
  TranslationOutlined,
  DisconnectOutlined,
  FolderOpenOutlined,
  RobotOutlined,
  MenuOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/authStore';
import { alertsApi } from '../api/alerts';
import { usePolling } from '../hooks/usePolling';
import { DarkModeContext, ToggleThemeContext } from '../contexts/DarkModeContext';
import '../i18n/operations';
import '../i18n/ai';

const { Header, Sider, Content } = AntLayout;
const { Text } = Typography;

export default function AppLayout() {
  const darkMode = useContext(DarkModeContext);
  const onToggleTheme = useContext(ToggleThemeContext);
  const navigate = useNavigate();
  const location = useLocation();
  const { user } = useAuthStore();
  const { t, i18n } = useTranslation();
  const { t: op } = useTranslation('operations');
  const [activeAlerts, setActiveAlerts] = useState<number | null>(null);
  const [alertError, setAlertError] = useState(false);
  const alertRequestRef = useRef<AbortController | null>(null);
  const [isOnline, setIsOnline] = useState(() => navigator.onLine);
  const [isMobile, setIsMobile] = useState(() => window.matchMedia('(max-width: 991px)').matches);
  const [navigationLocation, setNavigationLocation] = useState<string | null>(null);
  const navigationOpen = isMobile && navigationLocation === location.key;

  useEffect(() => {
    const query = window.matchMedia('(max-width: 991px)');
    const onChange = () => {
      setIsMobile(query.matches);
      setNavigationLocation(null);
    };
    query.addEventListener('change', onChange);
    return () => query.removeEventListener('change', onChange);
  }, []);

  useEffect(() => {
    const handleOnline = () => setIsOnline(true);
    const handleOffline = () => setIsOnline(false);
    window.addEventListener('online', handleOnline);
    window.addEventListener('offline', handleOffline);
    return () => {
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('offline', handleOffline);
    };
  }, []);

  const refreshAlertBadge = useCallback(() => {
    alertRequestRef.current?.abort();
    if (!isOnline) return;
    const controller = new AbortController();
    alertRequestRef.current = controller;
    return alertsApi.summary(controller.signal).then((res) => {
      if (controller.signal.aborted) return;
      setActiveAlerts(res.data?.active || 0);
      setAlertError(false);
    }).catch(() => {
      if (!controller.signal.aborted) setAlertError(true);
    });
  }, [isOnline]);

  useEffect(() => () => alertRequestRef.current?.abort(), []);

  // The alert engine evaluates on its own cadence, so a 30s poll is enough to
  // keep the badge honest without adding noticeable request load.
  usePolling(refreshAlertBadge, 30000, { leading: false, enabled: isOnline });

  // Also refresh on navigation, so acknowledging an alert and leaving the page
  // updates the badge without waiting out the interval.
  useEffect(() => {
    refreshAlertBadge();
  }, [location.pathname, refreshAlertBadge]);

  const menuItems = [
    { key: '/ai', icon: <RobotOutlined />, label: t('title', { ns: 'ai' }) },
    { key: '/dashboard', icon: <DashboardOutlined />, label: t('nav.servers') },
    {
      key: '/alerts',
      icon: <BellOutlined />,
      label: (
        <span className="nav-label-with-badge">
          {t('nav.alerts')}
          {activeAlerts !== null && activeAlerts > 0 && <em>{activeAlerts > 99 ? '99+' : activeAlerts}</em>}
        </span>
      ),
    },
    { key: '/docker', icon: <DockerOutlined />, label: t('nav.docker') },
    { key: '/files', icon: <FolderOpenOutlined />, label: t('nav.files') },
    { key: '/network', icon: <CloudServerOutlined />, label: op('network') },
    { key: '/logs', icon: <HistoryOutlined />, label: op('logs') },
    { key: '/notifications', icon: <BellOutlined />, label: op('notifications') },
    { key: '/backups', icon: <FolderOpenOutlined />, label: op('backups') },
    { key: '/credentials', icon: <KeyOutlined />, label: t('nav.credentials') },
    { key: '/login-history', icon: <HistoryOutlined />, label: t('nav.loginHistory') },
    { key: '/settings', icon: <SettingOutlined />, label: t('nav.settings') },
  ];

  const toggleLang = () => {
    const next = i18n.language === 'en' ? 'zh' : 'en';
    i18n.changeLanguage(next);
    localStorage.setItem('lang', next);
  };

  const handleLogout = () => navigate('/login?logout=1');

  const selectedKey = location.pathname.startsWith('/servers/') ? '/dashboard' : location.pathname;
  const currentItem = menuItems.find((item) => item.key === selectedKey);
  const currentLabel = selectedKey === '/alerts' ? t('nav.alerts') : currentItem?.label;
  const userInitial = user?.username?.trim().charAt(0).toUpperCase() || 'U';
  const alertStatusUnknown = !isOnline || alertError || activeAlerts === null;
  const sidebarContent = <>
    <div className="brand-lockup">
      <div className="brand-mark"><CloudServerOutlined /></div>
      <div className="brand-copy">
        <span>{t('app.title')}</span>
        <small>{t('nav.controlCenter')}</small>
      </div>
    </div>
    <nav className="sidebar-navigation" aria-label={t('nav.navigation')}>
      <Menu
        className="sidebar-menu"
        theme="dark"
        mode="inline"
        selectedKeys={[selectedKey]}
        items={menuItems}
        onClick={({ key }) => {
          setNavigationLocation(null);
          navigate(key);
        }}
      />
    </nav>
    <div className="sidebar-footer" role="status" aria-live="polite">
      <span className={`sidebar-status-dot${alertStatusUnknown ? ' unknown' : activeAlerts > 0 ? ' alerting' : ''}`} />
      <span>{!isOnline ? t('nav.offline') : alertError ? t('nav.alertStatusUnavailable') : activeAlerts === null ? t('common.loading') : activeAlerts > 0 ? t('nav.systemAlerting', { count: activeAlerts }) : t('nav.noActiveAlerts')}</span>
      {alertError && isOnline && <Button type="text" size="small" icon={<ReloadOutlined />} aria-label={t('nav.retryAlerts')} onClick={refreshAlertBadge} />}
    </div>
  </>;

  return (
    <AntLayout className="app-shell">
      {isMobile ? <Drawer
        rootClassName="mobile-navigation-drawer"
        placement="left"
        title={t('nav.navigation')}
        open={navigationOpen}
        size={280}
        onClose={() => setNavigationLocation(null)}
        styles={{ body: { padding: 0 } }}
      >{sidebarContent}</Drawer> : <Sider className="app-sidebar" width={248}>{sidebarContent}</Sider>}
      <AntLayout className="app-main">
        <Header className="app-header">
          <Button className="mobile-menu-button header-icon-button" type="text" icon={<MenuOutlined />} aria-label={t('nav.openNavigation')} aria-expanded={navigationOpen} aria-haspopup="dialog" onClick={() => setNavigationLocation(location.key)} />
          <div className="page-context">
            <Text type="secondary" className="page-context-label">{t('nav.workspace')}</Text>
            <strong>{currentLabel || t('server.serverDetail')}</strong>
          </div>
          <Space size={6} className="header-actions">
            <Tooltip title={alertError ? t('nav.alertStatusUnavailable') : t('nav.alerts')}>
              <Badge count={activeAlerts ?? 0} size="small" offset={[-4, 4]}>
                <Button
                  className="header-icon-button"
                  type="text"
                  aria-label={t('nav.alerts')}
                  icon={<BellOutlined />}
                  onClick={() => navigate('/alerts')}
                />
              </Badge>
            </Tooltip>
            <Tooltip title={i18n.language === 'en' ? '切换到中文' : 'Switch to English'}>
              <Button className="header-icon-button" type="text" aria-label={i18n.language === 'en' ? '切换到中文' : 'Switch to English'} icon={<TranslationOutlined />} onClick={toggleLang}>
                <span className="header-action-text">{i18n.language === 'en' ? '中文' : 'EN'}</span>
              </Button>
            </Tooltip>
            <Tooltip title={darkMode ? t('theme.light') : t('theme.dark')}>
              <Button className="header-icon-button" type="text" aria-label={darkMode ? t('theme.light') : t('theme.dark')} icon={darkMode ? <SunOutlined /> : <MoonOutlined />} onClick={onToggleTheme} />
            </Tooltip>
            <div className="header-divider" />
            <div className="user-chip">
              <Avatar size={34}>{userInitial}</Avatar>
              <span title={user?.username}>{user?.username}</span>
            </div>
            <Tooltip title={t('nav.logout')}>
              <Button className="header-icon-button" type="text" aria-label={t('nav.logout')} icon={<LogoutOutlined />} onClick={handleLogout} />
            </Tooltip>
          </Space>
        </Header>
        <Content className="app-content">
          <div className="content-inner">
            {!isOnline && (
              <div className="network-banner" role="status" aria-live="polite">
                <DisconnectOutlined />
                <span>{t('nav.networkOffline')}</span>
              </div>
            )}
            <Suspense fallback={<div className="page-loading" role="status" aria-label={t('common.loading')}><Spin size="large" /></div>}>
              <Outlet />
            </Suspense>
          </div>
        </Content>
      </AntLayout>
    </AntLayout>
  );
}
