import AsyncStorage from "@react-native-async-storage/async-storage";
import { activateKeepAwakeAsync, deactivateKeepAwake } from "expo-keep-awake";
import { create } from "zustand";

const UNIT_KEY = "jackery.tempUnit";
const AWAKE_KEY = "jackery.keepAwake";
const BUCKET_KEY = "jackery.energyBucketS";

const ENERGY_BUCKETS = new Set([900, 1800, 3600]);
export type EnergyBucketS = 900 | 1800 | 3600;

function parseEnergyBucketS(raw: string | null): EnergyBucketS {
  const n = Number(raw);
  return ENERGY_BUCKETS.has(n) ? (n as EnergyBucketS) : 900;
}

type PrefsState = {
  hydrated: boolean;
  tempUnit: "C" | "F";
  keepAwake: boolean;
  energyBucketS: EnergyBucketS;
  hydrate: () => Promise<void>;
  setTempUnit: (u: "C" | "F") => Promise<void>;
  setKeepAwake: (on: boolean) => Promise<void>;
  setEnergyBucketS: (s: EnergyBucketS) => Promise<void>;
};

export const usePrefs = create<PrefsState>((set) => ({
  hydrated: false,
  tempUnit: "C",
  keepAwake: false,
  energyBucketS: 900,
  hydrate: async () => {
    const [unit, awake, bucket] = await Promise.all([
      AsyncStorage.getItem(UNIT_KEY),
      AsyncStorage.getItem(AWAKE_KEY),
      AsyncStorage.getItem(BUCKET_KEY),
    ]);
    const keepAwake = awake === "1";
    set({
      hydrated: true,
      tempUnit: unit === "F" ? "F" : "C",
      keepAwake,
      energyBucketS: parseEnergyBucketS(bucket),
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
  setEnergyBucketS: async (s) => {
    await AsyncStorage.setItem(BUCKET_KEY, String(s));
    set({ energyBucketS: s });
  },
}));
