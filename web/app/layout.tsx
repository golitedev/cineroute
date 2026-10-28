import ParticlesComponent from '@/components/particles';
import type { Metadata, Viewport } from 'next';

import './globals.css';
import { I18nProvider } from '@/lib/i18n';
import LanguageMenu from '@/components/language-menu';

export const viewport: Viewport = {
    width: 'device-width',
    initialScale: 1,
    viewportFit: 'cover',
    themeColor: '#000000'
};

export const metadata: Metadata = {
    title: 'CineRoute',
    description: 'Movies, TV and Music. All in one place.',
    icons: { icon: [{ url: '/favicon.png', type: 'image/png', sizes: '64x64' }] }
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
    return (
        <html lang='en' className='dark'>
            <body className='antialiased'>
                <ParticlesComponent className='z-[-1] h-full w-full fixed' />
                <I18nProvider>
                    <LanguageMenu />
                    {children}
                </I18nProvider>
            </body>
        </html>
    );
}
