import client from './client';

export interface LoginResponse {
  token: string;
  user: { id: string; username: string };
}

export const authApi = {
  register: (username: string, password: string) =>
    client.post('/auth/register', { username, password }),

  login: (username: string, password: string) =>
    client.post<LoginResponse>('/auth/login', { username, password }),

  me: () => client.get('/auth/me'),
};
