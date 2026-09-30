# Examples

Deployment examples. Each directory has its own `config.yaml` with the shared settings; `config.example.yaml` in the repo root documents every key. The bot uses Socket Mode, so none of them exposes a port. Create the Slack app first (README, steps 1 to 4). Run only one instance per Slack app: Slack delivers each event to one open connection, so a local instance started with the tokens of an app that already runs elsewhere takes events away from it, and with an empty database pins a new board in every channel.

| Directory | Runs | Database |
|---|---|---|
| `docker-compose/` | locally, built from this checkout | Postgres container (SQLite by removing it) |
| `kubernetes/sqlite/` | Kubernetes, published image | SQLite file on a PersistentVolumeClaim |
| `kubernetes/postgres/` | Kubernetes, published image | Postgres StatefulSet in the same namespace |

## Docker Compose

```
cd examples/docker-compose && cp .env.example .env && docker compose up --build
```

Put the tokens in `.env` (gitignored). The Postgres password is a fixed local-only value in `compose.yaml`. `docker compose down -v` also deletes the data volumes.

## Kubernetes

Both variants are kustomize directories in namespace `itakeit`, running one replica with the `Recreate` strategy for the reason above. Tokens never go in the files: apply first, which creates the namespace, then create the Secrets. The pod waits in `CreateContainerConfigError` until they exist. Both commands are safe to re-run. `kubectl delete -k` deletes the namespace, and with it the Secrets and the database volume.

SQLite:

```
kubectl apply -k examples/kubernetes/sqlite
```

```
kubectl -n itakeit create secret generic itakeit-slack --from-literal=SLACK_BOT_TOKEN=xoxb-... --from-literal=SLACK_APP_TOKEN=xapp-... --dry-run=client -o yaml | kubectl apply -f -
```

Postgres (the password goes into a URL, so keep it URL-safe, as hex is):

```
kubectl apply -k examples/kubernetes/postgres
```

```
kubectl -n itakeit create secret generic itakeit-slack --from-literal=SLACK_BOT_TOKEN=xoxb-... --from-literal=SLACK_APP_TOKEN=xapp-... --dry-run=client -o yaml | kubectl apply -f -
```

Create the database password once. Re-running this with a new password breaks the bot's login, because Postgres keeps the password from its first start:

```
kubectl -n itakeit create secret generic itakeit-postgres --from-literal=password="$(openssl rand -hex 24)"
```

The bot has no readiness endpoint. Check it with `kubectl -n itakeit logs deploy/itakeit`, which logs `authenticated` once connected. Until Postgres is ready the bot exits and Kubernetes restarts it, which is expected on the first apply. The bot pod runs under the "restricted" Pod Security level, the example Postgres needs "baseline". Pin `image:` to a release tag (`X.Y.Z`) or `sha-<short>` rather than `latest` for anything you depend on. With your own Postgres, `kubernetes/postgres/setup-db.sh <admin-url>` creates the user and database (the bot creates its tables on start, there are no migrations) and prints the Secret command. Then delete `postgres.yaml` from `kustomization.yaml` and point `ITAKEIT_DATABASE_URL` at the server.
