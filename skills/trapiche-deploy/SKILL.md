---
name: trapiche-deploy
description: Deploy the current directory to Trapiche and return a live URL. Use when asked to "deploy this", "ship it", "get a preview link", or "deploy to production".
allowed-tools: Bash(trapiche:*), Bash(curl:*), Bash(which:*)
---

# Trapiche Deploy

Deploy the current project to Trapiche and return a live URL.

## Prerequisites

- User must be logged in: `trapiche auth login`
- Project must be a **static** site (Next.js with `output: 'export'`, Vite, Astro static, CRA, etc.) or a **container** app (server apps). A container app has a `Dockerfile` at the root, or a server framework such as Express, Fastify, NestJS, or Next.js without `output: 'export'`. It must listen on port **3000**.

## Setup

| Parameter | Default | Example override |
|-----------|---------|------------------|
| **Target directory** | Current directory (`.`) | `--dir ./frontend` |
| **Repository** | `git remote origin` | `--repo owner/repo` |
| **Branch metadata** | Current git branch | `--branch main` |
| **App name** | Repo name | `--name my-app` |
| **Type** | `auto` (detected) | `--type static` or `--type ssr` (container) |
| **API endpoint** | `https://api.trapiche.cloud` | `--api http://localhost:8080` |

## Workflow

```
1. Check     Verify trapiche CLI is installed
2. Install   Install CLI if missing (one-liner)
3. Auth      Ensure user is logged in
4. Deploy    Run trapiche deploy
5. Return    Share the live URL with the user
```

### 1. Check

```bash
which trapiche
```

### 2. Install

```bash
curl -fsSL https://trapiche.cloud/install.sh | bash
```

### 3. Auth

```bash
trapiche auth status || trapiche auth login
```

If not logged in, `trapiche auth login` opens the browser. Wait for it to complete.

### 4. Deploy

```bash
trapiche deploy
```

Or with options:

```bash
trapiche deploy --dir {DIR} --repo owner/repo --detach
```

Wait for the command to complete unless `--detach` was used.

### 5. Return

Share the URL from the output:

```
✓ Deployed!

https://my-app-brave-wolf.trapiche.site
```

## Other commands

```bash
trapiche deployments list
trapiche logs              # latest deploy for current repo
trapiche logs dep_abc123   # specific deployment
trapiche link dep_abc123   # link project to existing deployment
trapiche unlink            # remove trapiche.json
trapiche deploy --new      # force a new deployment
trapiche env list          # environment variables of the linked deployment (values hidden)
trapiche env set KEY=VALUE # set variables, applies on the next deploy
trapiche env unset KEY     # remove a variable
```

## Redeploy

After the first deploy, the CLI writes `trapiche.json` in the project directory (or `--dir` root). Subsequent `trapiche deploy` calls update that deployment in place — same URL, no new deployment slot.

- `trapiche deploy --new` — create a fresh deployment and overwrite `trapiche.json`
- `trapiche link dep_xxx` — recover linking to an existing deployment
- `trapiche unlink` — remove `trapiche.json` if the link is stale

Add `trapiche.json` to `.gitignore` if you do not want deployment metadata in git.

## Guidance

- Auth is required — anonymous deploy is not supported.
- Static sites and container (server) apps are supported via CLI upload. The type of a deployment cannot change. To switch type, use `trapiche deploy --new`.
- Container apps get a URL like `https://my-app.ssr.trapiche.cloud`. The app must listen on port 3000. The CLI does not send `.env` files. Set variables with `trapiche env set KEY=VALUE` (or `--file .env`). They apply on the next deploy.
- The CLI uploads local source; GitHub is metadata only (repo linking), not used to fetch code.
- If build fails, read the logs printed by the CLI and report the error.
