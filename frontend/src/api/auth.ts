import client from './client';

export interface LoginResponse {
  token: string;
  user: { id: string; username: string };
  last_login?: { ip: string; logged_at: string };
}

export interface LoginHistoryItem {
  id: string;
  user_id: string;
  ip: string;
  user_agent: string;
  success: boolean;
  logged_at: string;
}

export const authApi = {
  register: (username: string, password: string) =>
    client.post('/auth/register', { username, password }),

  login: (username: string, password: string) =>
    client.post<LoginResponse>('/auth/login', { username, password }),

  me: () => client.get('/auth/me'),

  getLoginHistory: (limit = 20, offset = 0) =>
    client.get<{ records: LoginHistoryItem[]; total: number }>('/auth/login-history', { params: { limit, offset } }),
};
