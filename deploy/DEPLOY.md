# Thresh — How to Deploy (All Free Tier)

Deploy the full stack in ~15 minutes. All services stay on free tiers.

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌──────────────┐
│   Vercel    │────▶│    Render    │────▶│    Render    │
│  (Nuxt.js)  │     │  (Go API)    │     │ (Python agent)│
│    FREE     │     │    FREE      │     │    FREE      │
└─────────────┘     └──────┬───────┘     └──────────────┘
                          │
                   ┌──────┴───────┐
                   │    Aiven     │── PostgreSQL (free tier)
                   │  idrive e2   │── S3-compatible storage (free tier)
                   │  Hindsight  │── Memory MCP (free tier)
                   │  Hetzner    │── Qwen3.8-27B inference
                   └──────────────┘
```

## Prerequisites

- GitHub account
- Render account (free)
- Vercel account (free)
- Aiven PostgreSQL (free tier)
- idrive e2 or any S3-compatible storage (free tier)
- Hetzner inference API key (for Qwen3.8-27B)
- Hindsight API key (for memory MCP)

## Step 1: Backend (Go) on Render

1. Go to [Render Dashboard](https://dashboard.render.com)
2. **New → Web Service** → Connect `your-repo`
3. Configure:
   - **Name:** `thresh-backend`
   - **Region:** Oregon
   - **Branch:** `main`
   - **Runtime:** Go
   - **Build Command:** `go build -o /opt/render/project/src/server ./cmd/api`
   - **Start Command:** `/opt/render/project/src/server`
   - **Plan:** Free
4. Add environment variables (see `render-backend.yaml`):
   | Variable | Value |
   |----------|-------|
   | `DATABASE_URL` | `postgres://user:pass@host:port/db?sslmode=require` |
   | `THRESH_ALLOWED_ORIGINS` | `https://your-frontend.vercel.app` |
   | `THRESH_AGENT_URL` | `https://thresh-agent.onrender.com` |
   | `S3_ENDPOINT` | `https://xxx.idrivee2-xx.com` |
   | `S3_REGION` | `eu-central-2` |
   | `S3_BUCKET` | `your-bucket` |
   | `S3_ACCESS_KEY` | `your-key` |
   | `S3_SECRET_KEY` | `your-secret` |
   | `THRESH_ADMIN_EMAIL` | `admin@example.com` |
   | `THRESH_ADMIN_PASSWORD` | `choose-a-password` |
5. Click **Create Web Service**
6. Wait ~2 min → verify `https://thresh-backend.onrender.com/api/v1/status` returns 200

## Step 2: Agent (Python) on Render

1. **New → Web Service** → Connect same repo
2. Configure:
   - **Name:** `thresh-agent`
   - **Region:** Oregon
   - **Branch:** `main`
   - **Runtime:** Python 3
   - **Root Directory:** `agent`
   - **Build Command:** `pip install -e . && pip install "mcp>=1.2"`
   - **Start Command:** `uvicorn thresh_agent.app:app --host 0.0.0.0 --port $PORT`
   - **Plan:** Free
3. Add environment variables (see `render-agent.yaml`):
   | Variable | Value |
   |----------|-------|
   | `THRESH_MODEL_GATEWAY_BASE_URL` | `https://inference.hetzner.com/api/v1/` |
   | `THRESH_MODEL_GATEWAY_KEY` | `your-hetzner-key` |
   | `THRESH_MODEL` | `Qwen3.8-27B` |
   | `HINDSIGHT_API_KEY` | `your-hindsight-key` |
   | `AGENT_HOST` | `0.0.0.0` |
   | `AGENT_PORT` | `10000` |
4. Click **Create** → verify the service is live

## Step 3: Frontend (Nuxt) on Vercel

1. Go to [Vercel Dashboard](https://vercel.com)
2. **Add New → Project** → Import `your-repo`
3. Configure:
   - **Framework Preset:** Nuxt.js
   - **Root Directory:** `web`
   - **Build Command:** `npm run build`
4. Add environment variable:
   - `NUXT_PUBLIC_BACKEND_BASE_URL` = `https://thresh-backend.onrender.com`
5. Click **Deploy**
6. After deploy, copy your Vercel URL (e.g., `https://your-app.vercel.app`)
7. **Go back to Render** → backend service → update `THRESH_ALLOWED_ORIGINS` to include your Vercel URL → redeploy

## Step 4: Register Hindsight Memory Provider

```bash
# Get admin token
TOKEN=$(curl -s -X POST https://thresh-backend.onrender.com/api/v1/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","password":"your-password"}' | jq -r .token)

# Register Hindsight
curl -X POST https://thresh-backend.onrender.com/api/v1/admin/memory-providers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"hindsight-prod","endpoint":"https://api.hindsight.vectorize.io/mcp"}'
```

## Step 5: Verify

```bash
# Backend + agent health
curl https://thresh-backend.onrender.com/api/v1/status

# Should return: {"backend": "ok", "agent": "reachable", ...}
```

Open your Vercel URL → you should see the Thresh homepage.

## Free Tier Notes

- **Render Free:** Services sleep after 15 min inactivity (~50s cold start). Fine for demos.
- **Vercel Hobby:** 100 GB bandwidth/month. Plenty for a demo.
- **Aiven Free:** 5 GB storage. Enough for thousands of records.
- **Total cost:** $0/month

## Troubleshooting

| Issue | Fix |
|-------|-----|
| CORS errors | Add your Vercel URL to `THRESH_ALLOWED_ORIGINS`, redeploy backend |
| Agent unreachable | Check `THRESH_AGENT_URL` matches your agent's Render URL |
| 502 on first load | Render cold start — wait 60s and retry |
| DB connection failed | Verify `DATABASE_URL` has `?sslmode=require` |
