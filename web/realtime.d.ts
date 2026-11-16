export type RealtimeOptions = {
  baseUrl?: string;
  getToken?: () => string | null;
  getDeviceSn: () => string | undefined | null;
  sourceFactory?: (url: string) => any;
  onEvent?: (data: Record<string, unknown>) => void;
  onRefresh: (kind?: string) => void;
  onConnected?: (connected: boolean) => void;
  onError?: (message: string) => void;
};
export function connectMonitorRealtime(options: RealtimeOptions): { reconnect: () => void; stop: () => void };
