import React, { useCallback, useEffect, useState } from 'react';
import { Layout as AntLayout, Menu, Button, Avatar, Badge, Space, Tooltip, Typography } from 'antd';
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
} from '@ant-design/icons';
import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/authStore';
import { alertsApi } from '../api/alerts';
import { usePolling } from '../hooks/usePolling';

const { Header, Sider, Content } = AntLayout;
const { Text } = Typography;

interface Props {
  darkMode: boolean;
  onToggleTheme: () => void;
}

export default function AppLayout({ darkMode, onToggleTheme }: Props) {
  const navigate = useNavigate();
  const location = useLocation();
  const { user, logout } = useAuthStore();
  const { t, i18n } = useTranslation();
  const [activeAlerts, setActiveAlerts] = useState(0);
  const [isOnline, setIsOnline] = useState(() => navigator.onLine);

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
    alertsApi.summary()
      .then((res) => setActiveAlerts(res.data?.active || 0))
      .catch(() => undefined);
  }, []);

  // The alert engine evaluates on its own cadence, so a 30s poll is enough to
  // keep the badge honest without adding noticeable request load.
  usePolling(refreshAlertBadge, 30000, { leading: false });

  // Also refresh on navigation, so acknowledging an alert and leaving the page
  // updates the badge without waiting out the interval.
  useEffect(() => {
    refreshAlertBadge();
  }, [location.pathname, refreshAlertBadge]);

  const menuItems = [
    { key: '/dashboard', icon: <DashboardOutlined />, label: t('nav.servers') },
    {
      key: '/alerts',
      icon: <BellOutlined />,
      label: (
        <span className="nav-label-with-badge">
          {t('nav.alerts')}
          {activeAlerts > 0 && <em>{activeAlerts > 99 ? '99+' : activeAlerts}</em>}
        </span>
      ),
    },
    { key: '/docker', icon: <DockerOutlined />, label: t('nav.docker') },
    { key: '/credentials', icon: <KeyOutlined />, label: t('nav.credentials') },
    { key: '/login-history', icon: <HistoryOutlined />, label: t('nav.loginHistory') },
    { key: '/settings', icon: <SettingOutlined />, label: t('nav.settings') },
  ];

  const toggleLang = () => {
    const next = i18n.language === 'en' ? 'zh' : 'en';
    i18n.changeLanguage(next);
    localStorage.setItem('lang', next);
  };

  const handleLogout = () => {
    logout();
    navigate('/login');
  };

  const selectedKey = location.pathname.startsWith('/servers/') ? '/dashboard' : location.pathname;
  const currentItem = menuItems.find((item) => item.key === selectedKey);
  const currentLabel = selectedKey === '/alerts' ? t('nav.alerts') : currentItem?.label;
  const userInitial = user?.username?.trim().charAt(0).toUpperCase() || 'U';

  return (
    <AntLayout className="app-shell">
      <Sider className="app-sidebar" width={248} breakpoint="lg" collapsedWidth="0">
        <div className="brand-lockup">
          <div className="brand-mark"><CloudServerOutlined /></div>
          <div className="brand-copy">
            <span>{t('app.title')}</span>
            <small>CONTROL CENTER</small>
          </div>
        </div>
        <Menu
          className="sidebar-menu"
          theme="dark"
          mode="inline"
          selectedKeys={[selectedKey]}
          items={menuItems}
          onClick={({ key }) => navigate(key)}
        />
        <div className="sidebar-footer">
          <span className={`sidebar-status-dot${activeAlerts > 0 ? ' alerting' : ''}`} />
          <span>{activeAlerts > 0 ? t('nav.systemAlerting', { count: activeAlerts }) : t('nav.systemOnline')}</span>
        </div>
      </Sider>
      <AntLayout className="app-main">
        <Header className="app-header">
          <div className="page-context">
            <Text type="secondary" className="page-context-label">{t('nav.workspace')}</Text>
            <strong>{currentLabel || t('server.serverDetail')}</strong>
          </div>
          <Space size={6} className="header-actions">
            <Tooltip title={t('nav.alerts')}>
              <Badge count={activeAlerts} size="small" offset={[-4, 4]}>
                <Button
                  className="header-icon-button"
                  type="text"
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
              <span>{user?.username}</span>
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
            <Outlet />
          </div>
        </Content>
      </AntLayout>
    </AntLayout>
  );
}
