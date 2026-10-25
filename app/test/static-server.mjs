// Short-lived SPA fixture, owned and stopped by Playwright's webServer lifecycle.
import http from 'node:http';
import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { resolve, relative, extname } from 'node:path';

const root = resolve('.output/public');
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.jpg': 'image/jpeg', '.woff2': 'font/woff2' };
http.createServer(async (request, response) => {
    if (/^\/(api|auth|get|read)\//.test(request.url)) {
        const proxy = http.request(`http://127.0.0.1:8080${request.url}`, { method: request.method, headers: request.headers }, upstream => {
            response.writeHead(upstream.statusCode, upstream.headers);
            upstream.pipe(response);
        });
        proxy.on('error', () => { response.writeHead(502); response.end(); });
        request.pipe(proxy);
        return;
    }
    try {
        const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
        let path = resolve(root, `.${pathname}`);
        if (relative(root, path).startsWith('..')) { response.writeHead(400); response.end(); return; }
        const info = await stat(path).catch(() => null);
        if (!info?.isFile()) {
            if (pathname.startsWith('/_nuxt/') || pathname.startsWith('/_i18n/')) { response.writeHead(404); response.end(); return; }
            path = resolve(root, '200.html');
        }
        response.writeHead(200, { 'Content-Type': mime[extname(path)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
        createReadStream(path).pipe(response);
    } catch {
        response.writeHead(500); response.end();
    }
}).listen(Number(process.env.PORT || 9000), '127.0.0.1');
