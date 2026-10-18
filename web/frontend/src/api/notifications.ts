import client from './client';

export type NotificationType = 'telegram' | 'wecom' | 'dingtalk' | 'email' | 'webhook';
export interface NotificationConfig {
  bot_token?: string; chat_id?: string; message_thread_id?: number;
  webhook_url?: string; secret?: string; bearer_token?: string;
  smtp_host?: string; smtp_port?: number; tls_mode?: 'tls' | 'starttls';
  username?: string; password?: string; from?: string; to?: string[];
}
export interface NotificationChannel {
  id: string; name: string; type: NotificationType; enabled: boolean;
  config: NotificationConfig; secret_fields: string[]; created_at: string; updated_at: string;
}
export interface NotificationChannelInput {
  name?: string; type?: NotificationType; enabled?: boolean;
  config?: NotificationConfig; clear_secret_fields?: string[];
}
export type DeliveryStatus = 'pending' | 'sending' | 'retry' | 'succeeded' | 'failed' | 'canceled';
export interface NotificationDelivery {
  id: number; channel_id: string | null; channel_name: string; channel_type: NotificationType;
  transition: string; status: DeliveryStatus; attempts: number; created_at: string;
  subject?: string; message?: string;
  last_attempt_at: string | null; next_attempt_at: string | null; delivered_at: string | null;
  last_error: string; retry_of?: number | null; event_id?: number;
}
export interface DeliveryPage { deliveries: NotificationDelivery[]; next_cursor?: string }
export const notificationsApi = {
  channels: (signal?: AbortSignal) => client.get<NotificationChannel[]>('/notifications/channels', { signal }),
  create: (data: NotificationChannelInput) => client.post<NotificationChannel>('/notifications/channels', data),
  update: (id: string, data: NotificationChannelInput) => client.put<NotificationChannel>(`/notifications/channels/${id}`, data),
  remove: (id: string) => client.delete(`/notifications/channels/${id}`),
  test: (id: string) => client.post<{ delivery_id: number }>(`/notifications/channels/${id}/test`),
  deliveries: (params: { status?: DeliveryStatus; channel_id?: string; cursor?: string }, signal?: AbortSignal) =>
    client.get<DeliveryPage>('/notifications/deliveries', { params: { ...params, limit: 50 }, signal }),
  retry: (id: number) => client.post<{ delivery_id: number }>(`/notifications/deliveries/${id}/retry`, { confirm_resend: true }),
};
