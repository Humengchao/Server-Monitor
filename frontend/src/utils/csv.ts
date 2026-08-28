// Excel only detects UTF-8 in a CSV when the file starts with a byte-order
// mark, so every export we produce is prefixed with one.
const UTF8_BOM = '\uFEFF';

/**
 * Escapes one CSV field. Our values are numbers, ISO timestamps and host names,
 * but quoting is still required: an unquoted comma or newline would shift every
 * following column.
 */
export function csvField(value: string | number | null | undefined): string {
  const text = String(value ?? '');
  return /[",\n\r]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/** Joins rows, prefixes the BOM, and triggers a browser download. */
export function downloadCSV(filename: string, rows: string[][]): void {
  const body = rows.map((row) => row.map(csvField).join(',')).join('\n');
  const blob = new Blob([UTF8_BOM + body], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

/** Strips characters that would be awkward or unsafe in a download filename. */
export function safeFilenamePart(text: string): string {
  return text.replace(/[^\w.-]+/g, '_').replace(/^_+|_+$/g, '') || 'export';
}
