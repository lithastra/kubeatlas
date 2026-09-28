const { createHash } = require('node:crypto');
const { readFileSync } = require('node:fs');
const path = require('node:path');
const { fileURLToPath } = require('node:url');
const tsJest = require('ts-jest').default;

const adapterHash = createHash('sha256').update(readFileSync(__filename)).digest('hex');
const inlineMap = /(^|\r?\n)(\/\/# sourceMappingURL=data:application\/json(?:;charset=[^;,]+)?;base64,)([A-Za-z0-9+/=]+)(\r?\n)?$/;

// ts-jest 29.4.13 emits file URLs. Istanbul 5 treats those as relative paths,
// producing broken LCOV source references and non-portable HTML filenames.
// Keep the original mappings/content and Babel coverage provider; normalize
// only local source names before Jest instruments the transformed code.
function normalizeMap(map, sourcePath) {
  return {
    ...map,
    sources: map.sources.map((source) => source.startsWith('file:')
      ? path.relative(path.dirname(sourcePath), fileURLToPath(source)).split(path.sep).join('/')
      : source),
  };
}

function normalizeResult(result, sourcePath) {
  const normalized = {
    ...result,
    code: result.code.replace(inlineMap, (_comment, newline, prefix, encoded, ending = '') => {
      const map = JSON.parse(Buffer.from(encoded, 'base64').toString('utf8'));
      const replacement = Buffer.from(JSON.stringify(normalizeMap(map, sourcePath))).toString('base64');
      return newline + prefix + replacement + ending;
    }),
  };
  if (result.map) {
    const map = typeof result.map === 'string' ? JSON.parse(result.map) : result.map;
    normalized.map = normalizeMap(map, sourcePath);
  }
  return normalized;
}

module.exports = {
  normalizeResult,
  createTransformer(options) {
    const transformer = tsJest.createTransformer(options);
    return {
      canInstrument: transformer.canInstrument,
      getCacheKey(...args) {
        return adapterHash + transformer.getCacheKey(...args);
      },
      async getCacheKeyAsync(...args) {
        return adapterHash + await transformer.getCacheKeyAsync(...args);
      },
      process(source, sourcePath, options) {
        return normalizeResult(transformer.process(source, sourcePath, options), sourcePath);
      },
      async processAsync(source, sourcePath, options) {
        return normalizeResult(await transformer.processAsync(source, sourcePath, options), sourcePath);
      },
    };
  },
};
