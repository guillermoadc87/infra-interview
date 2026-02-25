# Troubleshooting

## Project-Specific Notes

- Colima profile: `dfns-interview`
- kubectl context: `colima-dfns-interview` (set automatically by `setup.sh`)
- Kubernetes namespace: `interview-test`
- k3s uses the Docker runtime, so `docker build` makes images available to k3s automatically
- Vault is deployed in dev mode by `setup.sh` (not via a local chart)
- OpenTofu providers require running services in the cluster (the `tunnel` provider port-forwards to k8s services) — run `setup.sh` before `tofu apply`

## Full Reset

```bash
./scripts/cleanup.sh
./scripts/setup.sh
```
