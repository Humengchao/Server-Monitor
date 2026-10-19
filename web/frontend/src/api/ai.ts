import client from './client';

export type AIProviderKind = 'deepseek' | 'openai' | 'qwen' | 'moonshot' | 'zhipu' | 'siliconflow' | 'gemini' | 'compatible' | 'cpa';
export type AIApprovalMode = 'readonly' | 'standard' | 'automatic';
export interface AIModel { id: string; owned_by?: string }
export interface AIModelCatalog { models: AIModel[]; fetched_at: string; source: 'provider' }
export interface AISkill { id: string; name: string; description: string; content: string; created_at: string; updated_at: string }
export type AISkillInput = Pick<AISkill, 'name' | 'description' | 'content'>;
export interface AIPlugin { id: string; name: string; url: string; enabled: boolean; has_bearer_token: boolean; created_at: string; updated_at: string }
export interface AIPluginInput { name: string; url: string; enabled: boolean; bearer_token: string; clear_token?: boolean }
export interface AIProvider {
  id: string; name: string; kind: AIProviderKind; base_url: string; model: string;
  has_api_key: boolean; enabled: boolean; created_at: string; updated_at: string;
}
export interface AIProviderInput { name: string; kind: AIProviderKind; base_url: string; model: string; api_key: string; enabled: boolean }
export type AITaskStatus = 'queued' | 'running' | 'awaiting_approval' | 'awaiting_input' | 'completed' | 'failed' | 'cancel_requested' | 'canceled' | 'interrupted';
export interface AITask {
  id: string; server_id: string; server_name: string; server_type: string; provider_id: string; provider_name: string; model: string;
  prompt: string; status: AITaskStatus; error: string; created_at: string; updated_at: string; prompt_tokens: number; completion_tokens: number;
  approval_mode?: AIApprovalMode; skills?: Array<{ id: string; name: string }>; plugins?: Array<{ id: string; name: string }>;
}
export interface AIEvent { id: number; kind: 'user' | 'assistant' | 'system' | 'tool'; content: string; created_at: string; action_id?: string }
export type AIActionStatus = 'pending' | 'approved' | 'running' | 'succeeded' | 'failed' | 'rejected' | 'interrupted';
export interface AIAction {
  id: string; tool_name: string; arguments: Record<string, unknown>; summary: string; command: string; requires_approval: boolean; risk_reason: string;
  status: AIActionStatus; output: string; error: string; truncated: boolean; exit_code?: number; unknown: boolean; duration_ms: number; created_at: string; completed_at?: string;
}
export interface AITaskDetail { task: AITask; events: AIEvent[]; actions: AIAction[] }
export interface AITaskInput { server_id: string; provider_id: string; prompt: string; request_id: string; approval_mode: AIApprovalMode; skill_ids: string[]; plugin_ids: string[] }

export const aiApi = {
  providers: (signal?: AbortSignal) => client.get<AIProvider[]>('/ai/providers', { signal }),
  models: (id: string, signal?: AbortSignal) => client.get<AIModelCatalog>(`/ai/providers/${encodeURIComponent(id)}/models`, { signal, timeout: 100000 }),
  previewModels: (value: Pick<AIProviderInput, 'kind' | 'base_url' | 'api_key'>, signal?: AbortSignal) => client.post<AIModelCatalog>('/ai/providers/models', value, { signal, timeout: 100000 }),
  skills: (signal?: AbortSignal) => client.get<AISkill[]>('/ai/skills', { signal }),
  createSkill: (value: AISkillInput, signal?: AbortSignal) => client.post<AISkill>('/ai/skills', value, { signal }),
  updateSkill: (id: string, value: AISkillInput, signal?: AbortSignal) => client.put<AISkill>(`/ai/skills/${encodeURIComponent(id)}`, value, { signal }),
  deleteSkill: (id: string, signal?: AbortSignal) => client.delete(`/ai/skills/${encodeURIComponent(id)}`, { signal }),
  plugins: (signal?: AbortSignal) => client.get<AIPlugin[]>('/ai/plugins', { signal }),
  createPlugin: (value: AIPluginInput, signal?: AbortSignal) => client.post<AIPlugin>('/ai/plugins', value, { signal }),
  updatePlugin: (id: string, value: AIPluginInput, signal?: AbortSignal) => client.put<AIPlugin>(`/ai/plugins/${encodeURIComponent(id)}`, value, { signal }),
  deletePlugin: (id: string, signal?: AbortSignal) => client.delete(`/ai/plugins/${encodeURIComponent(id)}`, { signal }),
  testPlugin: (id: string, signal?: AbortSignal) => client.post<{ ok: boolean; tool_count?: number }>(`/ai/plugins/${encodeURIComponent(id)}/test`, undefined, { signal, timeout: 100000 }),
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
