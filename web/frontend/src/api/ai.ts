import client from './client';

export type AIProviderKind = 'deepseek' | 'compatible';
export interface AIProvider {
  id: string; name: string; kind: AIProviderKind; base_url: string; model: string;
  has_api_key: boolean; enabled: boolean; created_at: string; updated_at: string;
}
export interface AIProviderInput { name: string; kind: AIProviderKind; base_url: string; model: string; api_key: string; enabled: boolean }
export type AITaskStatus = 'queued' | 'running' | 'awaiting_approval' | 'awaiting_input' | 'completed' | 'failed' | 'cancel_requested' | 'canceled' | 'interrupted';
export interface AITask {
  id: string; server_id: string; server_name: string; server_type: string; provider_id: string; provider_name: string; model: string;
  prompt: string; status: AITaskStatus; error: string; created_at: string; updated_at: string; prompt_tokens: number; completion_tokens: number;
}
export interface AIEvent { id: number; kind: 'user' | 'assistant' | 'system' | 'tool'; content: string; created_at: string; action_id?: string }
export type AIActionStatus = 'pending' | 'approved' | 'running' | 'succeeded' | 'failed' | 'rejected' | 'interrupted';
export interface AIAction {
  id: string; tool_name: string; arguments: Record<string, unknown>; summary: string; command: string; requires_approval: boolean; risk_reason: string;
  status: AIActionStatus; output: string; error: string; truncated: boolean; exit_code?: number; unknown: boolean; duration_ms: number; created_at: string; completed_at?: string;
}
export interface AITaskDetail { task: AITask; events: AIEvent[]; actions: AIAction[] }
export interface AITaskInput { server_id: string; provider_id: string; prompt: string; request_id: string }

export const aiApi = {
  providers: (signal?: AbortSignal) => client.get<AIProvider[]>('/ai/providers', { signal }),
  createProvider: (value: AIProviderInput, signal?: AbortSignal) => client.post<AIProvider>('/ai/providers', value, { signal, timeout: 20000 }),
  updateProvider: (id: string, value: AIProviderInput, signal?: AbortSignal) => client.put<AIProvider>(`/ai/providers/${encodeURIComponent(id)}`, value, { signal, timeout: 20000 }),
  deleteProvider: (id: string, signal?: AbortSignal) => client.delete(`/ai/providers/${encodeURIComponent(id)}`, { signal }),
  testProvider: (id: string, signal?: AbortSignal) => client.post<{ ok: boolean; message?: string; model?: string }>(`/ai/providers/${encodeURIComponent(id)}/test`, undefined, { signal, timeout: 100000 }),
  tasks: (before = '', signal?: AbortSignal) => client.get<{ tasks: AITask[]; next_cursor?: string }>('/ai/tasks', { params: { limit: 30, before: before || undefined }, signal }),
  task: (id: string, signal?: AbortSignal) => client.get<AITaskDetail>(`/ai/tasks/${encodeURIComponent(id)}`, { signal }),
  createTask: (value: AITaskInput, signal?: AbortSignal) => client.post<AITask>('/ai/tasks', value, { signal, timeout: 30000 }),
  message: (id: string, value: { message: string; request_id: string }, signal?: AbortSignal) => client.post<AITask>(`/ai/tasks/${encodeURIComponent(id)}/messages`, value, { signal, timeout: 30000 }),
  cancel: (id: string, signal?: AbortSignal) => client.post<AITask>(`/ai/tasks/${encodeURIComponent(id)}/cancel`, undefined, { signal }),
  decide: (id: string, action: string, decision: 'approve' | 'reject', signal?: AbortSignal) => client.post<AITask>(`/ai/tasks/${encodeURIComponent(id)}/actions/${encodeURIComponent(action)}/decision`, { decision }, { signal }),
};
