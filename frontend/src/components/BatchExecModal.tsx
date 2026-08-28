import React, { useMemo, useState } from 'react';
import { Alert, App, Button, Collapse, Input, Modal, Space, Tag, Typography } from 'antd';
import {
  CheckCircleFilled, CloseCircleFilled, CodeOutlined, PlayCircleOutlined, WarningOutlined,
} from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { serversApi, BatchResult, Server, BATCH_MAX_TARGETS } from '../api/servers';

const { Text } = Typography;

interface Props {
  open: boolean;
  servers: Server[];
  onClose: () => void;
}

/** Commands worth a second look before fanning them out over a fleet. */
const RISKY_PATTERNS: { pattern: RegExp; key: string }[] = [
  { pattern: /\brm\s+(-[a-zA-Z]*\s+)*-?[a-zA-Z]*[rf]/, key: 'rm' },
  { pattern: /\b(shutdown|reboot|halt|poweroff)\b/, key: 'reboot' },
  { pattern: /\bmkfs(\.\w+)?\b|\bdd\s+.*of=\/dev\//, key: 'disk' },
  { pattern: />\s*\/dev\/[svn][dv]/, key: 'disk' },
  { pattern: /\b(userdel|deluser|passwd)\b/, key: 'account' },
];

function detectRisk(command: string): string | null {
  const normalized = command.toLowerCase();
  for (const { pattern, key } of RISKY_PATTERNS) {
    if (pattern.test(normalized)) return key;
  }
  return null;
}

function apiError(err: unknown, fallback: string): string {
  const detail = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
  return detail || fallback;
}

/**
 * Runs one command across the selected servers and shows a per-host result.
 * Deliberately not a terminal: it is a single non-interactive command, which is
 * what makes a fleet-wide fan-out safe to reason about.
 */
export default function BatchExecModal({ open, servers, onClose }: Props) {
  const { t } = useTranslation();
  // Both from App context: the static Modal/message helpers cannot see the
  // dynamic (dark) theme, so a confirm dialog would render light on dark.
  const { message, modal } = App.useApp();
  const [command, setCommand] = useState('');
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<BatchResult[] | null>(null);

  const risk = useMemo(() => detectRisk(command), [command]);
  const overLimit = servers.length > BATCH_MAX_TARGETS;

  const reset = () => {
    setCommand('');
    setResults(null);
    setRunning(false);
  };

  const handleClose = () => {
    if (running) return;
    reset();
    onClose();
  };

  const handleRun = async () => {
    const trimmed = command.trim();
    if (!trimmed) {
      message.warning(t('batch.commandRequired'));
      return;
    }
    modal.confirm({
      title: t('batch.confirmTitle', { count: servers.length }),
      width: 560,
      content: (
        <div className="batch-confirm">
          <Text type="secondary">{t('batch.confirmBody')}</Text>
          <code>{trimmed}</code>
          <div className="batch-confirm-targets">
            {servers.map((s) => <Tag key={s.id}>{s.name}</Tag>)}
          </div>
          {risk && <Alert type="warning" showIcon title={t(`batch.risk.${risk}`)} />}
        </div>
      ),
      okText: t('batch.run'),
      okType: 'danger',
      onOk: async () => {
        setRunning(true);
        setResults(null);
        try {
          const res = await serversApi.bulkExec(servers.map((s) => s.id), trimmed);
          setResults(res.data.results || []);
          if (res.data.failed === 0) {
            message.success(t('batch.allSucceeded', { count: res.data.succeeded }));
          } else {
            message.warning(t('batch.partialSuccess', { ok: res.data.succeeded, failed: res.data.failed }));
          }
        } catch (err: unknown) {
          message.error(apiError(err, t('batch.execFailed')));
        }
        setRunning(false);
      },
    });
  };

  const items = (results || []).map((result) => ({
    key: result.server_id,
    label: (
      <div className="batch-result-head">
        {result.ok
          ? <CheckCircleFilled className="ok" />
          : <CloseCircleFilled className="bad" />}
        <strong>{result.server_name}</strong>
        {result.error && <Text type="danger" ellipsis>{result.error}</Text>}
        <span className="batch-result-duration">{result.duration_ms} ms</span>
      </div>
    ),
    children: (
      <>
        <pre className="batch-output">{result.output || t('batch.noOutput')}</pre>
        {result.truncated && <Text type="secondary">{t('batch.outputTruncated')}</Text>}
      </>
    ),
  }));

  return (
    <Modal
      title={<Space><CodeOutlined />{t('batch.execTitle')}</Space>}
      open={open}
      onCancel={handleClose}
      width={760}
      mask={{ closable: !running }}
      footer={[
        <Button key="close" onClick={handleClose} disabled={running}>{t('common.cancel')}</Button>,
        <Button
          key="run"
          type="primary"
          danger
          icon={<PlayCircleOutlined />}
          loading={running}
          disabled={overLimit}
          onClick={handleRun}
        >
          {t('batch.runOn', { count: servers.length })}
        </Button>,
      ]}
    >
      <div className="batch-exec-body">
        <Alert
          type="info"
          showIcon
          title={t('batch.execNotice')}
          description={t('batch.execNoticeDetail')}
        />

        {overLimit && (
          <Alert
            type="error"
            showIcon
            icon={<WarningOutlined />}
            title={t('batch.tooMany', { max: BATCH_MAX_TARGETS, count: servers.length })}
          />
        )}

        <div>
          <Text type="secondary" className="batch-field-label">{t('batch.targets')}</Text>
          <div className="batch-target-list">
            {servers.map((s) => (
              <Tag key={s.id} className="batch-target">{s.name}<small>{s.host}</small></Tag>
            ))}
          </div>
        </div>

        <div>
          <Text type="secondary" className="batch-field-label">{t('batch.command')}</Text>
          <Input
            value={command}
            onChange={(event) => setCommand(event.target.value)}
            onPressEnter={handleRun}
            placeholder={t('batch.commandPlaceholder')}
            prefix={<span className="batch-prompt">$</span>}
            disabled={running}
            maxLength={4096}
            allowClear
          />
          {risk && (
            <Alert className="batch-risk" type="warning" showIcon title={t(`batch.risk.${risk}`)} />
          )}
        </div>

        {results && (
          <div>
            <Text type="secondary" className="batch-field-label">{t('batch.results')}</Text>
            <Collapse
              className="batch-results"
              items={items}
              defaultActiveKey={results.filter((r) => !r.ok).map((r) => r.server_id)}
            />
          </div>
        )}
      </div>
    </Modal>
  );
}
