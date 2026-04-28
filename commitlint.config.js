module.exports = {
  extends: ['@commitlint/config-conventional'],
  rules: {
    'scope-enum': [2, 'always', [
      'graph', 'discovery', 'extractor', 'aggregator',
      'store', 'api', 'cmd', 'ci', 'docs', 'helm',
      'web', 'repo', 'deps'
    ]],
  },
};
