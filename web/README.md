# CineRoute public interface

Static React/Next.js export served by the CineRoute Go process on its public
listener. The existing Go admin interface remains independent.

Run `npm ci`, `npm run lint`, `npm run build`, and `npm test` here. From the
repository root, `make web` exports and copies assets into the embedded public
asset directory; `make build` also builds Go. Docker performs both steps.

`npm test` serves the exported files on loopback with mocked catalog/request
APIs. Install the browser once using `npx playwright install chromium`.

The interface retains the Qobuz-DL design and its MIT license in LICENSE.
