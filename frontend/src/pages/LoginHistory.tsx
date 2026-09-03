import React, { useCallback, useEffect, useState } from 'react';
import { Alert, Card, Segmented, Space, Table, Tag, Tooltip, Typography } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { WarningOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { authApi, LoginHistoryItem } from '../api/auth';

const { Title, Text } = Typography;

const PAGE_SIZE = 20;

type Scope = 'all' | 'failed';

export default function LoginHistory() {
  const { t } = useTranslation();
  const [records, setRecords] = useState<LoginHistoryItem[]>([]);
  const [total, setTotal] = useState(0);
  const [failed, setFailed] = useState(0);
  const [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1);
  const [scope, setScope] = useState<Scope>('all');

  const fetchData = useCallback(async (p: number, s: Scope) => {
    setLoading(true);
    try {
      const res = await authApi.getLoginHistory(PAGE_SIZE, (p - 1) * PAGE_SIZE, s === 'failed');
      setRecords(res.data.records || []);
      setTotal(res.data.total || 0);
      setFailed(res.data.failed || 0);
    } catch {
      // Leaving the previous page on screen beats blanking the table.
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    fetchData(page, scope);
  }, [page, scope, fetchData]);

  const changeScope = (next: Scope) => {
    // Page 3 of "all" is rarely page 3 of "failed"; start over rather than
    // land the reader on an empty page.
    setPage(1);
    setScope(next);
  };

  const columns: ColumnsType<LoginHistoryItem> = [
    {
      title: t('loginHistory.time'),
      dataIndex: 'logged_at',
      key: 'logged_at',
      width: 200,
      render: (v: string) => new Date(v).toLocaleString(),
    },
    {
      title: t('loginHistory.ip'),
      dataIndex: 'ip',
      key: 'ip',
      width: 160,
      render: (v: string) => <Text className="mono-cell">{v || '—'}</Text>,
    },
    {
      title: t('loginHistory.userAgent'),
      dataIndex: 'user_agent',
      key: 'user_agent',
      // The full string is long and often the only way to tell one client from
      // another, so it stays reachable on hover.
      render: (v: string) => (
        <Tooltip title={v} placement="topLeft">
          <span className="command-cell">{v || '—'}</span>
        </Tooltip>
      ),
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

      {/* Rejected attempts are the whole reason this page exists; say so up
          front rather than making the reader page through successes to find
          them. Only shown when there is something to report. */}
      {failed > 0 && (
        <Alert
          className="settings-alert"
          type="warning"
          showIcon
          icon={<WarningOutlined />}
          title={t('loginHistory.failedNotice', { count: failed })}
          description={t('loginHistory.failedHint')}
        />
      )}

      <Card className="panel-card">
        <div className="process-toolbar">
          <Space size={16} wrap className="process-summary">
            <span><Text type="secondary">{t('loginHistory.attempts')}</Text><strong>{total}</strong></span>
            <span>
              <Text type="secondary">{t('loginHistory.failedCount')}</Text>
              <strong style={failed ? { color: '#e2545f' } : undefined}>{failed}</strong>
            </span>
          </Space>
          <Segmented
            value={scope}
            onChange={(value) => changeScope(value as Scope)}
            options={[
              { value: 'all', label: t('loginHistory.scopeAll') },
              { value: 'failed', label: t('loginHistory.scopeFailed') },
            ]}
          />
        </div>
        <Table
          className="server-table"
          rowKey="id"
          columns={columns}
          dataSource={records}
          loading={loading}
          scroll={{ x: 720 }}
          // Failed attempts get a tinted row so a burst stands out while
          // scanning the unfiltered list.
          rowClassName={(record) => (record.success ? '' : 'login-row-failed')}
          pagination={{
            current: page,
            total,
            pageSize: PAGE_SIZE,
            onChange: setPage,
            showSizeChanger: false,
            showTotal: (cnt) => t('loginHistory.total', { count: cnt }),
          }}
        />
      </Card>
    </div>
  );
}
