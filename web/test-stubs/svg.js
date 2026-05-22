// Jest stub for SVG imports. Vite resolves `?url`, `?raw`, etc. at
// build time; under Jest we treat all SVG imports as a string so
// modules that import them can load. Tests do not exercise the
// raster output — they assert behaviour above the sprite layer.
module.exports = 'test-stub-svg';
module.exports.default = 'test-stub-svg';
