'use client';

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { languages, translator, type Language, type T } from './core';
export * from './core';

const I18nContext = createContext({ language: 'en' as Language, setLanguage: (() => {}) as (language: Language) => void, t: translator('en') });
export function I18nProvider({ children }: { children: ReactNode }) {
    const [language, updateLanguage] = useState<Language>('en');
    const setLanguage = useCallback((value: Language) => {
        updateLanguage(value);
        document.documentElement.lang = value;
        try {
            localStorage.setItem('cineroute.lang', value);
        } catch {}
    }, []);
    useEffect(() => {
        let saved: string | null = null;
        try {
            saved = localStorage.getItem('cineroute.lang');
        } catch {}
        const detected = navigator.language.toLowerCase().split('-')[0];
        const value =
            saved && Object.prototype.hasOwnProperty.call(languages, saved)
                ? saved
                : Object.prototype.hasOwnProperty.call(languages, detected)
                  ? detected
                  : 'en';
        setLanguage(value as Language);
        const sync = (event: StorageEvent) => {
            if (event.key === 'cineroute.lang' && event.newValue && Object.prototype.hasOwnProperty.call(languages, event.newValue))
                setLanguage(event.newValue as Language);
        };
        window.addEventListener('storage', sync);
        return () => window.removeEventListener('storage', sync);
    }, [setLanguage]);
    const t = useMemo<T>(() => translator(language), [language]);
    useEffect(() => {
        document.querySelector('meta[name="description"]')?.setAttribute('content', t('tagline'));
    }, [t]);
    return <I18nContext.Provider value={{ language, setLanguage, t }}>{children}</I18nContext.Provider>;
}
export const useI18n = () => useContext(I18nContext);
export const useT = () => useI18n().t;
