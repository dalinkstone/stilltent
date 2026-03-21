# Deploy stilltent to DigitalOcean VPS

Assumes: Ubuntu 24.04 droplet already created (8 GB / 2 vCPU / 160 GB / $48 mo).

## 1. Upload .env to the VPS

On your local machine, prepare `.env` from `.env.example` with your real values, then:

```bash
scp .env root@<DROPLET_IP>:/root/.env
```

## 2. SSH in

```bash
ssh root@<DROPLET_IP>
```

## 3. Clone the repo and move .env into place

```bash
git clone https://github.com/dalinkstone/stilltent.git ~/stilltent
mv /root/.env ~/stilltent/.env
```

## 4. Run the deploy script

Handles everything in one shot: system hardening (Docker, ufw, fail2ban, swap, log rotation), starts the stack, initializes the database, runs health checks, and bootstraps the first iteration.

```bash
cd ~/stilltent
bash scripts/deploy-digitalocean.sh
```

The script is idempotent — safe to run again if interrupted. If `.env` is missing, it will prompt for the three required values (OPENROUTER_API_KEY, GITHUB_TOKEN, TARGET_REPO).

## 5. Review bootstrap output

The deploy script prints the first iteration result at the end. Check:

- Did the agent understand SKILL.md?
- New branches/PRs on the target repo: `gh pr list`
- Service health: `make health`

## 6. Test the orchestrator loop (optional)

Run a single iteration through the orchestrator to verify end-to-end before going autonomous:

```bash
make test-run
```

## 7. Start autonomous mode

Only after confirming the bootstrap (and optional test-run) succeeded:

```bash
make start
```

## Step-by-step alternative

If you want more control instead of the one-shot deploy script, replace step 4 with:

```bash
bash scripts/harden-vps.sh        # system hardening only
make up                            # start stack (auto-initializes DB on first run)
make health                        # verify all services are healthy
make bootstrap                     # clone target repo, seed memory, run first iteration
```

## Monitor and control

| Command        | What it does                    |
|----------------|---------------------------------|
| `make logs`    | Follow all service logs         |
| `make stats`   | Show iteration count and spend  |
| `make cost`    | Show current spend vs budget    |
| `make health`  | Check service health            |
| `make pause`   | Stop the agent (keeps services) |
| `make resume`  | Resume the agent                |
| `make down`    | Stop everything                 |
