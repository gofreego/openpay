import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist', 'src/apis'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': [
        'warn',
        { allowConstantExport: true },
      ],
      // Amounts are int64 minor units carried as strings (plan.md U-D4). A
      // JavaScript number is a float64: it cannot hold every int64 and
      // cannot add 0.1 and 0.2. Amounts go through utils/money.ts and BigInt,
      // never through these.
      'no-restricted-syntax': ['error',
        { selector: "CallExpression[callee.name='parseFloat']", message: 'Use Money.fromMajor / Money.parse: floats must never touch money.' },
        { selector: "CallExpression[callee.object.name='Number'][callee.property.name='parseFloat']", message: 'Use Money.fromMajor / Money.parse: floats must never touch money.' },
        { selector: "CallExpression[callee.name='Number']", message: 'Number() turns an amount into a float. Use Money.parse (BigInt), or Number.parseInt for a non-money integer.' },
        { selector: "CallExpression[callee.property.name='toFixed']", message: 'toFixed rounds floats. Format amounts with Money.format.' },
        { selector: "UnaryExpression[operator='+'][argument.type!='Literal']", message: 'Unary + turns a string into a float. Use Money.parse for amounts.' },
      ],
    },
  },
)
