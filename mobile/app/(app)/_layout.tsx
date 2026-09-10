import { Slot } from "expo-router";
import { useEffect, useState } from "react";
import { endpoints } from "../../src/api/client";
import { startLive, stopLive } from "../../src/api/ws";
import { JackeryLoginModal } from "../../src/components/JackeryLoginModal";

export default function AppLayout() {
  const [needCloud, setNeedCloud] = useState(false);

  useEffect(() => {
    startLive();
    void (async () => {
      try {
        const s = await endpoints.cloudStatus();
        if (s.has_credentials === false) setNeedCloud(true);
      } catch {
        /* ignore */
      }
    })();
    return () => stopLive();
  }, []);

  return (
    <>
      <Slot />
      <JackeryLoginModal visible={needCloud} onDone={() => setNeedCloud(false)} />
    </>
  );
}
