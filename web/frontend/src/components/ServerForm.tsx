import { Alert, Col, DatePicker, Form, Input, InputNumber, Row, Select, Switch, Typography } from 'antd';
import type { FormInstance } from 'antd';
import { DesktopOutlined, QuestionCircleOutlined, WindowsOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import CredentialSelect from './CredentialSelect';
import TagSelect from './TagSelect';
import type { ServerFormValues } from '../utils/serverForm';
import '../i18n/operations';

const { Text } = Typography;

interface Props {
  form: FormInstance<ServerFormValues>;
  /** Editing keeps secrets optional ("leave blank to keep"); adding a Windows host needs a password. */
  editing: boolean;
  credentialId?: string;
  onCredentialChange: (id?: string) => void;
  tagIds: string[];
  onTagsChange: (ids: string[]) => void;
  disabled?: boolean;
  onFinish: (values: ServerFormValues) => void;
}

/**
 * The add/edit server form, shared by the dashboard and the detail page so the
 * two can never drift apart. Grouped into sections because fifteen fields in a
 * single column pushed the dialog's buttons off the screen.
 */
export default function ServerForm({ form, editing, credentialId, onCredentialChange, tagIds, onTagsChange, disabled, onFinish }: Props) {
  const { t } = useTranslation();
  const { t: op } = useTranslation('operations');
  const agent = Form.useWatch('connection_mode', form) === 'agent';
  const serverType = Form.useWatch('server_type', form) || 'linux';
  const sshHostKey = Form.useWatch('ssh_host_key', form);
  const windows = serverType === 'windows';
  const autoRenewHelp = <div>
    {['autoRenewHelpConditions', 'autoRenewHelpCalendar', 'autoRenewHelpLimits'].map(key =>
      <p key={key} style={{ margin: '0 0 8px' }}>{op(key)}</p>)}
    <p style={{ margin: 0 }}>{op('autoRenewHelpScope')}</p>
  </div>;

  return (
    <Form form={form} layout="vertical" onFinish={onFinish} disabled={disabled} className="server-form">
      <div className="form-section-title">{t('server.sectionConnection')}</div>
      <Form.Item name="connection_mode" label={op('connectionMode')} initialValue="ssh">
        <Select disabled={editing} options={[{ value: 'ssh', label: op('sshMode') }, { value: 'agent', label: op('agentMode') }]} onChange={(mode) => {
          onCredentialChange(undefined);
          form.setFieldsValue({ ssh_username: undefined, ssh_password: undefined, ssh_key: undefined, ssh_host_key: undefined, port: mode === 'ssh' && !windows ? 22 : undefined });
        }} />
      </Form.Item>
      {agent && <Alert type="info" showIcon title={op('agentHint')} style={{ marginBottom: 16 }} />}
      <Row gutter={12}>
        <Col xs={24} sm={14}>
          <Form.Item name="name" label={t('server.serverName')} rules={[{ required: true }]}>
            <Input placeholder={t('server.serverNamePlaceholder')} maxLength={128} />
          </Form.Item>
        </Col>
        <Col xs={24} sm={10}>
          <Form.Item name="server_type" label={t('server.type')} initialValue="linux">
            <Select
              disabled={editing && agent}
              onChange={(value) => {
                onCredentialChange(undefined);
                form.setFieldsValue(value === 'windows'
                  ? { port: undefined, ssh_username: undefined, ssh_password: undefined, ssh_key: undefined, ssh_host_key: undefined }
                  : { port: agent ? undefined : form.getFieldValue('port') || 22, ssh_username: undefined, ssh_password: undefined, ssh_key: undefined, ssh_host_key: undefined });
              }}
              options={[
                { value: 'linux', label: <><DesktopOutlined /> Linux</> },
                { value: 'windows', label: <><WindowsOutlined /> Windows</> },
              ]}
            />
          </Form.Item>
        </Col>
      </Row>
      <Row gutter={12}>
        <Col xs={24} sm={windows ? 24 : 16}>
          <Form.Item name="host" label={t('server.host')} rules={[{ required: !agent }]} extra={agent ? op('agentHostHint') : undefined}>
            <Input placeholder={t('server.hostPlaceholder')} />
          </Form.Item>
        </Col>
        {!windows && !agent && (
          <Col xs={24} sm={8}>
            <Form.Item name="port" label={t('server.sshPort')} initialValue={22}>
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
          </Col>
        )}
      </Row>

      {!agent && <><div className="form-section-title">{t('server.sectionAuth')}</div>
      <Form.Item label={t('server.credential')}>
        <CredentialSelect value={credentialId} onChange={onCredentialChange} serverType={serverType} />
      </Form.Item>
      {!credentialId && (
        <>
          <Row gutter={12}>
            <Col xs={24} sm={12}>
              <Form.Item name="ssh_username" label={t(windows ? 'server.username' : 'server.sshUsername')} rules={[{ required: true }]}>
                <Input placeholder={t(windows ? 'server.usernamePlaceholder' : 'server.sshUsernamePlaceholder')} />
              </Form.Item>
            </Col>
            <Col xs={24} sm={12}>
              <Form.Item name="ssh_password" label={t(windows ? 'server.password' : 'server.sshPassword')} rules={windows && !editing ? [{ required: true }] : undefined}>
                <Input.Password placeholder={t(editing ? 'server.sshKeyEditPlaceholder' : windows ? 'server.passwordPlaceholder' : 'server.sshPasswordPlaceholder')} />
              </Form.Item>
            </Col>
          </Row>
          {!windows && (
            <Form.Item name="ssh_key" label={t('server.sshKey')}>
              <Input.TextArea rows={3} placeholder={t(editing ? 'server.sshKeyEditPlaceholder' : 'server.sshKeyPlaceholder')} />
            </Form.Item>
          )}
        </>
      )}
      {!windows && (
        <Form.Item
          name="ssh_host_key"
          label={t('server.sshHostKey')}
          extra={sshHostKey?.trim() ? undefined : <Text type="warning">{t('server.sshHostKeyWarning')}</Text>}
        >
          <Input.TextArea rows={2} placeholder={t('server.sshHostKeyPlaceholder')} />
        </Form.Item>
      )}

      </>}
      <div className="form-section-title">{t('server.sectionBilling')}</div>
      <Row gutter={12}>
        <Col xs={24} sm={12}>
          <Form.Item name="expires_at" label={t('server.expiresAt')}>
            <DatePicker showTime style={{ width: '100%' }} placeholder={t('server.expiresAtPlaceholder')} />
          </Form.Item>
        </Col>
        <Col xs={24} sm={12}>
          <Form.Item name="traffic_limit_gb" label={t('server.trafficLimit')} initialValue={0}>
            <InputNumber min={0} precision={2} suffix="GB" style={{ width: '100%' }} />
          </Form.Item>
        </Col>
      </Row>
      <Row gutter={12}>
        <Col xs={24} sm={8}>
          <Form.Item name="billing_price" label={t('server.billingPrice')} initialValue={0}>
            <InputNumber min={0} precision={2} style={{ width: '100%' }} />
          </Form.Item>
        </Col>
        <Col xs={12} sm={8}>
          <Form.Item name="billing_currency" label={t('server.billingCurrency')} initialValue="CNY">
            <Select options={[{ value: 'CNY', label: '¥ CNY' }, { value: 'USD', label: '$ USD' }, { value: 'EUR', label: '€ EUR' }]} />
          </Form.Item>
        </Col>
        <Col xs={12} sm={8}>
          <Form.Item name="billing_cycle" label={t('server.billingCycle')} initialValue="year">
            <Select options={[
              { value: 'month', label: t('server.cycleMonth') },
              { value: 'quarter', label: t('server.cycleQuarter') },
              { value: 'half_year', label: t('server.cycleHalfYear') },
              { value: 'year', label: t('server.cycleYear') },
            ]} />
          </Form.Item>
        </Col>
      </Row>

      <div className="form-section-title">{t('server.sectionDisplay')}</div>
      <Row gutter={12}>
        <Col xs={24} sm={12}><Form.Item name="is_public" label={op('public')} htmlFor="" valuePropName="checked" initialValue={true}><Switch aria-label={op('public')} /></Form.Item></Col>
        <Col xs={24} sm={12}><Form.Item name="monitoring_enabled" label={op('monitoring')} htmlFor="" valuePropName="checked" initialValue={true}><Switch aria-label={op('monitoring')} /></Form.Item></Col>
        <Col xs={24}><Form.Item name="auto_renew_enabled" label={op('autoRenew')} htmlFor="" valuePropName="checked" initialValue={true}
          tooltip={{ title: autoRenewHelp, trigger: ['hover', 'focus'], styles: { root: { maxWidth: 'min(420px, calc(100vw - 24px))' } },
            icon: <QuestionCircleOutlined aria-label={op('autoRenewHelpLabel')} tabIndex={0} /> }}>
          <Switch aria-label={op('autoRenew')} />
        </Form.Item></Col>
      </Row>
      <Row gutter={12}>
        <Col xs={24} sm={12}>
          <Form.Item name="public_location" label={t('server.publicLocation')}>
            <Input placeholder={t('server.publicLocationPlaceholder')} maxLength={128} />
          </Form.Item>
        </Col>
        <Col xs={24} sm={12}>
          <Form.Item label={t('server.tags')}>
            <TagSelect value={tagIds} onChange={onTagsChange} />
          </Form.Item>
        </Col>
      </Row>
    </Form>
  );
}
