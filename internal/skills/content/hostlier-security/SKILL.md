---
name: hostlier-security
description: Bootstrap or revoke Hostlier human approval keys and inspect approval authority.
---

# Hostlier security

`hostlier security status` inspects authority. On the human trusted machine, `hostlier security keygen --key FILE` generates an OpenSSH ECDSA FIDO key with PIN verification and touch. An administrator copies only its public file and runs `hostlier security enroll --name OWNER --public-key FILE.pub --socket PATH` on the protected controller. Revoke with `security revoke --fingerprint SHA256:... --socket PATH`. Remote API credentials cannot enroll or recover keys. Do not request private key handles, PINs or root access for an agent. Public-key enrollment trusts the administrator; no manufacturer attestation is verified.
