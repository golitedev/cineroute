import coreWebVitals from 'eslint-config-next/core-web-vitals';
import typescript from 'eslint-config-next/typescript';

const config = [
    { ignores: ['out/**', '.next/**', 'next-env.d.ts'] },
    ...coreWebVitals,
    ...typescript,
    {
        rules: {
            '@typescript-eslint/no-explicit-any': 'off',
            '@next/next/no-img-element': 'off',
            'react-hooks/exhaustive-deps': 'off',
            // The inherited catalog measures artwork after rendering.
            'react-hooks/set-state-in-effect': 'off',
            'react-hooks/refs': 'off'
        }
    }
];

export default config;
