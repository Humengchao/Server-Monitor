import { useCallback, useContext, useEffect, useRef, useState } from 'react';
import { Alert, App, Breadcrumb, Button, Card, Empty, Input, Modal, Progress, Space, Table, Tag, Tooltip, Typography } from 'antd';
import { ArrowUpOutlined, DownloadOutlined, EditOutlined, FileOutlined, FolderOpenOutlined, HomeOutlined, LinkOutlined, ReloadOutlined, SaveOutlined, UploadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useTranslation } from 'react-i18next';
import { fileErrorCode, filesApi } from '../api/files';
import type { FileTarget, RemoteFileEntry, RemoteFileList } from '../api/files';
import { formatBytes, formatDate } from '../utils/format';
import { UnsavedChangesContext } from '../contexts/UnsavedChangesContext';
import './FileManager.css';

const { Text } = Typography;
interface Editor { entry: RemoteFileEntry; content: string; original: string; revision: string; crlf: boolean }
interface Props extends FileTarget { onLockedChange?: (locked: boolean) => void }

export default function FileManager({ serverId, containerId, onLockedChange }: Props) {
  const { t, i18n } = useTranslation();
  const { message, modal } = App.useApp();
  const setNavigationDirty = useContext(UnsavedChangesContext);
  const [listing, setListing] = useState<RemoteFileList | null>(null);
  const [directory, setDirectory] = useState('');
  const [filter, setFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editor, setEditor] = useState<Editor | null>(null);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState(0);
  const listRequest = useRef<AbortController | null>(null);
  const operation = useRef<AbortController | null>(null);
  const uploadInput = useRef<HTMLInputElement>(null);
  const dirty = !!editor && editor.content !== editor.original;
  const locked = busy || dirty;

  useEffect(() => {
    onLockedChange?.(locked);
    setNavigationDirty(locked);
    return () => { onLockedChange?.(false); setNavigationDirty(false); };
  }, [locked, onLockedChange, setNavigationDirty]);

  useEffect(() => {
    if (!locked) return;
    const guard = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, [locked]);

  const loadDirectory = useCallback(async (path: string) => {
    listRequest.current?.abort();
    const controller = new AbortController();
    listRequest.current = controller;
    setLoading(true);
    setError('');
    try {
      const response = await filesApi.list({ serverId, containerId }, path, controller.signal);
      if (controller.signal.aborted) return;
      setListing(response.data);
      setDirectory(response.data.path);
      setFilter('');
    } catch (failure) {
      const code = await fileErrorCode(failure);
      if (!controller.signal.aborted) { setError(code); setListing(null); }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [serverId, containerId]);

  useEffect(() => {
    const timer = window.setTimeout(() => { void loadDirectory(''); }, 0);
    return () => { window.clearTimeout(timer); listRequest.current?.abort(); operation.current?.abort(); operation.current = null; };
  }, [loadDirectory]);

  const showFailure = async (failure: unknown, signal: AbortSignal) => {
    const code = await fileErrorCode(failure);
    if (!signal.aborted) message.error(t('files.error.' + code, { defaultValue: t('files.error.remote_failed') }), 6);
  };

  const startOperation = () => {
    operation.current?.abort();
    const controller = new AbortController();
    operation.current = controller;
    setBusy(true);
    setProgress(0);
    return controller;
  };

  const openFile = async (entry: RemoteFileEntry) => {
    if (entry.is_dir) { await loadDirectory(entry.path); return; }
    if (!entry.is_file) return;
    if (entry.size > 2 * 1024 * 1024) { message.info(t('files.textLimit')); return; }
    const controller = startOperation();
    try {
      const response = await filesApi.read({ serverId, containerId }, entry.path, controller.signal);
      if (!controller.signal.aborted) {
        const text = response.data.content.replace(/\r\n/g, '\n');
        setEditor({ entry, content: text, original: text, revision: response.data.revision, crlf: response.data.content.includes('\r\n') });
      }
    } catch (failure) { await showFailure(failure, controller.signal); }
    finally { if (operation.current === controller) setBusy(false); }
  };

  const closeEditor = () => {
    if (busy) return;
    if (!dirty) { setEditor(null); return; }
    modal.confirm({ title: t('files.discardTitle'), content: t('files.discardPrompt'), okText: t('files.discard'), okType: 'danger', onOk: () => setEditor(null) });
  };

  const saveText = async () => {
    if (!editor || !dirty || busy || editor.entry.is_symlink) return;
    const content = editor.crlf ? editor.content.replace(/\r?\n/g, '\r\n') : editor.content;
    if (new TextEncoder().encode(content).byteLength > 2 * 1024 * 1024) { message.error(t('files.textLimit')); return; }
    const controller = startOperation();
    const snapshot = editor;
    try {
      const response = await filesApi.save({ serverId, containerId }, snapshot.entry.path, content, snapshot.revision, controller.signal);
      if (!controller.signal.aborted) {
        setEditor({ ...snapshot, original: snapshot.content, revision: response.data.revision });
        message.success(t('files.saved'));
        if (listing) void loadDirectory(listing.path);
      }
    } catch (failure) { await showFailure(failure, controller.signal); }
    finally { if (operation.current === controller) setBusy(false); }
  };

  const download = async (entry: RemoteFileEntry) => {
    if (entry.size > 256 * 1024 * 1024) { message.error(t('files.downloadLimit')); return; }
    const controller = startOperation();
    try {
      const response = await filesApi.download({ serverId, containerId }, entry.path, controller.signal, setProgress);
      if (!controller.signal.aborted) {
        const url = URL.createObjectURL(response.data);
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = entry.name;
        document.body.appendChild(anchor);
        anchor.click();
        anchor.remove();
        window.setTimeout(() => URL.revokeObjectURL(url), 5000);
      }
    } catch (failure) { await showFailure(failure, controller.signal); }
    finally { if (operation.current === controller) setBusy(false); }
  };

  const upload = async (files: File[]) => {
    if (!listing || busy || dirty) return;
    const controller = startOperation();
    let uploaded = 0;
    try {
      for (const file of files) {
        if (controller.signal.aborted) break;
        if (file.size > 64 * 1024 * 1024) { message.error(file.name + ': ' + t('files.uploadLimit')); continue; }
        setProgress(0);
        try {
          await filesApi.upload({ serverId, containerId }, listing.path, file, false, controller.signal, setProgress);
          uploaded++;
        } catch (failure) {
          if (controller.signal.aborted) break;
          if (await fileErrorCode(failure) !== 'file_exists') { await showFailure(failure, controller.signal); continue; }
          const overwrite = await new Promise<boolean>((resolve) => {
            modal.confirm({ title: t('files.overwriteTitle'), content: t('files.overwritePrompt', { name: file.name }), okText: t('files.overwrite'), okType: 'danger', onOk: () => resolve(true), onCancel: () => resolve(false) });
          });
          if (!overwrite || controller.signal.aborted) continue;
          try { await filesApi.upload({ serverId, containerId }, listing.path, file, true, controller.signal, setProgress); uploaded++; }
          catch (retryFailure) { await showFailure(retryFailure, controller.signal); }
        }
      }
      if (uploaded && !controller.signal.aborted) message.success(t('files.uploaded', { count: uploaded }));
    } finally {
      if (operation.current === controller) { setBusy(false); void loadDirectory(listing.path); }
    }
  };

  const columns: ColumnsType<RemoteFileEntry> = [
    { title: t('common.name'), key: 'name', render: (_, entry) => <Button type="link" className="file-name" disabled={locked || (!entry.is_dir && !entry.is_file)} onClick={() => { void openFile(entry); }} icon={entry.is_dir ? <FolderOpenOutlined /> : <FileOutlined />}><span>{entry.name}</span>{entry.is_symlink && <LinkOutlined />}</Button> },
    { title: t('files.size'), key: 'size', width: 110, render: (_, entry) => entry.is_dir ? '—' : formatBytes(entry.size), sorter: (first, second) => first.size - second.size },
    { title: t('files.modified'), dataIndex: 'modified_at', width: 190, render: (value: string) => formatDate(value, i18n.language, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) },
    { title: t('files.permissions'), dataIndex: 'mode', width: 120, render: (value: string) => <Text code>{value}</Text> },
    { title: t('common.actions'), key: 'actions', width: 190, render: (_, entry) => entry.is_file && <Space><Tooltip title={t('files.textLimit')}><Button size="small" icon={<EditOutlined />} disabled={locked || entry.size > 2 * 1024 * 1024} onClick={() => { void openFile(entry); }}>{t('files.openText')}</Button></Tooltip><Button size="small" icon={<DownloadOutlined />} disabled={locked} onClick={() => { void download(entry); }}>{t('files.download')}</Button></Space> },
  ];
  const parts = (listing?.path || '').split('/').filter(Boolean);
  const breadcrumbs = [{ title: '/', onClick: () => { if (!locked) void loadDirectory('/'); } }, ...parts.map((part, index) => {
    let path = (listing?.path.startsWith('/') ? '/' : '') + parts.slice(0, index + 1).join('/');
    if (/^\/?[A-Za-z]:$/.test(path)) path += '/';
    return { title: part, onClick: () => { if (!locked) void loadDirectory(path); } };
  })];

  return <Card className="panel-card file-manager">
    <Alert type="info" showIcon title={t('files.limits')} description={containerId ? t('files.containerHint') : t('files.hostHint')} />
    <div className="file-toolbar">
      <Space><Tooltip title={t('files.home')}><Button icon={<HomeOutlined />} disabled={locked} onClick={() => { void loadDirectory(''); }} /></Tooltip><Tooltip title={t('files.parent')}><Button icon={<ArrowUpOutlined />} disabled={locked || !listing || listing.parent === listing.path} onClick={() => { if (listing) void loadDirectory(listing.parent); }} /></Tooltip></Space>
      <Input.Search value={directory} onChange={(event) => setDirectory(event.target.value)} onSearch={(value) => { void loadDirectory(value); }} disabled={locked} placeholder={t('files.pathPlaceholder')} enterButton={t('files.go')} aria-label={t('files.pathPlaceholder')} />
      <Button icon={<ReloadOutlined />} loading={loading} disabled={locked} onClick={() => { void loadDirectory(listing?.path || directory); }}>{t('common.refresh')}</Button>
      <Button type="primary" icon={<UploadOutlined />} disabled={locked || loading || !listing} onClick={() => uploadInput.current?.click()}>{t('files.upload')}</Button>
      <input ref={uploadInput} type="file" multiple hidden onChange={(event) => { const files = Array.from(event.target.files || []); event.target.value = ''; void upload(files); }} />
    </div>
    {busy && <div className="file-transfer"><Progress percent={progress} status="active" /><Text type="secondary">{t('files.working')}</Text></div>}
    {error && <Alert type="error" showIcon title={t('files.error.' + error, { defaultValue: t('files.error.remote_failed') })} />}
    {listing && <div className="file-location"><Breadcrumb items={breadcrumbs} /><Input.Search placeholder={t('files.filter')} value={filter} allowClear onChange={(event) => setFilter(event.target.value)} aria-label={t('files.filter')} /></div>}
    {listing?.truncated && <Alert type="warning" showIcon title={t('files.truncated')} />}
    <Table rowKey="path" columns={columns} dataSource={listing?.entries.filter((entry) => entry.name.toLowerCase().includes(filter.toLowerCase())) || []} loading={loading} size="small" scroll={{ x: 820 }} pagination={{ pageSize: 50, showSizeChanger: true, pageSizeOptions: [25, 50, 100] }} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={error ? t('files.loadFailed') : t('files.empty')} /> }} />
    <Modal destroyOnHidden open={!!editor} title={<Space><FileOutlined /><span className="file-editor-name">{editor?.entry.path}</span>{dirty && <Tag color="orange">{t('files.unsaved')}</Tag>}</Space>} width="min(1100px, 95vw)" onCancel={closeEditor} mask={{ closable: !dirty && !busy }} keyboard={!busy} footer={<Space><Button disabled={busy} onClick={closeEditor}>{t('files.close')}</Button><Button type="primary" icon={<SaveOutlined />} loading={busy} disabled={!dirty || editor?.entry.is_symlink} onClick={() => { void saveText(); }}>{t('common.save')}</Button></Space>}>
      {editor?.entry.is_symlink && <Alert type="warning" showIcon title={t('files.symlinkReadOnly')} />}
      <Input.TextArea className="file-editor" value={editor?.content || ''} readOnly={busy || editor?.entry.is_symlink} spellCheck={false} rows={24} onChange={(event) => setEditor((current) => current ? { ...current, content: event.target.value } : current)} onKeyDown={(event) => { if ((event.ctrlKey || event.metaKey) && event.key === 's') { event.preventDefault(); void saveText(); } }} />
      <Text type="secondary">{t('files.textLimit')} · UTF-8 · Ctrl/Cmd+S</Text>
    </Modal>
  </Card>;
}
