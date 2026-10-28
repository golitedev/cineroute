import { en } from './en';
import { es } from './es';
import { sv } from './sv';

export const languages = { en: 'English', es: 'Español', sv: 'Svenska' };
export type Language = keyof typeof languages;
export type Key = keyof typeof en;
type PluralKey = Key extends infer K ? (K extends `${infer Base}.one` ? Base : never) : never;
export type Params = Record<string, string | number | boolean>;
export type T = (key: Key | PluralKey, params?: Params) => string;
const dictionaries = { en, es, sv };
export function hasKey(key: string): key is Key {
    return Object.prototype.hasOwnProperty.call(en, key);
}
export function translator(language: Language): T {
    const plurals = new Intl.PluralRules(language);
    const numbers = new Intl.NumberFormat(language);
    return (key, params = {}) => {
        const plural = `${key}.${plurals.select(Number(params.count)) === 'one' ? 'one' : 'other'}`;
        const resolved = hasKey(plural) ? plural : (key as Key);
        const value = dictionaries[language][resolved] ?? en[resolved] ?? key;
        return value.replace(/\{(\w+)\}/g, (match, name: string) =>
            params[name] === undefined ? match : typeof params[name] === 'number' ? numbers.format(params[name]) : String(params[name])
        );
    };
}
