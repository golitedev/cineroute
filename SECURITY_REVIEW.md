# LAN admin / public portal security review

Reviewed 2026-10-25. This is a code review with regression and browser testing,
not an independent penetration test or a guarantee against every compromise.

## Scope and trust boundary

One CineRoute process owns the library, qBittorrent client and request worker.
The LAN admin listener and internet request listener use separate Go route
tables. Incoming Host, URL parameters and proxy headers cannot select the
other table. The public table contains only static portal resources, login,
session/logout, TMDB catalog search/details and the authenticated user's own
request list/create/cancel operations. It never falls back to the admin router.

Reviewed admin entry points include torrent intake/type/match/destination/submit,
copy scans/hardlinks, hardlink deletion/relinking, subtitle settings/jobs/cleanup,
request settings/indexers and queue approval/retry/cancellation. These actions
remain registered only on the admin handler. A public user supplies a TMDB ID
and season selections, not filesystem
paths, tracker URLs, tracker priorities or routing settings.
Server-side policy still controls destinations and release selection.

Approval, quotas, existing-file protections and shared-download placement remain
in force. Public users cannot override audio priorities or tracker priorities.
The public interface does not expose individual tracker configurations.
Release availability is advisory metadata for the request dialog; it does not
filter the catalog or block new requests.

UNIT3D language priorities are admin-only policy. The worker reads saved tracker
credentials from Prowlarr into a private, five-minute in-memory configuration
cache; public/admin indexer lists continue to use an explicit credential-free
resource. Native lookups use a fixed API path on the configured tracker origin,
matching only numeric same-origin torrent identities. Public tracker keys require
HTTPS (HTTP is limited to loopback test servers), use a Bearer header and never
follow redirects, including when retrieving configuration from Prowlarr. No
search-result URL can select a destination for credentials. Twelve-second request
timeouts, response/report size limits, bounded parsed-result caches, pacing and
five-minute failure backoff limit the extra work. Only normalized language codes,
recognized text/image format flags and preference points enter download receipts
and admin history. Raw reports,
tracker keys and credential-bearing download URLs are excluded. Tests cover
redirects, mismatched origins/identities, disabled trackers, unknown reports,
independent profiles, and public users attempting policy overrides.

## Findings and changes

| Finding | Resolution |
| --- | --- |
| A shared listener exposed admin login/API routes through any full-app tunnel. | Separate listeners, route tables, pages and admin scripts. Admin accounts cannot authenticate to the public listener; user accounts cannot authenticate to the admin listener. |
| Missing credentials enabled anonymous administration. | Startup validation and HTTP checks now fail closed. Legacy credentials work only as an admin; a public listener requires an enabled config user. |
| Cookie sessions were shared across interfaces and admin sessions lasted 90 days. | Separate host-only HttpOnly cookies, audience-bound signatures, admin SameSite Strict / 12-hour sessions, user 90-day sessions. Renaming a cookie does not cross the boundary. |
| Legacy cookies used a password-derived signing key without a persisted random server secret. | Legacy and config-account sessions now use the persisted 32-byte session key, with credentials bound into the signature. Existing key files are restricted to mode 0600. |
| Cookie mutations depended on an optional Origin header check. | Added per-session, per-interface CSRF tokens, Origin/Referer scheme+host+port checks and Fetch Metadata checks. A dummy Authorization header cannot bypass cookie CSRF. Administration also rejects cross-origin browser reads. |
| Basic Auth allowed unlimited failed password attempts. | Failed Basic Auth and form sign-ins are limited per actual peer and interface; the tracking map is bounded and expired entries are removed. Cookie sessions remain usable during a password-attempt lockout. |
| Binding the admin server on all container interfaces did not restrict remote peers or DNS-rebinding Host values. | Private/loopback CIDR peer checks, exact LAN hostname/IP policy and rejection of forwarding headers on admin requests. Forwarded client IPs are never trusted for admin authorization. |
| The installed Go 1.26.8 toolchain had 12 reachable standard-library advisory findings in the scan. | Raised the build minimum and pinned the Docker builder to Go 1.26.10. Updated x/crypto to 0.56.0. CI runs race tests and govulncheck. |

