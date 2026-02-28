| name | description | allowed-tools |
|------|-------------|---------------|
| trapiche-deploy | Deploy the current directory as an anonymous static site and return a temporary public URL. Use when asked to "deploy this", "ship it", "get a preview link", "share this project", or "deploy anonymously". | Bash(trapiche *), Bash(curl *), Bash(which *) |

# Trapiche Deploy

Deploy the current project as an anonymous static site and return a live URL — no account or GitHub required.

## Setup

Only the **Target Directory** is required. Everything else has sensible defaults.

| Parameter | Default | Example override |
|-----------|---------|------------------|
| **Target directory** | Current directory (`.`) | `--dir ./frontend` |
| **API endpoint** | `https://api.trapiche.cloud` | `--api http://localhost:8080` |

If the user says "deploy this" or "ship it", start immediately with defaults. Do not ask clarifying questions unless a specific subdirectory is mentioned.

## Workflow

```
1. Check     Verify trapiche CLI is installed
2. Install   Install CLI if missing (one-liner)
3. Deploy    Run trapiche deploy
4. Return    Share the live URL with the user
```

### 1. Check

Verify the CLI is available:

```bash
which trapiche
```

If the command is found, skip to step 3. If not found, proceed to step 2.

### 2. Install

Install the CLI with the official install script:

```bash
curl -fsSL https://trapiche.cloud/install.sh | bash
```

After installation, verify it works:

```bash
trapiche --help
```

If installation fails, tell the user and stop. Do not attempt to deploy without the CLI.

### 3. Deploy

Run the deploy command from the target directory:

```bash
trapiche deploy
```

Or with a specific subdirectory:

```bash
trapiche deploy --dir {DIR}
```

The command will:
- Compress the project (excluding `node_modules`, `.git`, `.env`, build output)
- Upload the archive to Trapiche
- Run `npm install` and `npm run build` on the server
- Return a live URL

Wait for the command to complete. It may take 1–3 minutes depending on project size and dependencies. Do not interrupt it.

### 4. Return

Once deployed, capture the URL from the output (looks like `https://{name}.trapiche.site`) and share it with the user:

```
✓ Deployed!

https://brave-wolf-4821.trapiche.site

Link expires in 7 days.
```

Tell the user the link is live and temporary — it expires in 7 days.

## Guidance

- **Only static sites are supported.** The project must have a `package.json` with a `build` script. Next.js apps must use `output: 'export'` in `next.config.js`.

- **Never deploy sensitive directories.** The CLI automatically excludes `.env` files, but confirm there are no hardcoded secrets in the source before deploying.

- **If the build fails**, read the logs printed by the CLI and report the error to the user. Common causes:
  - Missing `build` script in `package.json`
  - Next.js not configured for static export
  - Build dependencies missing

- **If the URL is already needed urgently**, tell the user the deploy is queued and they can check status via the URL pattern while waiting.

- **One deploy at a time.** The CLI is anonymous and rate-limited to 3 deploys per hour per IP. If rate-limited (429 error), tell the user to wait before retrying.
