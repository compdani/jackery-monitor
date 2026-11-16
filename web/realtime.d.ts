export type RealtimeOptions = {
  baseUrl?: string;
  getToken?: () => string | null;
  getDeviceSn: () => string | undefined | null;
  sourceFactory?: (url: string) => any;
  getViewId?: () => string | undefined | null;
  onSnapshot?: (patch: Record<string, unknown>) => void;
  onQueries?: () => void;
  onEvent?: (data: Record<string, unknown>) => void;
  onRefresh: (kind?: string) => void;
  onConnected?: (connected: boolean) => void;
  onError?: (message: string) => void;
};
export function connectMonitorRealtime(options: RealtimeOptions): { reconnect: () => void; stop: () => void };

export function realtimeConnected(): boolean;
export function readRealtimeQuery(path: string): unknown;
export function fetchRealtimeQuery(path: string): Promise<Response>;

export function resetRealtimeQueries(): void;
