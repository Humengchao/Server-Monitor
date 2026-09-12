import { create } from 'zustand';

export interface FileDraft { name: string; path: string; content: string }
export const useFileSessionStore = create<{ locked: boolean; draft: FileDraft | null }>(() => ({ locked: false, draft: null }));

export function downloadDraft(draft: FileDraft) {
  const url = URL.createObjectURL(new Blob([draft.content], { type: 'text/plain;charset=utf-8' }));
  const link = document.createElement('a');
  link.href = url;
  link.download = draft.name;
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 5000);
}
