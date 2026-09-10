import { Slot } from "expo-router";
import { useEffect, useState } from "react";
import { endpoints } from "../../src/api/client";
import { startLive, stopLive } from "../../src/api/ws";
import { JackeryLoginModal } from "../../src/components/JackeryLoginModal";
import { pushWidgetSnapshot } from "../../src/lib/widgetSync";
import { useLive } from "../../src/store/live";

export default function AppLayout() {
  const [needCloud, setNeedCloud] = useState(false);

  useEffect(() => {
    startLive();
    const live = useLive.getState();
    pushWidgetSnapshot(live.status, live.connected);
    const unsub = useLive.subscribe((state, prev) => {
      if (state.status === prev.status && state.connected === prev.connected) return;
      pushWidgetSnapshot(state.status, state.connected);
    });
    void (async () => {
      try {
        const s = await endpoints.cloudStatus();
        if (s.has_credentials === false) setNeedCloud(true);
      } catch {
        /* ignore */
      }
    })();
    return () => {
      unsub();
      stopLive();
    };
  }, []);

  return (
    <>
      <Slot />
      <JackeryLoginModal visible={needCloud} onDone={() => setNeedCloud(false)} />
    </>
  );
}
