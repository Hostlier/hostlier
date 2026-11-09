---
name: hostlier-tunnel
description: Manage generic Hostlier HTTPS tunnel reservations while Bridge owns local forwarding configuration and setup.
---

# Hostlier tunnel

The gateway operator configures `hostlier-controller run --tunnel-domain tunnels.example.com --tunnel-url https://controller.example.com`, wildcard DNS and HTTPS ingress. Public ingress preserves Host and forwards to the remote TCP listener (default 127.0.0.1:7463), never the privileged Unix socket. The suffix is reserved exclusively for tunnels.

For a local service, use Bridge setup directly against Hostlier's HTTP API:

```sh
fused-bridge tunnel 3000 --hostlier https://controller.example.com --name demo
```

Bridge handles email sign-in through Hostlier's existing HTTP API, saves a device-bound credential in its private per-controller profile, requests owner approval and starts forwarding in the same command. Hostlier email enrollment must be enabled and the mailbox authorized. Later runs can use `fused-bridge tunnel 3000 --name demo`; without `--name`, Bridge generates a stable name. An optional management credential in `HOSTLIER_TOKEN` or `--token-env NAME` overrides saved sign-in; app-deployment-only authority is insufficient. The local target and plaintext connector token stay in Bridge's private config, separate from the management credential. No Hostlier CLI subprocess or gRPC transport is needed. Do not print or commit either credential.

Resume pending setup with `fused-bridge tunnel setup --config FILE`. Bridge retains its credential and approval ID. `--new-approval` explicitly replaces a denied/expired review after inspection. Use `fused-bridge tunnel status|revoke --config FILE` for the same HTTP management lifecycle.

Hostlier's CLI remains connector-neutral: `hostlier tunnel list`, `hostlier tunnel status NAME`, `hostlier tunnel revoke NAME`, or `hostlier tunnel create NAME --bridge-id ID --from-env TOKEN_ENV [--for 24h --connections 4]`. Creation returns public lease metadata and never produces a Bridge config. `--target` and `--output` are rejected for tunnel creation. Remote mutations retain owner approval; `--socket PATH` selects protected local administration.

Lifetimes are 1 minute–720 hours, with no automatic renewal; reverse pools permit 1–16 connections. Revoke and recreate to rotate/extend. Names survive expiry and server restart. Bridge must keep running beside the app; HTTP, streaming HTTP/SSE and ordinary WebSocket upgrades use the existing dedicated TLS WebSocket transport. Raw TCP/UDP and native gRPC are unsupported.
