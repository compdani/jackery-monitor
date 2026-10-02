const { withDangerousMod, withXcodeProject } = require("expo/config-plugins");
const path = require("path");
const { patchFile } = require("./patch-widget-container-background");

const PHASE_NAME = "Patch widget container background";
const PHASE_SCRIPT =
  'if [ -f "$PODS_ROOT/../.xcode.env" ]; then . "$PODS_ROOT/../.xcode.env"; fi; ' +
  'if [ -f "$PODS_ROOT/../.xcode.env.local" ]; then . "$PODS_ROOT/../.xcode.env.local"; fi; ' +
  '"${NODE_BINARY:-node}" "$SRCROOT/../plugins/patch-widget-container-background.js"\n';

function targetUuid(project, name) {
  const section = project.pbxNativeTargetSection();
  for (const key of Object.keys(section)) {
    if (key.endsWith("_comment")) continue;
    const targetName = String(section[key].name || "").replace(/^"|"$/g, "");
    if (targetName === name) return key;
  }
  return null;
}

function hasPhase(project, name) {
  const phases = project.hash.project.objects.PBXShellScriptBuildPhase || {};
  const quoted = `"${name}"`;
  return Object.keys(phases).some((key) => {
    if (key.endsWith("_comment")) return false;
    const phaseName = phases[key] && phases[key].name;
    return phaseName === quoted || phaseName === name;
  });
}

function movePhaseBeforeSources(project, uuid, phaseComment) {
  const phases = project.pbxNativeTargetSection()[uuid].buildPhases;
  const addedIndex = phases.findIndex((phase) => phase.comment === phaseComment);
  const sourcesIndex = phases.findIndex((phase) => phase.comment === "Sources");
  if (addedIndex < 0 || sourcesIndex < 0 || addedIndex < sourcesIndex) return;
  const [added] = phases.splice(addedIndex, 1);
  const nextSources = phases.findIndex((phase) => phase.comment === "Sources");
  phases.splice(nextSources, 0, added);
}

function withWidgetContainerBackground(config) {
  config = withDangerousMod(config, [
    "ios",
    (cfg) => {
      patchFile(
        path.join(cfg.modRequest.platformProjectRoot, "ExpoWidgetsTarget", "StatusWidget.swift"),
      );
      return cfg;
    },
  ]);

  return withXcodeProject(config, (cfg) => {
    const project = cfg.modResults;
    const uuid = targetUuid(project, "ExpoWidgetsTarget");
    if (!uuid) {
      console.warn("[withWidgetContainerBackground] ExpoWidgetsTarget not found — skipping build phase.");
      return cfg;
    }
    if (hasPhase(project, PHASE_NAME)) return cfg;
    const added = project.addBuildPhase([], "PBXShellScriptBuildPhase", PHASE_NAME, uuid, {
      shellPath: "/bin/sh",
      shellScript: PHASE_SCRIPT,
    });
    if (added && added.buildPhase) added.buildPhase.alwaysOutOfDate = 1;
    movePhaseBeforeSources(project, uuid, PHASE_NAME);
    return cfg;
  });
}

module.exports = withWidgetContainerBackground;
