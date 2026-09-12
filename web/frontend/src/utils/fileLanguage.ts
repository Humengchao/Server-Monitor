import { LanguageDescription } from '@codemirror/language';
import { languages } from '@codemirror/language-data';

export const fileLanguages = [...languages].sort((first, second) => first.name.localeCompare(second.name));

export function detectFileLanguage(path: string): LanguageDescription | null {
  const normalized = path.replaceAll('\\', '/');
  const filename = normalized.split('/').pop() || '';
  const lower = filename.toLowerCase();
  const find = (name: string) => LanguageDescription.matchLanguageName(languages, name, false);
  const matched = LanguageDescription.matchFilename(languages, filename) || LanguageDescription.matchFilename(languages, lower);

  if (/^(dockerfile|containerfile)(\..+)?$/i.test(filename) || /\.(dockerfile|containerfile)$/i.test(filename)) return find('Dockerfile');
  if (/^\.env(?:\..*)?$/i.test(filename) || /^\.(?:bashrc|bash_profile|bash_login|bash_logout|zshrc|zprofile|profile|shrc|ashrc|kshrc)$/.test(lower)) return find('Shell');
  if (/^nginx\.conf(?:\..*)?$/.test(lower) || (/\/nginx\//i.test(normalized) && /\.conf(?:\.template)?$/.test(lower)) || (!matched && /\/nginx\/(?:conf\.d|sites-available|sites-enabled)\//i.test(normalized))) return find('Nginx');
  if (/\.(?:service|socket|timer|target|mount|automount|path|slice|swap|network|netdev|link)$/.test(lower) || ['.gitconfig', '.gitmodules'].includes(lower)) return find('Properties files');
  if (['.babelrc', '.eslintrc', '.prettierrc'].includes(lower)) return find('JSON');
  if (/\.cfg$/.test(lower)) return null;
  return matched;
}
