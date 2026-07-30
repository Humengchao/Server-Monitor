import React from 'react';
import { Layout as AntLayout, Menu, Button, Avatar, Space, Tooltip, Typography } from 'antd';
import {
  DashboardOutlined,
  SunOutlined,
  MoonOutlined,
  SettingOutlined,
  LogoutOutlined,
  CloudServerOutlined,
  DockerOutlined,
  KeyOutlined,
  TranslationOutlined,
} from '@ant-design/icons';
import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/authStore';

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

  const menuItems = [
    { key: '/dashboard', icon: <DashboardOutlined />, label: t('nav.servers') },
    { key: '/docker', icon: <DockerOutlined />, label: t('nav.docker') },
    { key: '/credentials', icon: <KeyOutlined />, label: t('nav.credentials') },
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
          <span className="sidebar-status-dot" />
          <span>{t('nav.systemOnline')}</span>
        </div>
      </Sider>
      <AntLayout className="app-main">
        <Header className="app-header">
          <div className="page-context">
            <Text type="secondary" className="page-context-label">{t('nav.workspace')}</Text>
            <strong>{currentItem?.label || t('server.serverDetail')}</strong>
          </div>
          <Space size={6} className="header-actions">
            <Tooltip title={i18n.language === 'en' ? '切换到中文' : 'Switch to English'}>
              <Button className="header-icon-button" type="text" icon={<TranslationOutlined />} onClick={toggleLang}>
                <span className="header-action-text">{i18n.language === 'en' ? '中文' : 'EN'}</span>
              </Button>
            </Tooltip>
            <Tooltip title={darkMode ? t('theme.light') : t('theme.dark')}>
              <Button className="header-icon-button" type="text" icon={darkMode ? <SunOutlined /> : <MoonOutlined />} onClick={onToggleTheme} />
            </Tooltip>
            <div className="header-divider" />
            <div className="user-chip">
              <Avatar size={34}>{userInitial}</Avatar>
              <span>{user?.username}</span>
            </div>
            <Tooltip title={t('nav.logout')}>
              <Button className="header-icon-button" type="text" icon={<LogoutOutlined />} onClick={handleLogout} />
            </Tooltip>
          </Space>
        </Header>
        <Content className="app-content">
          <div className="content-inner"><Outlet /></div>
        </Content>
      </AntLayout>
    </AntLayout>
  );
}
