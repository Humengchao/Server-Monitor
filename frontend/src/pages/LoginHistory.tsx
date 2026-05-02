import React, { useEffect, useState } from 'react';
import { Table, Tag, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { authApi, LoginHistoryItem } from '../api/auth';

const { Title } = Typography;

export default function LoginHistory() {
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
    fetchData(page);
  }, [page]);

  const columns: ColumnsType<LoginHistoryItem> = [
    {
      title: 'Time',
      dataIndex: 'logged_at',
      key: 'logged_at',
      render: (v: string) => new Date(v).toLocaleString(),
    },
    {
      title: 'IP',
      dataIndex: 'ip',
      key: 'ip',
    },
    {
      title: 'User Agent',
      dataIndex: 'user_agent',
      key: 'user_agent',
      ellipsis: true,
    },
    {
      title: 'Status',
      dataIndex: 'success',
      key: 'success',
      width: 100,
      render: (v: boolean) =>
        v ? <Tag color="success">Success</Tag> : <Tag color="error">Failed</Tag>,
    },
  ];

  return (
    <div>
      <Title level={4} style={{ marginBottom: 16 }}>Login History</Title>
      <Table
        rowKey="id"
        columns={columns}
        dataSource={records}
        loading={loading}
        pagination={{
          current: page,
          total,
          pageSize: 20,
          onChange: setPage,
          showTotal: (t) => `Total ${t} records`,
        }}
      />
    </div>
  );
}
