import React, { useState, useEffect } from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { ConfigProvider, App as AntApp, theme } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import enUS from 'antd/locale/en_US';
import i18n from './i18n';
import AppLayout from './components/Layout';
import Login from './pages/Login';
import Register from './pages/Register';
import Dashboard from './pages/Dashboard';
import ServerDetail from './pages/ServerDetail';
import Settings from './pages/Settings';
import LoginHistory from './pages/LoginHistory';
import Docker from './pages/Docker';
import Credentials from './pages/Credentials';
import { useAuthStore } from './store/authStore';

const antdLocales: Record<string, typeof enUS> = { en: enUS, zh: zhCN };

function PrivateRoute({ children }: { children: React.ReactNode }) {
  const token = useAuthStore((s) => s.token);
  return token ? <>{children}</> : <Navigate to="/login" />;
}

export default function App() {
  const [lang, setLang] = useState(i18n.language);
  const [darkMode, setDarkMode] = useState(() => localStorage.getItem('theme') === 'dark');

  useEffect(() => {
    i18n.on('languageChanged', setLang);
    return () => { i18n.off('languageChanged', setLang); };
  }, []);

  const toggleTheme = () => {
    setDarkMode((prev) => {
      const next = !prev;
      localStorage.setItem('theme', next ? 'dark' : 'light');
      return next;
    });
  };

  return (
    <ConfigProvider locale={antdLocales[lang] || enUS} theme={{ algorithm: darkMode ? theme.darkAlgorithm : theme.defaultAlgorithm, token: { colorPrimary: '#1890ff' } }}>
      <AntApp>
        <BrowserRouter>
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route path="/register" element={<Register />} />
            <Route
              path="/"
              element={
                <PrivateRoute>
                  <AppLayout darkMode={darkMode} onToggleTheme={toggleTheme} />
                </PrivateRoute>
              }
            >
              <Route index element={<Navigate to="/dashboard" />} />
              <Route path="dashboard" element={<Dashboard />} />
              <Route path="servers/:id" element={<ServerDetail />} />
              <Route path="settings" element={<Settings />} />
              <Route path="login-history" element={<LoginHistory />} />
              <Route path="docker" element={<Docker />} />
              <Route path="credentials" element={<Credentials />} />
            </Route>
            <Route path="*" element={<Navigate to="/dashboard" />} />
          </Routes>
        </BrowserRouter>
      </AntApp>
    </ConfigProvider>
  );
}
