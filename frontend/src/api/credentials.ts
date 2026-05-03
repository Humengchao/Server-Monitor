import client from './client';

export interface Credential {
  id: string;
  user_id: string;
  name: string;
  ssh_username: string;
  created_at: string;
}

export const credentialsApi = {
  list: () => client.get<Credential[]>('/credentials'),

  create: (data: {
    name: string;
    ssh_username: string;
    ssh_password?: string;
    ssh_key?: string;
  }) => client.post<Credential>('/credentials', data),

  update: (id: string, data: {
    name: string;
    ssh_username: string;
    ssh_password?: string;
    ssh_key?: string;
  }) => client.put(`/credentials/${id}`, data),

  delete: (id: string) => client.delete(`/credentials/${id}`),
};
