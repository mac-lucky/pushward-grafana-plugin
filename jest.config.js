// force timezone to UTC to allow tests to work regardless of local timezone
// generally used by snapshots, but can affect specific tests
process.env.TZ = 'UTC';

const { grafanaESModules, nodeModulesToTransform } = require('./.config/jest/utils');

// Jest configuration provided by Grafana scaffolding
const base = require('./.config/jest.config');

module.exports = {
  ...base,
  // @grafana/ui 13.2 pulls in @react-hookz/web (and its @ver0/deep-equal), which ship ESM only
  transformIgnorePatterns: [nodeModulesToTransform([...grafanaESModules, '@react-hookz/web', '@ver0/deep-equal'])],
  // d3-interpolate 3.0.2 resolves to dist/*.mjs, which the scaffold's (t|j)sx? transform skips
  transform: { ...base.transform, '^.+\\.mjs$': base.transform['^.+\\.(t|j)sx?$'] },
};
