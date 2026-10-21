import type { SSHAuthInput, SSHAuthMethod } from './servers';
import client from './client';

export interface Credential {
  id: string;
  user_id: string;
  name: string;
  ssh_username: string;
  has_password: boolean;
  ssh_auth_method?: SSHAuthMethod;
  has_key: boolean;
  credential_type: string;
  created_at: string;
}

export interface CredentialInput extends SSHAuthInput {
  name: string;
  ssh_username: string;
  credential_type?: string;
}

export const credentialsApi = {
  list: (signal?: AbortSignal) => client.get<Credential[]>('/credentials', { signal }),

  create: (data: CredentialInput) => client.post<Credential>('/credentials', data),

  update: (id: string, data: CredentialInput) => client.put(`/credentials/${id}`, data),

  delete: (id: string) => client.delete(`/credentials/${id}`),
};
