import client from './client';

export interface LoginResponse {
  token: string;
  user: { id: string; username: string };
  last_login?: { ip: string; logged_at: string };
}

export interface LoginHistoryItem {
  id: number;
  user_id: string;
  ip: string;
  user_agent: string;
  success: boolean;
  logged_at: string;
}

export interface ChangePasswordResponse {
  message: string;
  /** Replacement token that keeps this session alive after the rotation. */
  token?: string;
  /** Set when the server could not mint one and the user must sign in again. */
  reauth_required?: boolean;
}

export const authApi = {
  register: (username: string, password: string) =>
    client.post('/auth/register', { username, password }),

  login: (username: string, password: string) =>
    client.post<LoginResponse>('/auth/login', { username, password }),

  me: () => client.get('/auth/me'),

  // `failed` filters server-side: paging through successes to find the one
  // rejected attempt defeats the point of the log. `total` counts the filtered
  // view for pagination; `failed` is always the unfiltered all-time count.
  getLoginHistory: (limit = 20, offset = 0, failedOnly = false) =>
    client.get<{ records: LoginHistoryItem[]; total: number; failed: number }>(
      '/auth/login-history',
      { params: { limit, offset, ...(failedOnly ? { failed: 1 } : {}) } },
    ),

  // skipAuthRedirect: a rejected current password must surface as a form error,
  // never as a forced sign-out.
  changePassword: (current_password: string, new_password: string) =>
    client.post<ChangePasswordResponse>('/auth/password', { current_password, new_password },
      { skipAuthRedirect: true }),
};
