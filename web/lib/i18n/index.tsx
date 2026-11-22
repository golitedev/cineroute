'use client';

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { getMe, type Session } from '@/lib/cineroute-api';
import { languages, translator, type Language, type T } from './core';
export * from './core';

const browserLanguageKey = 'cineroute.lang';
const accountLanguageKey = (user: string) => `${browserLanguageKey}.${user}`;
const isLanguage = (value?: string | null): value is Language => !!value && Object.prototype.hasOwnProperty.call(languages, value);

export function resetAccountLanguage({ user, language }: Session) {
    if (!isLanguage(language)) return;
    try {
        localStorage.removeItem(accountLanguageKey(user));
    } catch {}
}

const I18nContext = createContext({ language: 'en' as Language, setLanguage: (() => {}) as (language: Language) => void, ready: false, t: translator('en') });
export function I18nProvider({ children }: { children: ReactNode }) {
    const [language, updateLanguage] = useState<Language>('en');
    const [ready, setReady] = useState(false);
    const preference = useRef({ key: browserLanguageKey, fallback: 'en' as Language });
    const setLanguage = useCallback((value: Language) => {
        updateLanguage(value);
        try {
            localStorage.setItem(preference.current.key, value);
        } catch {}
    }, []);
    useEffect(() => {
        let active = true;
        const detected = navigator.language.toLowerCase().split('-')[0];
        preference.current = { key: browserLanguageKey, fallback: isLanguage(detected) ? detected : 'en' };
        const readLanguage = () => {
            const { key, fallback } = preference.current;
            let saved: string | null = null;
            try {
                saved = localStorage.getItem(key);
            } catch {}
            updateLanguage(isLanguage(saved) ? saved : fallback);
        };
        readLanguage();
        void getMe()
            .then(({ user, language }) => {
                if (!active || !isLanguage(language)) return;
                preference.current = { key: accountLanguageKey(user), fallback: language };
                readLanguage();
            })
            .catch(() => {})
            .finally(() => {
                if (active) setReady(true);
            });
        const sync = (event: StorageEvent) => {
            if (event.key === preference.current.key || event.key === null) readLanguage();
        };
        window.addEventListener('storage', sync);
        return () => {
            active = false;
            window.removeEventListener('storage', sync);
        };
    }, []);
    const t = useMemo<T>(() => translator(language), [language]);
    useEffect(() => {
        document.documentElement.lang = language;
        document.querySelector('meta[name="description"]')?.setAttribute('content', t('tagline'));
    }, [language, t]);
    return <I18nContext.Provider value={{ language, setLanguage, ready, t }}>{children}</I18nContext.Provider>;
}
export const useI18n = () => useContext(I18nContext);
export const useT = () => useI18n().t;
