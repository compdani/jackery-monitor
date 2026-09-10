import * as SecureStore from "expo-secure-store";
import { create } from "zustand";

const TOKEN_KEY = "jackery.sessionToken";
const USER_KEY = "jackery.username";

function asStoreString(value: unknown, label: string): string {
  if (typeof value === "string" && value.length > 0) return value;
  throw new Error(
    `Cannot save ${label}: expected a string, got ${value == null ? "missing" : typeof value}`,
  );
}

type SessionState = {
  hydrated: boolean;
  token: string | null;
  username: string | null;
  hydrate: () => Promise<void>;
  setSession: (token: string, username: string) => Promise<void>;
  clear: () => Promise<void>;
};

export const useSession = create<SessionState>((set) => ({
  hydrated: false,
  token: null,
  username: null,
  hydrate: async () => {
    const [token, username] = await Promise.all([
      SecureStore.getItemAsync(TOKEN_KEY),
      SecureStore.getItemAsync(USER_KEY),
    ]);
    set({ hydrated: true, token: token || null, username: username || null });
  },
  setSession: async (token, username) => {
    const t = asStoreString(token, "session token");
    const u = asStoreString(username, "username");
    await SecureStore.setItemAsync(TOKEN_KEY, t);
    await SecureStore.setItemAsync(USER_KEY, u);
    set({ token: t, username: u });
  },
  clear: async () => {
    await SecureStore.deleteItemAsync(TOKEN_KEY);
    await SecureStore.deleteItemAsync(USER_KEY);
    set({ token: null, username: null });
  },
}));
