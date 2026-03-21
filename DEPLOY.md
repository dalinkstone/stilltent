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

## 3. Clone the repo

```bash
git clone https://github.com/dalinkstone/stilltent.git ~/stilltent
```

## 4. Move .env into place

```bash
mv /root/.env ~/stilltent/.env
```

## 5. Run the deploy script (hardening + config)

This installs Docker, git, ufw, fail2ban, configures swap, firewall, and log rotation:

```bash
cd ~/stilltent
bash scripts/deploy-digitalocean.sh
```

The script is idempotent — safe to run again if interrupted.

## 6. Start the stack (without orchestrator)

```bash
make up
```

Wait ~30 seconds for services to boot, then verify:

```bash
make health
```

## 7. Initialize the database (first time only)

```bash
make init-db
```

## 8. Bootstrap (single test iteration)

```bash
make bootstrap
```

Review the output. If it looks good, run a single orchestrated iteration to verify end-to-end:

```bash
make test-run
```

## 9. Start autonomous mode

Only after confirming the test iteration succeeded:

```bash
make start
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
