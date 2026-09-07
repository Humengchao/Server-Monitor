import React, { useEffect, useState } from 'react';
import { Card, Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { useTranslation } from 'react-i18next';
import { authApi, LoginHistoryItem } from '../api/auth';

const { Title, Text } = Typography;

export default function LoginHistory() {
  const { t } = useTranslation();
  const [records, setRecords] = useState<LoginHistoryItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1);

  const fetchData = async (p: number) => {
    setLoading(true);
    try {
      const res = await authApi.getLoginHistory(20, (p - 1) * 20);
      setRecords(res.data.records || []);
      setTotal(res.data.total || 0);
    } catch {
      // ignore
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
      render: (v: string) => new Date(v).toLocaleString(),
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
      </Card>
    </div>
  );
}