Added no-store responses for pages/APIs, framing restrictions, nosniff,
same-origin resource policy and a CSP limiting connections/resources. Current
HTML uses inline scripts/event handlers, so CSP retains `unsafe-inline`; it is
not a substitute for escaping. Reviewed the request queue's metadata, owner,
messages and diagnostics rendering for escaping and data-attribute handling.
The public page and its scripts do not include the admin UI or admin API calls.

The Compose example binds admin publication to a specific LAN IP and the public
publication to loopback for host cloudflared. It drops Linux capabilities, sets
no-new-privileges, runs as UID/GID 1000 and uses a read-only root filesystem.

## Verification

- Full Go suite with `go test -race ./...`, `go vet ./...`, build and JavaScript
  syntax checks.
- Admin APIs denied on the public handler across GET/HEAD/POST/PUT/PATCH/DELETE/
  OPTIONS, with no credentials, a user cookie, an admin cookie and admin Basic
  Auth; spoofed proxy headers do not change this.
- Encoded segments, path traversal, double slashes and normalized redirects
  cannot reach admin APIs through the public listener.
- Public admin login rejected; user/admin cookie names, signatures, roles and
  lifetimes isolated, including renamed cookies and correctly signed tokens
  carrying a role inappropriate for that listener.
- Admin requests rejected for public/invalid peers, forged LAN headers,
  Cloudflare/proxy headers, unapproved Host values and public browser origins.
- Cookie mutations reject absent/wrong CSRF tokens, dummy Basic Auth, mismatched
  schemes/ports, null Origin, cross-site/same-site Fetch Metadata and bad Referer.
- A public cookie-authenticated request using simulated Cloudflare headers
  proceeds through the shared worker and appears in the LAN admin queue.
- Actual running two-port service checked in Chromium: user/admin login,
  separate pages/cookies, CSRF mutation, cross-origin blocking and desktop/mobile
  portal rendering. Test media/state stayed under temporary directories.
- Docker Compose configuration validates. No production container, media,
  firewall, tunnel, router or NAS configuration was changed.

After patching, govulncheck reports zero reachable or imported-package
vulnerabilities. Its module-level report still flags the unused, unmaintained
x/crypto/openpgp package (GO-2026-5932); CineRoute imports bcrypt, not openpgp or
ssh. This does not establish the security of every runtime binary in the image;
ffmpeg, alass and base-image packages were not separately vulnerability-scanned.

## Deployment assumptions and limits

- Publish only the public port through Cloudflare. Do not port-forward admin.
  The application checks are additional protection; retain LAN/firewall/Docker
  isolation. A trusted local proxy that strips forwarding headers can obscure
  the true origin of a connection, so it must not forward external traffic to
  administration. UFW alone does not reliably restrict Docker-published ports.
- Administration uses direct HTTP unless secured externally. Use a trusted LAN
  or an SSH-encrypted connection; the LAN restriction does not encrypt traffic.
  HTTP admin cookies cannot use Secure; HTTPS public cookies do use it when the
  tunnel provides X-Forwarded-Proto=https. These are host-only cookies, but web
  cookies are not isolated by port; names and signature scopes enforce separation.
- Sessions are signed and stateless. Logout clears the browser cookie; a copied
  token remains valid until expiry or a credential/role/account change followed
  by restart. Rotate credentials and restart if a token is compromised.
- This is a network/application authorization boundary inside one process,
  not an OS sandbox between the portal and array access. A remote code execution
  flaw in the public application could reach the same writable media mounts.
  Public user quotas and request policy also matter for disk/resource use.
- Admin credentials and permitted LAN clients remain trusted. This review
  prevents public users from invoking admin routes; it does not remove the
  admin's intentional hardlink, subtitle and routing capabilities.

## References

- [OWASP CSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
- [Cloudflare Tunnel ingress](https://developers.cloudflare.com/tunnel/features/locally-managed-tunnels/configuration-file/)
- [Docker port publishing](https://docs.docker.com/engine/network/port-publishing/)
- [Docker firewall interaction](https://docs.docker.com/engine/network/packet-filtering-firewalls/)
- [Go vulnerability database](https://pkg.go.dev/vuln/)
