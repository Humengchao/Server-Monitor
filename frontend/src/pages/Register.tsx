import React, { useState } from 'react';
import { Form, Input, Button, Card, Typography, App } from 'antd';
import { UserOutlined, LockOutlined } from '@ant-design/icons';
import { useNavigate, Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { authApi } from '../api/auth';

const { Title, Text } = Typography;

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
    } catch (err: any) {
      message.error(err.response?.data?.error || t('register.failed'));
    }
    setLoading(false);
  };

  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f0f2f5' }}>
      <Card style={{ width: 400, borderRadius: 12 }}>
        <Title level={3} style={{ textAlign: 'center', marginBottom: 32 }}>{t('register.title')}</Title>
        <Form onFinish={handleRegister} size="large">
          <Form.Item name="username" rules={[
            { required: true, message: t('register.usernameRequired') },
            { min: 3, message: t('register.usernameMin') },
          ]}>
            <Input prefix={<UserOutlined />} placeholder={t('register.usernamePlaceholder')} />
          </Form.Item>
          <Form.Item name="password" rules={[
            { required: true, message: t('register.passwordRequired') },
            { min: 6, message: t('register.passwordMin') },
          ]}>
            <Input.Password prefix={<LockOutlined />} placeholder={t('register.passwordPlaceholder')} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={loading} block>
              {t('register.submit')}
            </Button>
          </Form.Item>
        </Form>
        <div style={{ textAlign: 'center' }}>
          <Text>{t('register.hasAccount')}</Text>
          <Link to="/login">{t('register.login')}</Link>
        </div>
      </Card>
    </div>
  );
}
