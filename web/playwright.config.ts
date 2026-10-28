import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
    testDir: './tests',
    fullyParallel: true,
    timeout: 30000,
    use: { baseURL: 'http://127.0.0.1:4173', headless: true, trace: 'retain-on-failure' },
    projects: [
        { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 960 } } },
        { name: 'mobile', use: { ...devices['iPhone 13'], defaultBrowserType: 'chromium' } }
    ],
    webServer: { command: 'node scripts/serve-test.mjs', url: 'http://127.0.0.1:4173', reuseExistingServer: false }
});
