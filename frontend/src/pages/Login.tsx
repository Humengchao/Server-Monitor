import React, { useState } from 'react';
import { Form, Input, Button, Card, Typography, App } from 'antd';
import { UserOutlined, LockOutlined, CloudServerOutlined, SafetyCertificateOutlined, LineChartOutlined, GlobalOutlined } from '@ant-design/icons';
import { useNavigate, Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAuthStore } from '../store/authStore';
import { authApi } from '../api/auth';

const { Title, Text } = Typography;

export default function Login() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();
  const { setAuth } = useAuthStore();

  const handleLogin = async (values: { username: string; password: string }) => {
    setLoading(true);
    try {
      const res = await authApi.login(values.username, values.password);
      setAuth(res.data.token, res.data.user);

      if (res.data.last_login) {
        const { ip, logged_at } = res.data.last_login;
        localStorage.setItem('last_login', JSON.stringify({ ip, logged_at }));
      }

      navigate('/dashboard');
    } catch (err: any) {
      message.error(err.response?.data?.error || t('login.failed'));
    }
    setLoading(false);
  };

  return (
    <div className="auth-page">
      <div className="auth-backdrop auth-backdrop-one" />
      <div className="auth-backdrop auth-backdrop-two" />
      <section className="auth-showcase">
        <div className="auth-brand"><span><CloudServerOutlined /></span>{t('app.title')}</div>
        <div className="auth-showcase-copy">
          <Text className="eyebrow">INFRASTRUCTURE, SIMPLIFIED</Text>
          <Title>{t('auth.heroTitle')}</Title>
          <Text>{t('auth.heroSubtitle')}</Text>
          <div className="auth-features">
            <span><LineChartOutlined />{t('auth.featureRealtime')}</span>
            <span><SafetyCertificateOutlined />{t('auth.featureSecure')}</span>
            <span><GlobalOutlined />{t('auth.featureUnified')}</span>
          </div>
        </div>
        <Text className="auth-copyright">© {new Date().getFullYear()} Server Monitor</Text>
      </section>
      <main className="auth-form-side">
      <Card className="auth-card" variant="borderless">
        <div className="auth-mobile-brand"><CloudServerOutlined /> {t('app.title')}</div>
        <Text className="eyebrow">{t('auth.welcomeBack')}</Text>
        <Title level={2}>{t('login.title')}</Title>
        <Text type="secondary" className="auth-form-subtitle">{t('auth.loginSubtitle')}</Text>
        <Form onFinish={handleLogin} size="large">
          <Form.Item name="username" rules={[{ required: true, message: t('login.usernameRequired') }]}>
            <Input prefix={<UserOutlined />} placeholder={t('login.usernamePlaceholder')} />
          </Form.Item>
          <Form.Item name="password" rules={[{ required: true, message: t('login.passwordRequired') }]}>
            <Input.Password prefix={<LockOutlined />} placeholder={t('login.passwordPlaceholder')} />
          </Form.Item>
          <Form.Item className="auth-submit">
            <Button type="primary" htmlType="submit" loading={loading} block>
              {t('login.submit')}
            </Button>
          </Form.Item>
        </Form>
        <div className="auth-switch">
          <Text>{t('login.noAccount')}</Text>
          <Link to="/register">{t('login.register')}</Link>
        </div>
      </Card>
      </main>
    </div>
  );
}
