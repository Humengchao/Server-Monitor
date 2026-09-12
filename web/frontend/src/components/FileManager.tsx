import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react';
import { Alert, App, Breadcrumb, Button, Card, Empty, Input, Modal, Progress, Space, Spin, Table, Tag, Tooltip, Typography } from 'antd';
import { ArrowUpOutlined, DownloadOutlined, EditOutlined, FileOutlined, FolderOpenOutlined, DeleteOutlined, PlusOutlined, HistoryOutlined, HomeOutlined, LinkOutlined, ReloadOutlined, SaveOutlined, UploadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useTranslation } from 'react-i18next';
import { fileErrorCode, filesApi } from '../api/files';
import type { FileTarget, RemoteFileEntry, RemoteFileList, FileAudit } from '../api/files';
import { formatBytes, formatDate } from '../utils/format';
import { useFileSessionStore } from '../store/fileSessionStore';
import './FileManager.css';

const FileCodeEditor = lazy(() => import('./FileCodeEditor'));

const { Text } = Typography;
interface Editor { entry: RemoteFileEntry; content: string; original: string; revision: string; crlf: boolean }
interface Props extends FileTarget { onLockedChange?: (locked: boolean) => void }

export default function FileManager({ serverId, containerId, onLockedChange }: Props) {
  const { t, i18n } = useTranslation();
  const { message, modal } = App.useApp();
  const [listing, setListing] = useState<RemoteFileList | null>(null);
  const [directory, setDirectory] = useState('');
  const [filter, setFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editor, setEditor] = useState<Editor | null>(null);
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState(0);
  const [phase, setPhase] = useState('reading');
  const [cursor, setCursor] = useState('');
  const [previousCursors, setPreviousCursors] = useState<string[]>([]);
  const [history, setHistory] = useState<FileAudit[] | null>(null);
  const [change, setChange] = useState<{ action: 'create' | 'rename'; kind: string; name: string; entry?: RemoteFileEntry; version?: string } | null>(null);
  const listRequest = useRef<AbortController | null>(null);
  const operation = useRef<AbortController | null>(null);
  const uploadInput = useRef<HTMLInputElement>(null);
  const confirmations = useRef(new Set<ReturnType<typeof modal.confirm>>());
  const confirm = (options: Parameters<typeof modal.confirm>[0]) => {
    const instance = modal.confirm({ ...options, afterClose: () => { confirmations.current.delete(instance); options.afterClose?.(); } });
    confirmations.current.add(instance);
    return instance;
  };
  const dirty = !!editor && editor.content !== editor.original;
  const locked = busy || dirty;

  useEffect(() => {
    onLockedChange?.(locked);
    useFileSessionStore.setState({ locked, draft: dirty && editor ? { name: editor.entry.name, path: editor.entry.path, content: editor.crlf ? editor.content.replace(/\r?\n/g, '\r\n') : editor.content } : null });
  }, [locked, dirty, editor, onLockedChange]);
  useEffect(() => () => { onLockedChange?.(false); useFileSessionStore.setState({ locked: false, draft: null }); }, [onLockedChange]);

  const loadDirectory = useCallback(async (path: string, nextCursor = '', previous: string[] = []) => {
    listRequest.current?.abort();
    const controller = new AbortController();
    listRequest.current = controller;
    setLoading(true);
    setError('');
    try {
      const response = await filesApi.list({ serverId, containerId }, path, controller.signal, nextCursor);
      if (controller.signal.aborted) return;
      setCursor(nextCursor);
      setPreviousCursors(previous);
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
    const pendingConfirmations = confirmations.current;
    const timer = window.setTimeout(() => { void loadDirectory(''); }, 0);
    return () => { window.clearTimeout(timer); listRequest.current?.abort(); operation.current?.abort(); operation.current = null; for (const instance of pendingConfirmations) instance.destroy(); pendingConfirmations.clear(); };
  }, [loadDirectory]);

  const showFailure = async (failure: unknown, signal: AbortSignal) => {
    const code = await fileErrorCode(failure);
    if (!signal.aborted) message.error(t('files.error.' + code, { defaultValue: t('files.error.remote_failed') }), 6);
  };

  const startOperation = (nextPhase = 'reading') => {
    setPhase(nextPhase);
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
    confirm({ title: t('files.discardTitle'), content: t('files.discardPrompt'), okText: t('files.discard'), okType: 'danger', onOk: () => setEditor(null) });
  };

  const saveText = async () => {
    if (!editor || !dirty || busy || editor.entry.is_symlink) return;
    const content = editor.crlf ? editor.content.replace(/\r?\n/g, '\r\n') : editor.content;
    if (new TextEncoder().encode(content).byteLength > 2 * 1024 * 1024) { message.error(t('files.textLimit')); return; }
    const controller = startOperation('saving');
    const snapshot = editor;
    try {
      const response = await filesApi.save({ serverId, containerId }, snapshot.entry.path, content, snapshot.revision, controller.signal);
      if (!controller.signal.aborted) {
        setEditor({ ...snapshot, original: snapshot.content, revision: response.data.revision });
        message.success(t('files.savedWithBackup'));
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
    const controller = startOperation('preparing');
    let uploaded = 0;
    try {
      for (const file of files) {
        if (controller.signal.aborted) break;
        if (file.size > 64 * 1024 * 1024) { message.error(file.name + ': ' + t('files.uploadLimit')); continue; }
        setProgress(0);
        setPhase('preparing');
        try {
          const filename = listing.path.replace(/\/$/, '') + '/' + file.name;
          const metadata = (await filesApi.metadata({ serverId, containerId }, filename, controller.signal)).data;
          if (controller.signal.aborted) break;
          if (metadata.exists && (!metadata.entry?.is_file || metadata.entry.is_symlink)) { message.error(t('files.error.not_regular')); continue; }
          if (metadata.exists) {
            const overwrite = await new Promise<boolean>(resolve => confirm({ title: t('files.overwriteTitle'), content: t('files.overwritePrompt', { name: file.name }), okText: t('files.overwrite'), okType: 'danger', onOk: () => resolve(true), onCancel: () => resolve(false), afterClose: () => resolve(false) }));
            if (!overwrite || controller.signal.aborted) continue;
          }
          setPhase('uploading');
          await filesApi.upload({ serverId, containerId }, listing.path, file, metadata.exists, metadata.version, controller.signal, percent => { setProgress(Math.min(percent, 99)); if (percent >= 100) setPhase('writing'); });
          uploaded++;
        } catch (failure) { await showFailure(failure, controller.signal); }
      }
      if (uploaded && !controller.signal.aborted) message.success(t('files.uploaded', { count: uploaded }));
    } finally { if (operation.current === controller) { setBusy(false); if (!controller.signal.aborted) void loadDirectory(listing.path); } }
  };

  const prepareChange = async (entry: RemoteFileEntry, action: 'rename' | 'delete') => {
    const controller = startOperation('preparing');
    try {
      const metadata = (await filesApi.metadata({ serverId, containerId }, entry.path, controller.signal)).data;
      if (controller.signal.aborted) return;
      if (!metadata.exists) { message.error(t('files.error.not_found')); return; }
      if (action === 'rename') setChange({ action, kind: '', name: entry.name, entry, version: metadata.version });
      else confirm({ title: t('files.deleteTitle'), content: t('files.deletePrompt', { path: entry.path }), okText: t('common.delete'), okType: 'danger', onOk: async () => {
        if (controller.signal.aborted) return;
        const deletion = startOperation('changing');
        try { await filesApi.change({ serverId, containerId }, entry.path, { action: 'delete', version: metadata.version }, deletion.signal); message.success(t('files.changed')); if (listing) void loadDirectory(listing.path); }
        catch (failure) { await showFailure(failure, deletion.signal); throw failure; }
        finally { if (operation.current === deletion) setBusy(false); }
      } });
    } catch (failure) { await showFailure(failure, controller.signal); }
    finally { if (operation.current === controller) setBusy(false); }
  };
  const applyChange = async () => {
    if (!change || !listing || busy) return;
    if (!change.name || change.name === '.' || change.name === '..' || /[/\\]/.test(change.name) || Array.from(change.name).some(character => character.charCodeAt(0) < 32) || new TextEncoder().encode(change.name).length > 255) { message.error(t('files.error.invalid_path')); return; }
    const controller = startOperation('changing');
    try {
      const filename = change.entry?.path || listing.path.replace(/\/$/, '') + '/' + change.name;
      await filesApi.change({ serverId, containerId }, filename, change, controller.signal);
      if (!controller.signal.aborted) { setChange(null); message.success(t('files.changed')); void loadDirectory(listing.path); }
    } catch (failure) { await showFailure(failure, controller.signal); }
    finally { if (operation.current === controller) setBusy(false); }
  };
  const loadHistory = async () => {
    const controller = startOperation();
    try { const response = await filesApi.history({ serverId, containerId }, controller.signal); if (!controller.signal.aborted) setHistory(response.data); }
    catch (failure) { await showFailure(failure, controller.signal); }
    finally { if (operation.current === controller) setBusy(false); }
  };

  const columns: ColumnsType<RemoteFileEntry> = [
    { title: t('common.name'), key: 'name', render: (_, entry) => <Button type="link" aria-label={entry.name} className="file-name" disabled={locked || (!entry.is_dir && !entry.is_file)} onClick={() => { void openFile(entry); }} icon={entry.is_dir ? <FolderOpenOutlined /> : <FileOutlined />}><span>{entry.name}</span>{entry.is_symlink && <LinkOutlined />}</Button> },
    { title: t('files.size'), key: 'size', width: 110, render: (_, entry) => entry.is_dir ? '—' : formatBytes(entry.size), sorter: (first, second) => first.size - second.size },
    { title: t('files.modified'), dataIndex: 'modified_at', width: 190, render: (value: string) => formatDate(value, i18n.language, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) },
    { title: t('files.permissions'), dataIndex: 'mode', width: 120, render: (value: string) => <Text code>{value}</Text> },
    { title: t('common.actions'), key: 'actions', width: 340, render: (_, entry) => <Space wrap>{entry.is_file && <><Button size="small" icon={<EditOutlined />} disabled={locked || entry.size > 2 * 1024 * 1024} onClick={() => { void openFile(entry); }}>{t('files.openText')}</Button><Button size="small" icon={<DownloadOutlined />} disabled={locked} onClick={() => { void download(entry); }}>{t('files.download')}</Button></>}<Button size="small" disabled={locked} onClick={() => { void prepareChange(entry, 'rename'); }}>{t('files.rename')}</Button><Button size="small" danger icon={<DeleteOutlined />} disabled={locked} onClick={() => { void prepareChange(entry, 'delete'); }}>{t('common.delete')}</Button></Space> },
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
    <Space className="file-toolbar" wrap><Button icon={<PlusOutlined />} disabled={locked || !listing} onClick={() => setChange({ action: 'create', kind: 'file', name: '' })}>{t('files.newFile')}</Button><Button icon={<FolderOpenOutlined />} disabled={locked || !listing} onClick={() => setChange({ action: 'create', kind: 'directory', name: '' })}>{t('files.newDirectory')}</Button><Button icon={<HistoryOutlined />} disabled={locked} onClick={() => { void loadHistory(); }}>{t('files.history')}</Button></Space>
    {busy && <div className="file-transfer"><Progress percent={progress} status="active" showInfo={phase === 'uploading'} /><Text type="secondary">{t('files.phase.' + phase)}</Text></div>}
    {error && <Alert type="error" showIcon title={t('files.error.' + error, { defaultValue: t('files.error.remote_failed') })} />}
    {listing && <div className="file-location"><Breadcrumb items={breadcrumbs} /><Input.Search placeholder={t('files.filter')} value={filter} allowClear onChange={(event) => setFilter(event.target.value)} aria-label={t('files.filter')} /></div>}
    {listing?.truncated && <Alert type="warning" showIcon title={t('files.truncated')} />}
    <Table rowKey="path" columns={columns} dataSource={listing?.entries.filter((entry) => entry.name.toLowerCase().includes(filter.toLowerCase())) || []} loading={loading} size="small" scroll={{ x: 820 }} pagination={false} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={error ? t('files.loadFailed') : t('files.empty')} /> }} />
    <Space className="file-pagination"><Button disabled={locked || loading || !previousCursors.length} onClick={() => { if (listing) void loadDirectory(listing.path, previousCursors[previousCursors.length - 1], previousCursors.slice(0, -1)); }}>{t('files.previousPage')}</Button><Text>{t('files.pageNumber', { count: previousCursors.length + 1 })}</Text><Button disabled={locked || loading || !listing?.next_cursor} onClick={() => { if (listing) void loadDirectory(listing.path, listing.next_cursor, [...previousCursors, cursor]); }}>{t('files.nextPage')}</Button></Space>
    <Modal open={!!change} title={change?.action === 'rename' ? t('files.rename') : change?.kind === 'directory' ? t('files.newDirectory') : t('files.newFile')} confirmLoading={busy} onOk={() => { void applyChange(); }} onCancel={() => { if (!busy) setChange(null); }}>
      <Input aria-label={t('common.name')} value={change?.name || ''} disabled={busy} onChange={event => setChange(current => current ? { ...current, name: event.target.value } : current)} onPressEnter={() => { void applyChange(); }} />
    </Modal>
    <Modal open={history !== null} title={t('files.history')} width="min(1100px,95vw)" onCancel={() => { if (!busy) setHistory(null); }} footer={null}>
      <Text type="secondary">{t('files.historyHint')}</Text>
      <Table rowKey="id" dataSource={history || []} pagination={{ pageSize: 10 }} scroll={{ x: 800 }} columns={[
        { title: t('files.modified'), dataIndex: 'created_at', render: (value: string) => formatDate(value, i18n.language, { hour: '2-digit', minute: '2-digit' }) },
        { title: t('common.actions'), dataIndex: 'action' },
        { title: t('common.name'), dataIndex: 'path' },
        { title: t('files.result'), dataIndex: 'outcome' },
        { title: t('files.backup'), dataIndex: 'backup_path', render: (value: string) => value && <Button disabled={busy} onClick={() => { void download({ name: value.split('/').pop() || 'backup', path: value, size: 0, mode: '', modified_at: '', is_dir: false, is_file: true, is_symlink: false }); }}>{t('files.download')}</Button> },
      ]} />
    </Modal>
    <Modal destroyOnHidden open={!!editor} title={<Space><FileOutlined /><span className="file-editor-name">{editor?.entry.path}</span>{dirty && <Tag color="orange">{t('files.unsaved')}</Tag>}</Space>} width="min(1100px, 95vw)" onCancel={closeEditor} mask={{ closable: !dirty && !busy }} keyboard={!busy} footer={<Space><Button disabled={busy} onClick={closeEditor}>{t('files.close')}</Button><Button type="primary" icon={<SaveOutlined />} loading={busy} disabled={!dirty || editor?.entry.is_symlink} onClick={() => { void saveText(); }}>{t('common.save')}</Button></Space>}>
      {editor?.entry.is_symlink && <Alert type="warning" showIcon title={t('files.symlinkReadOnly')} />}
      {editor && <Suspense fallback={<div className="file-editor-loading"><Spin /><Text type="secondary">{t('files.editorLoading')}</Text></div>}>
        <FileCodeEditor key={editor.entry.path} path={editor.entry.path} value={editor.content} readOnly={busy || editor.entry.is_symlink} onChange={(content) => setEditor((current) => current ? { ...current, content } : current)} onSave={() => { void saveText(); }} />
      </Suspense>}
      <Text type="secondary">{t('files.textLimit')} · UTF-8 · Ctrl/Cmd+S</Text>
    </Modal>
  </Card>;
}
