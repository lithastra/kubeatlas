// Jest config. Phase 1 wires the web bar at >=60% coverage in the
// W7 CI job; this file is the test-runner side of that contract.
//
// Authored as JS (not TS) because Jest's TS-config loader needs
// `ts-node`, which Phase 1's dependency whitelist deliberately omits.
// Vite handles every other TS load path, so this is the only file
// that needs the JS extension.
/** @type {import('jest').Config} */
const config = {
  testEnvironment: 'jsdom',

  // ts-jest with the ESM-aware transformer covers the TypeScript +
  // JSX cases the rest of the codebase uses.
  transform: {
    '^.+\\.tsx?$': ['ts-jest', { tsconfig: 'tsconfig.json' }],
  },
  moduleFileExtensions: ['ts', 'tsx', 'js', 'jsx', 'json'],

  // CSS imports become identity proxies so component tests can mount
  // without a real bundler.
  moduleNameMapper: {
    '\\.(css|less|scss|sass)$': 'identity-obj-proxy',
  },

  // jest-dom matchers (toBeInTheDocument, etc.) load globally.
  setupFilesAfterEnv: ['@testing-library/jest-dom'],

  coverageDirectory: 'coverage',
  coverageReporters: ['json-summary', 'text', 'lcov'],
  collectCoverageFrom: ['src/**/*.{ts,tsx}', '!src/**/*.d.ts', '!src/main.tsx'],
};

module.exports = config;
