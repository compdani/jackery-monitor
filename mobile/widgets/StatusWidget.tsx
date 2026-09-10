import { HStack, Spacer, Text, VStack } from "@expo/ui/swift-ui";
import {
  containerBackground,
  font,
  foregroundStyle,
  frame,
  lineLimit,
  padding,
} from "@expo/ui/swift-ui/modifiers";
import { createWidget, type WidgetEnvironment } from "expo-widgets";

export type StatusWidgetProps = {
  name: string;
  soc: number;
  solarW: number;
  loadW: number;
  eta: string;
  connected: boolean;
  updatedAt: number;
};

const StatusWidget = (props: StatusWidgetProps, environment: WidgetEnvironment) => {
  "widget";
  const ready = typeof props.updatedAt === "number" && props.updatedAt > 0;
  const soc = typeof props.soc === "number" && Number.isFinite(props.soc) ? props.soc : -1;
  const color = soc < 0 ? "#6b7280" : soc < 20 ? "#ef4444" : "#4ade80";
  const socText = soc < 0 ? "--" : `${Math.round(soc)}`;
  const name = props.name || "Jackery";
  const solarW = Math.round(Number(props.solarW) || 0);
  const loadW = Math.round(Number(props.loadW) || 0);
  const eta = props.eta || "";
  const connected = !!props.connected;
  const isMedium = environment.widgetFamily === "systemMedium";
  const rootMods = [
    containerBackground("#0b0d10", "widget"),
    padding({ all: 14 }),
    frame({ maxWidth: Infinity, maxHeight: Infinity, alignment: "leading" as const }),
  ];

  if (!ready) {
    return (
      <VStack alignment="leading" spacing={6} modifiers={rootMods}>
        <Text modifiers={[font({ weight: "semibold", size: 12 }), foregroundStyle("#98a2b3")]}>
          Jackery
        </Text>
        <Spacer />
        <Text modifiers={[font({ weight: "medium", size: 15 }), foregroundStyle("#e6eaef"), lineLimit(3)]}>
          Open Jackery Monitor to connect
        </Text>
      </VStack>
    );
  }

  if (isMedium) {
    return (
      <VStack alignment="leading" spacing={6} modifiers={rootMods}>
        <HStack spacing={8}>
          <Text modifiers={[font({ weight: "semibold", size: 13 }), foregroundStyle("#e6eaef"), lineLimit(1)]}>
            {name}
          </Text>
          <Spacer />
          <Text
            modifiers={[
              font({ weight: "semibold", size: 11 }),
              foregroundStyle(connected ? "#4ade80" : "#fbbf24"),
            ]}
          >
            {connected ? "connected" : "offline"}
          </Text>
        </HStack>
        <HStack alignment="firstTextBaseline" spacing={2}>
          <Text modifiers={[font({ weight: "bold", size: 32 }), foregroundStyle(color)]}>{socText}</Text>
          <Text modifiers={[font({ weight: "semibold", size: 14 }), foregroundStyle("#98a2b3")]}>%</Text>
        </HStack>
        <HStack spacing={16}>
          <Text modifiers={[font({ weight: "medium", size: 12 }), foregroundStyle("#fbbf24")]}>
            Solar {solarW}W
          </Text>
          <Text modifiers={[font({ weight: "medium", size: 12 }), foregroundStyle("#e6eaef")]}>
            Load {loadW}W
          </Text>
          <Spacer />
          <Text modifiers={[font({ weight: "medium", size: 12 }), foregroundStyle("#6b7280"), lineLimit(1)]}>
            {eta}
          </Text>
        </HStack>
      </VStack>
    );
  }

  return (
    <VStack alignment="leading" spacing={2} modifiers={rootMods}>
      <Text modifiers={[font({ weight: "semibold", size: 12 }), foregroundStyle("#98a2b3"), lineLimit(1)]}>
        {name}
      </Text>
      <Spacer />
      <HStack alignment="firstTextBaseline" spacing={2}>
        <Text modifiers={[font({ weight: "bold", size: 40 }), foregroundStyle(color)]}>{socText}</Text>
        <Text modifiers={[font({ weight: "semibold", size: 16 }), foregroundStyle("#98a2b3")]}>%</Text>
      </HStack>
      <Text modifiers={[font({ weight: "medium", size: 11 }), foregroundStyle("#6b7280"), lineLimit(1)]}>
        {solarW}W in / {loadW}W out
      </Text>
    </VStack>
  );
};

export default createWidget("StatusWidget", StatusWidget);
