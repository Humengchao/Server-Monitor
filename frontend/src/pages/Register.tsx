import React, { useState } from 'react';
import { Form, Input, Button, Card, Typography, App } from 'antd';
import { UserOutlined, LockOutlined } from '@ant-design/icons';
import { useNavigate, Link } from 'react-router-dom';
import { authApi } from '../api/auth';

const { Title, Text } = Typography;

export default function Register() {
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();

  const handleRegister = async (values: { username: string; password: string }) => {
    setLoading(true);
    try {
      await authApi.register(values.username, values.password);
      message.success('Registration successful, please login');
      navigate('/login');
    } catch (err: any) {
      message.error(err.response?.data?.error || 'Registration failed');
    }
    setLoading(false);
  };

  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f0f2f5' }}>
      <Card style={{ width: 400, borderRadius: 12 }}>
        <Title level={3} style={{ textAlign: 'center', marginBottom: 32 }}>Register</Title>
        <Form onFinish={handleRegister} size="large">
          <Form.Item name="username" rules={[
            { required: true, message: 'Enter a username' },
            { min: 3, message: 'Username must be at least 3 characters' },
          ]}>
            <Input prefix={<UserOutlined />} placeholder="Username" />
          </Form.Item>
          <Form.Item name="password" rules={[
            { required: true, message: 'Enter a password' },
            { min: 6, message: 'Password must be at least 6 characters' },
          ]}>
            <Input.Password prefix={<LockOutlined />} placeholder="Password" />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={loading} block>
              Register
            </Button>
          </Form.Item>
        </Form>
        <div style={{ textAlign: 'center' }}>
          <Text>Already have an account? </Text>
          <Link to="/login">Login</Link>
        </div>
      </Card>
    </div>
  );
}
