import { defineConfig } from 'vite';
import path from 'path';

export default defineConfig({
    root: 'web',
    resolve: {
        alias: {
            'gl-bench': path.resolve(__dirname, 'node_modules/gl-bench/dist/gl-bench.module.js'),
            '@cosmograph/cosmograph/data-kit': path.resolve(__dirname, 'node_modules/@cosmograph/cosmograph/data-kit/data-kit.js'),
        },
    },
    build: {
        outDir: '../web-dist',
        emptyOutDir: true,
    },
    server: {
        port: 5173,
        proxy: {
            '/api': 'http://localhost:8080',
        },
    },
});
