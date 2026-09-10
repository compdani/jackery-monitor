const { withDangerousMod } = require("expo/config-plugins");
const fs = require("fs");
const path = require("path");

const MARKER = "containerBackground(Color(red: 11/255";
const NEEDLE = `    StaticConfiguration(kind: name, provider: WidgetsTimelineProvider(name: name)) { entry in
      WidgetsEntryView(entry: entry)
    }`;
const REPLACEMENT = `    StaticConfiguration(kind: name, provider: WidgetsTimelineProvider(name: name)) { entry in
      if #available(iOS 17.0, *) {
        WidgetsEntryView(entry: entry)
          .containerBackground(Color(red: 11/255, green: 13/255, blue: 16/255), for: .widget)
      } else {
        WidgetsEntryView(entry: entry)
      }
    }`;

function withWidgetContainerBackground(config) {
  return withDangerousMod(config, [
    "ios",
    (config) => {
      const file = path.join(
        config.modRequest.platformProjectRoot,
        "ExpoWidgetsTarget",
        "StatusWidget.swift",
      );
      if (!fs.existsSync(file)) {
        console.warn("[withWidgetContainerBackground] StatusWidget.swift not found — skipping.");
        return config;
      }
      const contents = fs.readFileSync(file, "utf8");
      if (contents.includes(MARKER)) return config;
      if (!contents.includes(NEEDLE)) {
        console.warn("[withWidgetContainerBackground] WidgetsEntryView block not found — skipping.");
        return config;
      }
      fs.writeFileSync(file, contents.replace(NEEDLE, REPLACEMENT));
      return config;
    },
  ]);
}

module.exports = withWidgetContainerBackground;
