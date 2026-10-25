import { defineConfig } from '@playwright/test';
import base from './playwright.config';

export default defineConfig(base, {
    testMatch: ['cover-preview.spec.ts', 'pagination-navigation.spec.ts', 'bookharbor-brand.spec.ts', 'home.spec.ts', 'history-cards.spec.ts'],
    reporter: [['list']],
    use: { baseURL: 'http://127.0.0.1:9000' },
    webServer: [
        {
            command: 'node test/mock-server.js',
            env: { PORT: '8080' },
            url: 'http://127.0.0.1:8080/api/user/info',
            reuseExistingServer: false,
        },
        {
            command: 'node test/static-server.mjs',
            env: { PORT: '9000' },
            url: 'http://127.0.0.1:9000',
            reuseExistingServer: false,
        },
    ],
});
