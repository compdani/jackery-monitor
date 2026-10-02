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

function patchFile(file) {
  if (!fs.existsSync(file)) {
    console.warn("[withWidgetContainerBackground] StatusWidget.swift not found — skipping.");
    return false;
  }
  const contents = fs.readFileSync(file, "utf8");
  if (contents.includes(MARKER)) return false;
  if (!contents.includes(NEEDLE)) {
    console.warn("[withWidgetContainerBackground] WidgetsEntryView block not found — skipping.");
    return false;
  }
  fs.writeFileSync(file, contents.replace(NEEDLE, REPLACEMENT));
  return true;
}

if (require.main === module) {
  const file =
    process.argv[2] ||
    path.join(__dirname, "..", "ios", "ExpoWidgetsTarget", "StatusWidget.swift");
  patchFile(file);
}

module.exports = { patchFile, MARKER };
