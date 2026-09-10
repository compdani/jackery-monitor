module.exports = function (api) {
  api.cache(true);
  // #region agent log
  try {
    const resolved = require.resolve("babel-preset-expo");
    require("fs").appendFileSync(
      "/Volumes/mini512/jackery-monitor/.cursor/debug-856ad8.log",
      JSON.stringify({sessionId:"856ad8",hypothesisId:"A",location:"babel.config.js",message:"babel-preset-expo resolved",data:{resolved},timestamp:Date.now()}) + "\n",
    );
  } catch (e) {
    require("fs").appendFileSync(
      "/Volumes/mini512/jackery-monitor/.cursor/debug-856ad8.log",
      JSON.stringify({sessionId:"856ad8",hypothesisId:"A",location:"babel.config.js",message:"babel-preset-expo missing",data:{error:String(e && e.message),code:e && e.code},timestamp:Date.now()}) + "\n",
    );
  }
  try {
    const assetResolved = require.resolve("expo-asset/package.json");
    require("fs").appendFileSync(
      "/Volumes/mini512/jackery-monitor/.cursor/debug-856ad8.log",
      JSON.stringify({sessionId:"856ad8",runId:"asset-debug",hypothesisId:"F",location:"babel.config.js",message:"expo-asset resolved",data:{assetResolved,topLevel:assetResolved.includes("/node_modules/expo-asset/") && !assetResolved.includes("/expo/node_modules/")},timestamp:Date.now()}) + "\n",
    );
  } catch (e) {
    require("fs").appendFileSync(
      "/Volumes/mini512/jackery-monitor/.cursor/debug-856ad8.log",
      JSON.stringify({sessionId:"856ad8",runId:"asset-debug",hypothesisId:"F",location:"babel.config.js",message:"expo-asset missing at resolve",data:{error:String(e && e.message),code:e && e.code},timestamp:Date.now()}) + "\n",
    );
  }
  // #endregion
  return {
    presets: ["babel-preset-expo"],
  };
};
