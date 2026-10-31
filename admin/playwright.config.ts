import { defineConfig, devices } from '@playwright/test';
export default defineConfig({
	testDir: './tests',
	fullyParallel: true,
	timeout: 30000,
	use: { headless: true, trace: 'retain-on-failure' },
	projects: [
		{ name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 1000 } } },
		{ name: 'mobile', use: { ...devices['iPhone 13'], defaultBrowserType: 'chromium' } },
	],
});
