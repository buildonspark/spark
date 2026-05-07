const { FlatCompat } = require('@eslint/eslintrc');
const js = require('@eslint/js');

const compat = new FlatCompat({
  baseDirectory: __dirname,
  recommendedConfig: js.configs.recommended,
  allConfig: js.configs.all,
});

module.exports = [
  {
    ignores: [
      '.bundle/**',
      '.turbo/**',
      'android/**',
      'ios/**',
      'coverage/**',
      'node_modules/**',
    ],
  },
  ...compat.extends('@react-native'),
  {
    files: ['**/*.js'],
    rules: {
      'ft-flow/define-flow-type': 'off',
      'ft-flow/use-flow-type': 'off',
    },
  },
  {
    files: ['e2e/**/*.js'],
    languageOptions: {
      globals: {
        by: 'readonly',
        device: 'readonly',
        element: 'readonly',
        waitFor: 'readonly',
      },
    },
  },
  {
    rules: {
      'react-native/no-inline-styles': 'off',
    },
  },
];
