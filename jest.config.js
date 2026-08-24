// force timezone to UTC to allow tests to work regardless of local timezone
// generally used by snapshots, but can affect specific tests
process.env.TZ = 'UTC';

const { grafanaESModules, nodeModulesToTransform } = require('./.config/jest/utils');

module.exports = {
  // Jest configuration provided by Grafana scaffolding
  ...require('./.config/jest.config'),
  // @grafana/ui 13.2 pulls in @react-hookz/web (and its @ver0/deep-equal), which ship ESM only
  transformIgnorePatterns: [nodeModulesToTransform([...grafanaESModules, '@react-hookz/web', '@ver0/deep-equal'])],
};
