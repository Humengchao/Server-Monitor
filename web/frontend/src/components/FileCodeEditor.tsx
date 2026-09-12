import { useContext, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Select, Space, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import { basicSetup } from 'codemirror';
import { Annotation, Compartment, EditorState, Prec, Transaction } from '@codemirror/state';
import { EditorView, keymap } from '@codemirror/view';
import { oneDark } from '@codemirror/theme-one-dark';
import { DarkModeContext } from '../contexts/DarkModeContext';
import { detectFileLanguage, fileLanguages } from '../utils/fileLanguage';

interface Props {
  path: string;
  value: string;
  readOnly: boolean;
  onChange: (value: string) => void;
  onSave: () => void;
}

const externalChange = Annotation.define<boolean>();

export default function FileCodeEditor({ path, value, readOnly, onChange, onSave }: Props) {
  const { t } = useTranslation();
  const darkMode = useContext(DarkModeContext);
  const parent = useRef<HTMLDivElement>(null);
  const editor = useRef<EditorView | null>(null);
  const callbacks = useRef({ onChange, onSave });
  const [mode, setMode] = useState('auto');
  const [loaded, setLoaded] = useState<{ name: string; failed: boolean } | null>(null);
  const [compartments] = useState(() => ({ language: new Compartment(), theme: new Compartment(), editing: new Compartment() }));
  const detected = useMemo(() => detectFileLanguage(path), [path]);
  const language = mode === 'auto' ? detected : fileLanguages.find((item) => item.name === mode) || null;
  const label = t('files.editorLabel', { path });
  const languageFailed = !!language && loaded?.name === language.name && loaded.failed;

  useEffect(() => { callbacks.current = { onChange, onSave }; }, [onChange, onSave]);

  useEffect(() => {
    if (!parent.current) return;
    const view = new EditorView({
      parent: parent.current,
      state: EditorState.create({
        extensions: [
          basicSetup,
          EditorState.lineSeparator.of('\n'),
          EditorView.lineWrapping,
          compartments.language.of([]),
          compartments.theme.of([]),
          compartments.editing.of([]),
          Prec.highest(keymap.of([{ key: 'Mod-s', preventDefault: true, run: () => { callbacks.current.onSave(); return true; } }])),
          EditorView.updateListener.of((update) => {
            if (update.docChanged && !update.transactions.some((transaction) => transaction.annotation(externalChange))) {
              callbacks.current.onChange(update.state.doc.toString());
            }
          }),
        ],
      }),
    });
    editor.current = view;
    return () => { editor.current = null; view.destroy(); };
  }, [compartments]);

  useEffect(() => {
    const view = editor.current;
    if (view && value !== view.state.doc.toString()) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value }, annotations: [externalChange.of(true), Transaction.addToHistory.of(false)] });
    }
  }, [value]);

  useEffect(() => {
    editor.current?.dispatch({ effects: compartments.editing.reconfigure([
      EditorState.readOnly.of(readOnly),
      EditorView.editable.of(!readOnly),
      EditorView.contentAttributes.of({ 'aria-label': label, 'aria-readonly': String(readOnly), 'aria-multiline': 'true', role: 'textbox', tabindex: '0', spellcheck: 'false', autocorrect: 'off', autocapitalize: 'off', translate: 'no' }),
    ]) });
  }, [readOnly, label, compartments]);

  useEffect(() => {
    editor.current?.dispatch({ effects: compartments.theme.reconfigure(darkMode ? oneDark : []) });
  }, [darkMode, compartments]);

  useEffect(() => {
    const view = editor.current;
    if (!view) return;
    let cancelled = false;
    view.dispatch({ effects: compartments.language.reconfigure([]) });
    if (language) {
      void language.load().then((support) => {
        if (cancelled) return;
        view.dispatch({ effects: compartments.language.reconfigure(support) });
        setLoaded({ name: language.name, failed: false });
      }).catch(() => {
        if (!cancelled) setLoaded({ name: language.name, failed: true });
      });
    }
    return () => { cancelled = true; };
  }, [language, compartments]);

  return <div className="file-code-editor" data-theme={darkMode ? 'dark' : 'light'}>
    <Space className="file-editor-toolbar" wrap>
      <Typography.Text type="secondary">{t('files.language')}</Typography.Text>
      <Select size="small" className="file-editor-language" aria-label={t('files.language')} showSearch optionFilterProp="label" value={mode} onChange={setMode} loading={!!language && loaded?.name !== language.name} options={[
        { value: 'auto', label: t('files.languageAuto', { language: detected?.name || t('files.plainText') }) },
        { value: 'plain', label: t('files.plainText') },
        ...fileLanguages.map((item) => ({ value: item.name, label: item.name })),
      ]} />
    </Space>
    {languageFailed && <Alert type="warning" showIcon title={t('files.highlightFailed')} />}
    <div className="file-editor-surface" ref={parent} />
  </div>;
}
