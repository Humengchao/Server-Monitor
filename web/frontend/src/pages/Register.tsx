import React, { useState } from 'react';
import { Form, Input, Button, Card, Typography, App } from 'antd';
import { UserOutlined, LockOutlined, CloudServerOutlined, CheckCircleFilled } from '@ant-design/icons';
import { useNavigate, Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { authApi } from '../api/auth';

const { Title, Text } = Typography;

function apiError(err: unknown, fallback: string): string {
  const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
  return detail || fallback;
}

export default function Register() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();

  const handleRegister = async (values: { username: string; password: string }) => {
    setLoading(true);
    try {
      await authApi.register(values.username, values.password);
      message.success(t('register.success'));
      navigate('/login');
    } catch (err: unknown) {
      message.error(apiError(err, t('register.failed')));
    }
    setLoading(false);
  };

  return (
    <div className="auth-page auth-register-page">
      <div className="auth-backdrop auth-backdrop-one" />
      <div className="auth-backdrop auth-backdrop-two" />
      <section className="auth-showcase">
        <div className="auth-brand"><span><CloudServerOutlined /></span>{t('app.title')}</div>
        <div className="auth-showcase-copy">
          <Text className="eyebrow">GET STARTED</Text>
          <Title>{t('auth.registerHeroTitle')}</Title>
          <Text>{t('auth.registerHeroSubtitle')}</Text>
          <div className="auth-checklist">
            <span><CheckCircleFilled />{t('auth.registerFeatureOne')}</span>
            <span><CheckCircleFilled />{t('auth.registerFeatureTwo')}</span>
            <span><CheckCircleFilled />{t('auth.registerFeatureThree')}</span>
          </div>
        </div>
        <Text className="auth-copyright">© {new Date().getFullYear()} Server Monitor</Text>
      </section>
      <main className="auth-form-side">
      <Card className="auth-card" variant="borderless">
        <div className="auth-mobile-brand"><CloudServerOutlined /> {t('app.title')}</div>
        <Text className="eyebrow">{t('auth.createAccount')}</Text>
        <Title level={2}>{t('register.title')}</Title>
        <Text type="secondary" className="auth-form-subtitle">{t('auth.registerSubtitle')}</Text>
        <Form onFinish={handleRegister} size="large">
          <Form.Item name="username" rules={[
            { required: true, message: t('register.usernameRequired') },
            { min: 3, message: t('register.usernameMin') },
          ]}>
            <Input autoComplete="username" prefix={<UserOutlined />} placeholder={t('register.usernamePlaceholder')} />
          </Form.Item>
          <Form.Item name="password" rules={[
            { required: true, message: t('register.passwordRequired') },
            { min: 6, message: t('register.passwordMin') },
          ]}>
            <Input.Password autoComplete="new-password" prefix={<LockOutlined />} placeholder={t('register.passwordPlaceholder')} />
          </Form.Item>
          <Form.Item className="auth-submit">
            <Button type="primary" htmlType="submit" loading={loading} block>
              {t('register.submit')}
            </Button>
          </Form.Item>
        </Form>
        <div className="auth-switch">
          <Text>{t('register.hasAccount')}</Text>
          <Link to="/login">{t('register.login')}</Link>
        </div>
      </Card>
      </main>
    </div>
  );
}
