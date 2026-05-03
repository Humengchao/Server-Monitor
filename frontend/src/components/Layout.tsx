import React from 'react';
import { Layout as AntLayout, Menu, Button, theme } from 'antd';
import {
  DashboardOutlined,
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

export default function AppLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const { user, logout } = useAuthStore();
  const { token: { colorBgContainer } } = theme.useToken();
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

  return (
    <AntLayout style={{ minHeight: '100vh' }}>
      <Sider breakpoint="lg" collapsedWidth="0">
        <div style={{ height: 48, margin: 16, display: 'flex', alignItems: 'center', gap: 8, color: '#fff' }}>
          <CloudServerOutlined style={{ fontSize: 20 }} />
          <span style={{ fontWeight: 600, whiteSpace: 'nowrap' }}>{t('app.title')}</span>
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[location.pathname]}
          items={menuItems}
          onClick={({ key }) => navigate(key)}
        />
      </Sider>
      <AntLayout>
        <Header style={{ padding: '0 24px', background: colorBgContainer, display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 16 }}>
          <Button type="text" icon={<TranslationOutlined />} onClick={toggleLang}>
            {i18n.language === 'en' ? '中文' : 'EN'}
          </Button>
          <span>{user?.username}</span>
          <Button type="text" icon={<LogoutOutlined />} onClick={handleLogout}>{t('nav.logout')}</Button>
        </Header>
        <Content style={{ margin: 24 }}>
          <Outlet />
        </Content>
      </AntLayout>
    </AntLayout>
  );
}
