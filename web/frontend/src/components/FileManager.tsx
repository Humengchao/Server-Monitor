import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { DragEvent } from 'react';
import { Alert, App, Breadcrumb, Button, Card, Empty, Input, Modal, Progress, Space, Spin, Table, Tag, Tooltip, Typography } from 'antd';
import {
  ArrowUpOutlined, CloudUploadOutlined, DeleteOutlined, DownloadOutlined, EditOutlined, FileAddOutlined, FileOutlined,
  FolderAddOutlined, FolderOpenOutlined, FormOutlined, HistoryOutlined, HomeOutlined, InfoCircleOutlined, LinkOutlined,
  ReloadOutlined, SaveOutlined, UploadOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import { useTranslation } from 'react-i18next';
import { fileErrorCode, filesApi } from '../api/files';
import type { FileTarget, RemoteFileEntry, RemoteFileList, FileAudit } from '../api/files';
import { formatBytes, formatDate } from '../utils/format';
import { useFileSessionStore } from '../store/fileSessionStore';
import './FileManager.css';

const FileCodeEditor = lazy(() => import('./FileCodeEditor'));

const { Text } = Typography;
const TEXT_LIMIT = 2 * 1024 * 1024;
interface Editor { entry: RemoteFileEntry; content: string; original: string; revision: string; crlf: boolean }
interface Props extends FileTarget { onLockedChange?: (locked: boolean) => void }

// The audit log stores "METHOD basename" for the text and upload endpoints and
// a verb for structural changes; the last word is the stable part either way.
function auditActionCode(action: string): string {
  return (action.trim().split(/\s+/).pop() || action).toLowerCase();
}

const OUTCOME_COLOR: Record<string, string> = { success: 'success', failed: 'error', pending: 'processing' };

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
  const [dragging, setDragging] = useState(false);
  // dragenter/dragleave fire for every child the pointer crosses; the overlay
  // only goes away once the pointer has left as many elements as it entered.
  const dragDepth = useRef(0);
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
    if (entry.size > TEXT_LIMIT) { message.info(t('files.textLimit')); return; }
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
    if (new TextEncoder().encode(content).byteLength > TEXT_LIMIT) { message.error(t('files.textLimit')); return; }
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

  // Dropping files anywhere on the listing uploads them into the open directory.
  const canDrop = !!listing && !locked && !loading;
  const hasFiles = (event: DragEvent) => Array.from(event.dataTransfer?.types || []).includes('Files');
  const onDragEnter = (event: DragEvent) => {
    if (!canDrop || !hasFiles(event)) return;
    event.preventDefault();
    dragDepth.current += 1;
    setDragging(true);
  };
  const onDragOver = (event: DragEvent) => {
    if (!canDrop || !hasFiles(event)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = 'copy';
  };
  const onDragLeave = () => {
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };
  const onDrop = (event: DragEvent) => {
    if (!hasFiles(event)) return;
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    if (!canDrop) return;
    const files = Array.from(event.dataTransfer.files || []);
    if (files.length) void upload(files);
  };

  const entries = listing?.entries || [];
  const needle = filter.trim().toLowerCase();
  const visibleEntries = needle ? entries.filter((entry) => entry.name.toLowerCase().includes(needle)) : entries;
  const editorStats = useMemo(() => editor
    ? { lines: editor.content.split('\n').length, bytes: new TextEncoder().encode(editor.content).byteLength }
    : null, [editor]);

  const columns: ColumnsType<RemoteFileEntry> = [
    {
      title: t('common.name'),
      key: 'name',
      width: 300,
      // Directories stay together when sorting by name, as in any file browser.
      sorter: (first, second) => Number(second.is_dir) - Number(first.is_dir) || first.name.localeCompare(second.name),
      render: (_, entry) => (
        <Button type="link" aria-label={entry.name} className="file-name" disabled={locked || (!entry.is_dir && !entry.is_file)} onClick={() => { void openFile(entry); }} icon={entry.is_dir ? <FolderOpenOutlined /> : <FileOutlined />}>
          <span>{entry.name}</span>{entry.is_symlink && <LinkOutlined />}
        </Button>
      ),
    },
    { title: t('files.size'), key: 'size', width: 110, align: 'right', className: 'file-size', render: (_, entry) => entry.is_dir ? '—' : formatBytes(entry.size), sorter: (first, second) => first.size - second.size },
    { title: t('files.modified'), dataIndex: 'modified_at', width: 190, sorter: (first, second) => Date.parse(first.modified_at) - Date.parse(second.modified_at), render: (value: string) => formatDate(value, i18n.language, { hour: '2-digit', minute: '2-digit', second: '2-digit' }) },
    { title: t('files.permissions'), dataIndex: 'mode', width: 120, render: (value: string) => <Text code>{value}</Text> },
    {
      title: t('common.actions'),
      key: 'actions',
      width: 150,
      className: 'file-actions',
      // Icon buttons with names: four labelled buttons on every row read as a
      // wall of controls, and the red Delete on each line shouted the loudest.
      render: (_, entry) => (
        <Space size={0}>
          {entry.is_file && (
            <>
              <Tooltip title={t('files.openText')}><Button type="text" size="small" icon={<EditOutlined />} aria-label={t('files.openText')} disabled={locked || entry.size > TEXT_LIMIT} onClick={() => { void openFile(entry); }} /></Tooltip>
              <Tooltip title={t('files.download')}><Button type="text" size="small" icon={<DownloadOutlined />} aria-label={t('files.download')} disabled={locked} onClick={() => { void download(entry); }} /></Tooltip>
            </>
          )}
          <Tooltip title={t('files.rename')}><Button type="text" size="small" icon={<FormOutlined />} aria-label={t('files.rename')} disabled={locked} onClick={() => { void prepareChange(entry, 'rename'); }} /></Tooltip>
          <Tooltip title={t('common.delete')}><Button type="text" size="small" danger icon={<DeleteOutlined />} aria-label={t('common.delete')} disabled={locked} onClick={() => { void prepareChange(entry, 'delete'); }} /></Tooltip>
        </Space>
      ),
    },
  ];
  const parts = (listing?.path || '').split('/').filter(Boolean);
  const breadcrumbs = [
    { title: <span className="file-crumb-root" aria-label="/"><HomeOutlined /></span>, onClick: () => { if (!locked) void loadDirectory('/'); } },
    ...parts.map((part, index) => {
      let path = (listing?.path.startsWith('/') ? '/' : '') + parts.slice(0, index + 1).join('/');
      if (/^\/?[A-Za-z]:$/.test(path)) path += '/';
      return { title: part, onClick: () => { if (!locked) void loadDirectory(path); } };
    }),
  ];

  return <Card className="panel-card file-manager">
    {containerId && <Alert type="warning" showIcon title={t('files.containerHint')} />}
    <div className="file-toolbar">
      <Space.Compact>
        <Tooltip title={t('files.home')}><Button icon={<HomeOutlined />} aria-label={t('files.home')} disabled={locked} onClick={() => { void loadDirectory(''); }} /></Tooltip>
        <Tooltip title={t('files.parent')}><Button icon={<ArrowUpOutlined />} aria-label={t('files.parent')} disabled={locked || !listing || listing.parent === listing.path} onClick={() => { if (listing) void loadDirectory(listing.parent); }} /></Tooltip>
      </Space.Compact>
      <Input.Search value={directory} onChange={(event) => setDirectory(event.target.value)} onSearch={(value) => { void loadDirectory(value); }} disabled={locked} placeholder={t('files.pathPlaceholder')} enterButton={t('files.go')} aria-label={t('files.pathPlaceholder')} />
      <div className="file-toolbar-actions">
        <Button icon={<FileAddOutlined />} disabled={locked || !listing} onClick={() => setChange({ action: 'create', kind: 'file', name: '' })}>{t('files.newFile')}</Button>
        <Button icon={<FolderAddOutlined />} disabled={locked || !listing} onClick={() => setChange({ action: 'create', kind: 'directory', name: '' })}>{t('files.newDirectory')}</Button>
        <Button type="primary" icon={<UploadOutlined />} disabled={locked || loading || !listing} onClick={() => uploadInput.current?.click()}>{t('files.upload')}</Button>
        <Tooltip title={t('files.history')}><Button icon={<HistoryOutlined />} aria-label={t('files.history')} disabled={locked} onClick={() => { void loadHistory(); }} /></Tooltip>
        <Tooltip title={t('common.refresh')}><Button icon={<ReloadOutlined />} aria-label={t('common.refresh')} loading={loading} disabled={locked} onClick={() => { void loadDirectory(listing?.path || directory); }} /></Tooltip>
        <input ref={uploadInput} type="file" multiple hidden onChange={(event) => { const files = Array.from(event.target.files || []); event.target.value = ''; void upload(files); }} />
      </div>
    </div>
    {busy && <div className="file-transfer"><Progress percent={progress} status="active" showInfo={phase === 'uploading'} /><Text type="secondary">{t('files.phase.' + phase)}</Text></div>}
    {error && <Alert type="error" showIcon title={t('files.error.' + error, { defaultValue: t('files.error.remote_failed') })} />}
    {listing && <div className="file-location">
      <Breadcrumb items={breadcrumbs} />
      <div className="file-location-tools">
        <Text type="secondary" className="file-count">{needle ? t('files.entryCountFiltered', { count: visibleEntries.length, total: entries.length }) : t('files.entryCount', { count: entries.length })}</Text>
        <Input.Search placeholder={t('files.filter')} value={filter} allowClear onChange={(event) => setFilter(event.target.value)} aria-label={t('files.filter')} />
      </div>
    </div>}
    {listing?.truncated && <Alert type="warning" showIcon title={t('files.truncated')} />}
    <div className={`file-dropzone${dragging ? ' is-dragging' : ''}`} onDragEnter={onDragEnter} onDragOver={onDragOver} onDragLeave={onDragLeave} onDrop={onDrop}>
      <Table rowKey="path" columns={columns} dataSource={visibleEntries} loading={loading} size="small" scroll={{ x: 880 }} pagination={false} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={error ? t('files.loadFailed') : t('files.empty')} /> }} />
      {dragging && <div className="file-drop-overlay" aria-hidden="true"><CloudUploadOutlined /><span>{t('files.dropHint')}</span></div>}
    </div>
    <div className="file-footer">
      <Text type="secondary" className="file-hints">
        <InfoCircleOutlined />
        <span className="file-hints-text">
          {t('files.limits')}
          <Tooltip title={containerId ? t('files.containerHint') : t('files.hostHint')}><span className="col-hint">{t('files.hintsMore')}</span></Tooltip>
        </span>
      </Text>
      <Space className="file-pagination">
        <Button disabled={locked || loading || !previousCursors.length} onClick={() => { if (listing) void loadDirectory(listing.path, previousCursors[previousCursors.length - 1], previousCursors.slice(0, -1)); }}>{t('files.previousPage')}</Button>
        <Text>{t('files.pageNumber', { count: previousCursors.length + 1 })}</Text>
        <Button disabled={locked || loading || !listing?.next_cursor} onClick={() => { if (listing) void loadDirectory(listing.path, listing.next_cursor, [...previousCursors, cursor]); }}>{t('files.nextPage')}</Button>
      </Space>
    </div>
    <Modal open={!!change} title={change?.action === 'rename' ? t('files.rename') : change?.kind === 'directory' ? t('files.newDirectory') : t('files.newFile')} confirmLoading={busy} onOk={() => { void applyChange(); }} onCancel={() => { if (!busy) setChange(null); }}>
      <Input aria-label={t('common.name')} value={change?.name || ''} disabled={busy} onChange={event => setChange(current => current ? { ...current, name: event.target.value } : current)} onPressEnter={() => { void applyChange(); }} />
    </Modal>
    <Modal open={history !== null} title={t('files.history')} width="min(1100px,95vw)" onCancel={() => { if (!busy) setHistory(null); }} footer={null}>
      <Text type="secondary">{t('files.historyHint')}</Text>
      <Table rowKey="id" className="file-history" dataSource={history || []} pagination={{ pageSize: 10 }} scroll={{ x: 800 }} size="small" columns={[
        { title: t('files.modified'), dataIndex: 'created_at', width: 170, render: (value: string) => formatDate(value, i18n.language, { hour: '2-digit', minute: '2-digit' }) },
        { title: t('common.actions'), dataIndex: 'action', width: 130, render: (value: string) => t('files.historyAction.' + auditActionCode(value), { defaultValue: value }) },
        { title: t('files.path'), dataIndex: 'path', render: (value: string) => <Text className="mono-cell">{value}</Text> },
        { title: t('files.result'), key: 'outcome', width: 220, render: (_, record: FileAudit) => (
          <Space size={6} wrap>
            <Tag color={OUTCOME_COLOR[record.outcome] || 'default'}>{t('files.outcome.' + record.outcome, { defaultValue: record.outcome })}</Tag>
            {record.error_code && <Text type="secondary">{t('files.error.' + record.error_code, { defaultValue: record.error_code })}</Text>}
          </Space>
        ) },
        { title: t('files.backup'), dataIndex: 'backup_path', width: 120, render: (value: string) => value && <Button size="small" icon={<DownloadOutlined />} disabled={busy} onClick={() => { void download({ name: value.split('/').pop() || 'backup', path: value, size: 0, mode: '', modified_at: '', is_dir: false, is_file: true, is_symlink: false }); }}>{t('files.download')}</Button> },
      ]} />
    </Modal>
    <Modal destroyOnHidden open={!!editor} title={<Space><FileOutlined /><span className="file-editor-name">{editor?.entry.path}</span>{dirty && <Tag color="orange">{t('files.unsaved')}</Tag>}</Space>} width="min(1100px, 95vw)" onCancel={closeEditor} mask={{ closable: !dirty && !busy }} keyboard={!busy} footer={<Space><Button disabled={busy} onClick={closeEditor}>{t('files.close')}</Button><Button type="primary" icon={<SaveOutlined />} loading={busy} disabled={!dirty || editor?.entry.is_symlink} onClick={() => { void saveText(); }}>{t('common.save')}</Button></Space>}>
      {editor?.entry.is_symlink && <Alert type="warning" showIcon title={t('files.symlinkReadOnly')} />}
      {editor && <Suspense fallback={<div className="file-editor-loading"><Spin /><Text type="secondary">{t('files.editorLoading')}</Text></div>}>
        <FileCodeEditor key={editor.entry.path} path={editor.entry.path} value={editor.content} readOnly={busy || editor.entry.is_symlink} onChange={(content) => setEditor((current) => current ? { ...current, content } : current)} onSave={() => { void saveText(); }} />
      </Suspense>}
      {editor && editorStats && (
        <Text type="secondary" className="file-editor-status" title={t('files.textLimit')}>
          <span>{t('files.lines', { count: editorStats.lines })}</span>
          <span>{formatBytes(editorStats.bytes)} / 2 MiB</span>
          <span>UTF-8</span>
          <span>{editor.crlf ? 'CRLF' : 'LF'}</span>
          <span>Ctrl/Cmd+S</span>
        </Text>
      )}
    </Modal>
  </Card>;
}
