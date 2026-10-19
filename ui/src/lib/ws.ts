import type { StatusSnapshot } from './types';

// Re-export for callers that need the snapshot type alongside the socket helper.
export type { StatusSnapshot };

export type WsMessage =
  | { type: 'snapshot' | 'telemetry' | 'status'; data: StatusSnapshot }
  | { type: 'alert'; data?: { message?: string } }
  | { type: 'kasa_updated'; data?: unknown }
  | { type: 'automation_fired'; data?: { id?: string; name?: string; action?: string; kasa_alias?: string; last_fired?: number } }
  | { type: string; data?: unknown };

export type WsHandlers = {
  onStatus?: (data: StatusSnapshot) => void;
  onAlert?: (message: string) => void;
  onKasaUpdated?: () => void;
  onAutomationFired?: (data: NonNullable<Extract<WsMessage, { type: 'automation_fired' }>['data']>) => void;
};

/**
 * Live WebSocket with 2s reconnect, matching the legacy connectWs behavior.
 */
export function connectWs(handlers: WsHandlers): () => void {
  let stopped = false;
  let socket: WebSocket | undefined;
  let retry: ReturnType<typeof setTimeout> | undefined;

  function connect() {
    if (stopped) return;
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${proto}//${location.host}/ws`);
    socket = ws;
    ws.onmessage = (ev) => {
      let msg: WsMessage;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (msg.type === 'snapshot' || msg.type === 'telemetry' || msg.type === 'status') {
        handlers.onStatus?.(msg.data as StatusSnapshot);
      } else if (msg.type === 'alert') {
        const alertData = msg.data as { message?: string } | undefined;
        handlers.onAlert?.(alertData?.message || 'Alert');
      } else if (msg.type === 'kasa_updated') {
        handlers.onKasaUpdated?.();
      } else if (msg.type === 'automation_fired') {
        handlers.onAutomationFired?.(
          (msg.data as NonNullable<Extract<WsMessage, { type: 'automation_fired' }>['data']>) || {},
        );
      }
    };
    ws.onclose = () => {
      if (stopped) return;
      retry = setTimeout(connect, 2000);
    };
    ws.onerror = () => {
      try {
        ws.close();
      } catch {
        /* ignore */
      }
    };
  }

  connect();

  return () => {
    stopped = true;
    if (retry) clearTimeout(retry);
    try {
      socket?.close();
    } catch {
      /* ignore */
    }
  };
}
