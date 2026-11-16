const path = require('path');
const { getDefaultConfig } = require('expo/metro-config');
const config = getDefaultConfig(__dirname);
// The dependency-free SSE transport is shared with both web frontends.
config.watchFolders = [...(config.watchFolders || []), path.resolve(__dirname, '../web')];
module.exports = config;
