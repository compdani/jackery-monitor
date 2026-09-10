import { create } from "zustand";
import type { StatusPayload } from "../api/types";

type Alert = { level?: string; message: string; at: number };

type LiveState = {
  status: StatusPayload | null;
  connected: boolean;
  lastError: string | null;
  alerts: Alert[];
  viewDeviceId: string | null;
  applyStatus: (s: StatusPayload) => void;
  setConnected: (v: boolean) => void;
  setError: (e: string | null) => void;
  pushAlert: (a: { level?: string; message: string }) => void;
  dismissAlert: () => void;
  setViewDeviceId: (id: string | null) => void;
};

export const useLive = create<LiveState>((set) => ({
  status: null,
  connected: false,
  lastError: null,
  alerts: [],
  viewDeviceId: null,
  applyStatus: (s) =>
    set({
      status: s,
      viewDeviceId: (s.cloud?.selected_device_id as string) || null,
    }),
  setConnected: (v) => set({ connected: v }),
  setError: (e) => set({ lastError: e }),
  pushAlert: (a) =>
    set((st) => ({
      alerts: [{ ...a, message: a.message, at: Date.now() }, ...st.alerts].slice(0, 8),
    })),
  dismissAlert: () => set((st) => ({ alerts: st.alerts.slice(1) })),
  setViewDeviceId: (id) => set({ viewDeviceId: id }),
}));
