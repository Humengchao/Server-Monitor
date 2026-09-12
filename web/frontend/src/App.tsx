import React, { useState, useEffect, Suspense, lazy } from 'react';
import { createBrowserRouter, createRoutesFromElements, RouterProvider, Route, Navigate } from 'react-router-dom';
import { ConfigProvider, App as AntApp, Spin, theme } from 'antd';
import zhCN from 'antd/locale/zh_CN';
import enUS from 'antd/locale/en_US';
import i18n from './i18n';
import AppLayout from './components/Layout';
import { useAuthStore } from './store/authStore';
import { DarkModeContext, ToggleThemeContext } from './contexts/DarkModeContext';
import NavigationGuard from './components/NavigationGuard';

// Route-level code splitting: heavy dependencies (recharts, xterm) load only
// with the pages that use them, instead of on every first visit.
const Login = lazy(() => import('./pages/Login'));
const Register = lazy(() => import('./pages/Register'));
const Dashboard = lazy(() => import('./pages/Dashboard'));
const ServerDetail = lazy(() => import('./pages/ServerDetail'));
const Settings = lazy(() => import('./pages/Settings'));
const LoginHistory = lazy(() => import('./pages/LoginHistory'));
const Docker = lazy(() => import('./pages/Docker'));
const Files = lazy(() => import('./pages/Files'));
const Credentials = lazy(() => import('./pages/Credentials'));
const Alerts = lazy(() => import('./pages/Alerts'));
const PublicStatus = lazy(() => import('./pages/PublicStatus'));

const antdLocales: Record<string, typeof enUS> = { en: enUS, zh: zhCN };

function PrivateRoute({ children }: { children: React.ReactNode }) {
  const token = useAuthStore((s) => s.token);
  return token ? <>{children}</> : <Navigate to="/login" />;
}

const router = createBrowserRouter(createRoutesFromElements(
          <Route element={<NavigationGuard />}>
            <Route path="/login" element={<Login />} />
            <Route path="/register" element={<Register />} />
            <Route path="/status" element={<PublicStatus />} />
            <Route path="/probe" element={<Navigate to="/status" />} />
            <Route
              path="/"
              element={
                <PrivateRoute>
                  <AppLayout />
                </PrivateRoute>
              }
            >
              <Route index element={<Navigate to="/dashboard" />} />
              <Route path="dashboard" element={<Dashboard />} />
              <Route path="servers/:id" element={<ServerDetail />} />
              <Route path="settings" element={<Settings />} />
              <Route path="login-history" element={<LoginHistory />} />
              <Route path="docker" element={<Docker />} />
              <Route path="files" element={<Files />} />
              <Route path="credentials" element={<Credentials />} />
              <Route path="alerts" element={<Alerts />} />
            </Route>
            <Route path="*" element={<Navigate to="/dashboard" />} />
          </Route>
));

export default function App() {
  const [lang, setLang] = useState(i18n.language);
  // No stored choice falls back to the OS preference; keep this in sync with
  // the inline boot script in index.html.
  const [darkMode, setDarkMode] = useState(() => {
    const stored = localStorage.getItem('theme');
    if (stored === 'dark' || stored === 'light') return stored === 'dark';
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  });

  useEffect(() => {
    i18n.on('languageChanged', setLang);
    return () => { i18n.off('languageChanged', setLang); };
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = darkMode ? 'dark' : 'light';
  }, [darkMode]);

  const toggleTheme = () => {
    setDarkMode((prev) => {
      const next = !prev;
      localStorage.setItem('theme', next ? 'dark' : 'light');
      return next;
    });
  };

  return (
    <ConfigProvider
      locale={antdLocales[lang] || enUS}
      theme={{
        algorithm: darkMode ? theme.darkAlgorithm : theme.defaultAlgorithm,
        token: {
          colorPrimary: '#4f7cff',
          colorInfo: '#4f7cff',
          borderRadius: 10,
          borderRadiusLG: 16,
          controlHeight: 38,
          fontFamily: "Inter, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
        },
        components: {
          Button: { fontWeight: 600, primaryShadow: '0 8px 20px rgba(79, 124, 255, 0.22)' },
          Card: { headerFontSize: 16 },
          Menu: { itemBorderRadius: 10, itemHeight: 46 },
        },
      }}
    >
      <DarkModeContext.Provider value={darkMode}>
      <AntApp>
        <ToggleThemeContext.Provider value={toggleTheme}>
          <Suspense
            fallback={
              <div style={{ display: 'flex', minHeight: '60vh', alignItems: 'center', justifyContent: 'center' }}>
                <Spin size="large" />
              </div>
            }
          >
          <RouterProvider router={router} />
          </Suspense>
        </ToggleThemeContext.Provider>
      </AntApp>
      </DarkModeContext.Provider>
    </ConfigProvider>
  );
}
