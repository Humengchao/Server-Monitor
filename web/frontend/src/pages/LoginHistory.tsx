import React, { useEffect, useState } from 'react';
import { Button, Card, Result, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { ReloadOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { authApi, LoginHistoryItem } from '../api/auth';
import { formatDateTime } from '../utils/format';

const { Title, Text } = Typography;

export default function LoginHistory() {
  const { t, i18n } = useTranslation();
  const [records, setRecords] = useState<LoginHistoryItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [page, setPage] = useState(1);

  const fetchData = async (p: number) => {
    setLoading(true);
    try {
      const res = await authApi.getLoginHistory(20, (p - 1) * 20);
      setRecords(res.data.records || []);
      setTotal(res.data.total || 0);
      setError(false);
    } catch {
      setError(true);
    }
    setLoading(false);
  };

  useEffect(() => {
    const timer = window.setTimeout(() => { void fetchData(page); }, 0);
    return () => window.clearTimeout(timer);
  }, [page]);

  const columns: ColumnsType<LoginHistoryItem> = [
    {
      title: t('loginHistory.time'),
      dataIndex: 'logged_at',
      key: 'logged_at',
      render: (v: string) => formatDateTime(v, i18n.language),
    },
    {
      title: t('loginHistory.ip'),
      dataIndex: 'ip',
      key: 'ip',
    },
    {
      title: t('loginHistory.userAgent'),
      dataIndex: 'user_agent',
      key: 'user_agent',
      ellipsis: true,
    },
    {
      title: t('common.status'),
      dataIndex: 'success',
      key: 'success',
      width: 100,
      render: (v: boolean) =>
        v ? <Tag color="success">{t('common.success')}</Tag> : <Tag color="error">{t('common.failed')}</Tag>,
    },
  ];

  return (
    <div>
      <div className="page-heading">
        <div>
          <Text className="eyebrow">{t('loginHistory.eyebrow')}</Text>
          <Title level={2}>{t('loginHistory.title')}</Title>
          <Text type="secondary">{t('loginHistory.subtitle')}</Text>
        </div>
      </div>
      <Card className="panel-card">
      {error && records.length === 0 ? (
        <Result
          status="error"
          title={t('loginHistory.loadFailed')}
          extra={<Button type="primary" icon={<ReloadOutlined />} onClick={() => { void fetchData(page); }}>{t('common.refresh')}</Button>}
        />
      ) : (
      <Table
        className="server-table"
        rowKey="id"
        columns={columns}
        dataSource={records}
        loading={loading}
        scroll={{ x: 640 }}
        pagination={{
          current: page,
          total,
          pageSize: 20,
          onChange: setPage,
          showTotal: (cnt) => t('loginHistory.total', { count: cnt }),
        }}
      />
      )}
      </Card>
    </div>
  );
}
