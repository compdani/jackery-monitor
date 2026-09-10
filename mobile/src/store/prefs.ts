import AsyncStorage from "@react-native-async-storage/async-storage";
import { activateKeepAwakeAsync, deactivateKeepAwake } from "expo-keep-awake";
import { create } from "zustand";

const UNIT_KEY = "jackery.tempUnit";
const AWAKE_KEY = "jackery.keepAwake";

type PrefsState = {
  hydrated: boolean;
  tempUnit: "C" | "F";
  keepAwake: boolean;
  hydrate: () => Promise<void>;
  setTempUnit: (u: "C" | "F") => Promise<void>;
  setKeepAwake: (on: boolean) => Promise<void>;
};

export const usePrefs = create<PrefsState>((set) => ({
  hydrated: false,
  tempUnit: "C",
  keepAwake: false,
  hydrate: async () => {
    const [unit, awake] = await Promise.all([
      AsyncStorage.getItem(UNIT_KEY),
      AsyncStorage.getItem(AWAKE_KEY),
    ]);
    const keepAwake = awake === "1";
    set({
      hydrated: true,
      tempUnit: unit === "F" ? "F" : "C",
      keepAwake,
    });
    if (keepAwake) await activateKeepAwakeAsync();
  },
  setTempUnit: async (u) => {
    await AsyncStorage.setItem(UNIT_KEY, u);
    set({ tempUnit: u });
  },
  setKeepAwake: async (on) => {
    await AsyncStorage.setItem(AWAKE_KEY, on ? "1" : "0");
    set({ keepAwake: on });
    if (on) await activateKeepAwakeAsync();
    else await deactivateKeepAwake();
  },
}));
