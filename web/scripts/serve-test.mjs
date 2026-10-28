// A test-only static server. API calls are intercepted by Playwright fixtures.
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import path from 'node:path';
const root = path.resolve('out');
const csp = readFileSync('../internal/web/listeners.go', 'utf8').match(/Set\("Content-Security-Policy", "([^"]+)"/)[1];
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' };
createServer(async (req, res) => {
    res.setHeader('Content-Security-Policy', csp);
    const url = new URL(req.url, 'http://localhost');
    const relative = url.pathname === '/' ? '/index.html' : url.pathname === '/login' ? '/login.html' : url.pathname;
    const filename = path.resolve(root, '.' + relative);
    if (!filename.startsWith(root + path.sep)) {
        res.writeHead(403).end();
        return;
    }
    try {
        const data = await readFile(filename);
        res.writeHead(200, { 'Content-Type': mime[path.extname(filename)] || 'application/octet-stream' }).end(data);
    } catch {
        res.writeHead(404).end();
    }
}).listen(4173, '127.0.0.1');
