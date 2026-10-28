import type { MetadataRoute } from 'next';

export const dynamic = 'force-static';

export default function manifest(): MetadataRoute.Manifest {
    return {
        name: 'CineRoute',
        short_name: 'CineRoute',
        description: 'Movies, TV and music. All in one place.',
        start_url: '/',
        scope: '/',
        display: 'standalone',
        background_color: '#000000',
        theme_color: '#000000',
        orientation: 'portrait',
        icons: [{ src: '/favicon.svg', sizes: 'any', type: 'image/svg+xml' }]
    };
}
