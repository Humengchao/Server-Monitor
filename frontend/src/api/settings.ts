import client from './client';

export interface UserSettings {
  /** Empty when the user has never set one. */
  default_webhook_url: string;
  updated_at: string;
}

export const settingsApi = {
  get: () => client.get<UserSettings>('/settings'),
  // Sending an empty string clears the default; alert rules then notify only
  // through whatever webhook they carry themselves.
  update: (default_webhook_url: string) =>
    client.put<UserSettings>('/settings', { default_webhook_url }),
};
