import AsyncStorage from "@react-native-async-storage/async-storage";
import { create } from "zustand";
import { normalizeBaseUrl } from "../lib/url";

const KEY = "jackery.baseUrl";

type ConnectionState = {
  hydrated: boolean;
  baseUrl: string | null;
  hydrate: () => Promise<void>;
  setBaseUrl: (raw: string) => Promise<string>;
  clearBaseUrl: () => Promise<void>;
};

export const useConnection = create<ConnectionState>((set) => ({
  hydrated: false,
  baseUrl: null,
  hydrate: async () => {
    const v = await AsyncStorage.getItem(KEY);
    set({ hydrated: true, baseUrl: v || null });
  },
  setBaseUrl: async (raw) => {
    const url = normalizeBaseUrl(raw);
    await AsyncStorage.setItem(KEY, url);
    set({ baseUrl: url });
    return url;
  },
  clearBaseUrl: async () => {
    await AsyncStorage.removeItem(KEY);
    set({ baseUrl: null });
  },
}));
