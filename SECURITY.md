# Security policy

## Reporting a vulnerability

Please **do not** open a public issue. Report vulnerabilities privately through
[GitHub private vulnerability reporting](https://github.com/shaansundar/doppio/security/advisories/new).
We aim to acknowledge reports within 3 business days.

## Supported versions

doppio is pre-1.0. Only the latest release and `main` receive security fixes.

## Threat model

doppio runs a local HTTP daemon that accepts images and runs ML models. The
design assumes other local users and web pages in your browser are untrusted.

| Protection | How |
|---|---|
| Network exposure | `doppio serve` refuses to bind to anything other than a loopback address. |
| Other local processes | Every endpoint except `/healthz` requires `Authorization: Bearer <token>`. The token is 256 random bits stored in `<config>/token` with mode 0600; doppio refuses to use a token readable by others. |
| DNS rebinding and browsers | Requests whose `Host` header is not `127.0.0.1:<port>`, `localhost:<port>` or `[::1]:<port>` are rejected (421). CORS headers are only sent to explicitly allow-listed origins. |
| Hostile uploads | Size limit (default 200 MB), magic-byte sniffing (JPEG/PNG/WebP/TIFF/HEIC only), decompression-bomb pixel limit in the decoder, and filename sanitizing. |
| Path traversal | Packs are served only for artifacts listed in their manifest, through `os.Root`. Worker artifact paths are checked with `filepath.IsLocal`. Client downloads verify each artifact's SHA-256. |
| Worker IPC | gRPC over a Unix socket in a per-user 0700 directory, socket mode 0600. The worker exits when the daemon goes away. |
| Supply chain | Model revisions are pinned to commit SHAs. Python dependencies are locked in `worker/uv.lock`. GitHub-hosted Python dependencies are opt-in (`doppio setup`). |

Out of scope: a malicious user with your account's privileges, and the
behaviour of third-party models you send packs to.
