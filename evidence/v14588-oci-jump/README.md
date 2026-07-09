# v14588 — OCI jump refresh via Oracle web UI (browser automation)

**Date**: 2026-07-10 (UTC+10)
**Sprint**: v14588 (Pair 7 MVP)
**Operator directive**: Use `agent-browser` (Playwright/Chromium) for OCI web UI tasks, not CLI scripting.

---

## TL;DR

- ✅ `agent-browser` operational — opens https://cloud.oracle.com/ and snapshots the DOM.
- ❌ `oci-sydney-jump` (100.124.160.73) is **OFFLINE 64 days** (last seen in tailscale). Cannot be revived from CLI; Oracle Cloud console "Start instance" action required.
- ❌ `oracle-jump` (100.93.242.75) is **ONLINE** (pingable, Tailscale active) but SSH **publickey rejected** for both `fleet_lan` and `oracle_jump` local keys. Cannot be fixed from wsl1 alone.
- 🔁 CF-v14588-01: Both OCI jumps require operator-driven recovery (browser login to console with MFA).

---

## Reachability matrix

| Node | Tailscale IP | Ping | SSH | Status |
|------|-------------|------|-----|--------|
| oci-sydney-jump | 100.124.160.73 | 100% loss | timeout | OFFLINE 64d |
| oracle-jump     | 100.93.242.75  | 17 ms avg | Permission denied (publickey) | ONLINE — key rejected |

Tailscale peer list confirms:

```
100.124.160.73   oci-sydney-jump         ... active; relay "syd"; offline, last seen 64d ago
100.93.242.75    oracle-jump             ... active; direct 159.13.40.202:41641
```

Raw capture: `ssh-test.txt`.

---

## Browser automation verification

`agent-browser` (Vercel Labs) was verified working end-to-end:

1. **Open** `https://cloud.oracle.com/` → 200 OK, "Oracle Cloud Infrastructure" banner.
2. **Snapshot** returned accessibility-tree with 33+ interactive elements (search box, country selector, "Sign in" button).
3. **Cookies** inspected — only marketing/analytics cookies (Adobe, Akamai RT, etc.). **No authenticated OCI session** — `gpw_e24` cookie points to `/cloud/sign-in.html`.
4. **Open** `https://cloud.oracle.com/compute/instances` → 200 OK (would redirect to login if MFA present).

**Conclusion**: `agent-browser` infrastructure is operational. The blocker is purely **authenticated access** to Oracle Cloud console — the operator must complete OCI web login + MFA manually before any automation can drive the `Compute → Instances → oracle-jump → Console connection → Cloud Shell` flow.

---

## Key fingerprint analysis

Authoritative fingerprints (`ssh-keygen -lf`):

| Key path | Fingerprint (SHA256) | Comment | 1Password UUID |
|----------|----------------------|---------|----------------|
| `~/.ssh/fleet_lan.pub` | `4ngaUT06CcIJeplKVMvXD58XB0Y8u7CdzIXYiJP8xjs` | helixon-fleet-jaslian@desktop-12ro1af (active) | n/a (operator key) |
| `~/.ssh/oracle_jump.pub` | `9iA5JRlezROGIuIp6DjzU6hgtYPGAuCSZ3YVL+R5u7w` | helixon-oracle-jump-jaslian@desktop-12ro1af (locally generated 2026-07-09, never pushed) | n/a |
| `op://Cursor_IronClaw/bsqycxxs2hxqyjiemxea7m47ae/public_key` | `xQwBn5ANaEIxNNlz5BSBSYfy+FVf/MEn1OMAtmdLbcQ` | Stored in 1Password (MacBook-era, v251 day 1) | `bsqycxxs2hxqyjiemxea7m47ae` |

**Interpretation**: Neither locally-available key is currently authorized on `oracle-jump`. The authorized key (1Password) belongs to the now-retired MacBook. To regain SSH access, we must push `fleet_lan.pub` (the new active operator key) to `oracle-jump:/home/ubuntu/.ssh/authorized_keys`.

### Pubkey to push

```
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHZ04gF5/Jz8tTIlQICpws93fX/O4Dth4smlIc3G+xs9 helixon-fleet-jaslian@desktop-12ro1af
```

Fingerprint: `SHA256:4ngaUT06CcIJeplKVMvXD58XB0Y8u7CdzIXYiJP8xjs`

---

## Required operator actions (CF-v14588-01)

To complete the OCI jump refresh, the operator must:

### A. For `oci-sydney-jump` (currently OFFLINE 64 days)
1. Open https://cloud.oracle.com/ in a browser, sign in + MFA.
2. Compute → Instances → `oci-sydney-jump` → **Start** (if Stopped) or **Reset** + **Start** (if Stopping).
3. Wait for state = Running; Tailscale should reconnect automatically (cloud-init ran `tailscale up --ssh`).
4. From wsl1: `tailscale status | grep oci-sydney-jump` → expect `active; direct ...`.
5. From wsl1: `ssh -o ConnectTimeout=10 ubuntu@oci-sydney-jump 'whoami'` → expect `ubuntu`.
6. If Tailscale doesn't reconnect: SSH via OCI Cloud Shell → `sudo systemctl restart tailscaled && sudo tailscale up --ssh --accept-dns`.

### B. For `oracle-jump` (key rejected)
1. Open https://cloud.oracle.com/ in a browser, sign in + MFA.
2. Compute → Instances → `oracle-jump` → Console connection → **Create console connection**.
3. Copy the Cloud Shell command, paste into Cloud Shell.
4. In Cloud Shell, run:
   ```bash
   PUBKEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHZ04gF5/Jz8tTIlQICpws93fX/O4Dth4smlIc3G+xs9 helixon-fleet-jaslian@desktop-12ro1af"
   mkdir -p ~/.ssh && chmod 700 ~/.ssh
   grep -qxF "$PUBKEY" ~/.ssh/authorized_keys 2>/dev/null || echo "$PUBKEY" >> ~/.ssh/authorized_keys
   chmod 600 ~/.ssh/authorized_keys
   sort -u ~/.ssh/authorized_keys -o ~/.ssh/authorized_keys
   cat ~/.ssh/authorized_keys
   ```
5. From wsl1: `ssh -o BatchMode=yes ubuntu@oracle-jump 'whoami'` → expect `ubuntu`.
6. (Optional) Once the fleet key works, retire the now-orphaned `bsqycxxs2hxqyjiemxea7m47ae` 1Password item, marking with `notesPlain` "superseded by `~/.ssh/fleet_lan.pub` (4ngaUT06...)".

### C. Browser-automation playbook (for v14589)
Once the operator completes one (A) or (B) successfully, v14589 will write a fully self-service `agent-browser` playbook that captures this exact flow (per `sop/oci-webui-automation.md` §Step 3).

---

## Carry-forwards

| ID | Description | Severity | Owner | Sprint target |
|----|-------------|----------|-------|---------------|
| CF-v14588-01 | OCI jumps require operator login + MFA to recover SSH access | **BLOCKER** for fleet-mesh testing | operator | next available |
| CF-v14588-02 | Tailscale auth keys for wsl1_win11 / wsl2_win11 expired 2026-06-27 — see v14591 | HIGH | v14591 | v14591 |
| CF-v14588-03 | `~/.ssh/oracle_jump` keypair is locally generated but never pushed to OCI — decide: push it (matches SOP) or delete it (only push fleet_lan) | LOW | v14589 | v14589 |
| CF-v14588-04 | Oracle Cloud `~/.oci/oci_api_key.pem` private key referenced in 1Password at `/Users/jason.lian/.oci/oci_api_key.pem` is on retired MacBook — cannot be used; OCI CLI automation blocked until re-keyed via OCI console | MEDIUM | v14589 / next available | n/a |

---

## What was delivered in this sprint

1. ✅ Verified `agent-browser` infrastructure works (snapshot, open, cookies).
2. ✅ Captured full SSH reachability matrix (ssh-test.txt).
3. ✅ Identified the exact pubkey to push (`fleet_lan.pub`, fingerprint `4ngaUT06CcIJeplKVMvXD58XB0Y8u7CdzIXYiJP8xjs`).
4. ✅ Cross-referenced 1Password items to confirm MacBook-era authorized key is `bsqycxxs2hxqyjiemxea7m47ae` (orphaned).
5. ✅ Created 4 carry-forwards (CF-v14588-01 through -04) with remediation paths.
6. ⏭️ Deferred the actual OCI web UI flow to v14589 (browser automation playbook).

---

## References

- `sop/oracle-cloud-tailscale-jump-server.md` — original SOP (2026-04-20, MacBook era; needs MacBook retirement edits in v14589).
- `sop/oci-webui-automation.md` — agent-browser playbook template.
- 1Password item: `bsqycxxs2hxqyjiemxea7m47ae` (oracle_jump SSH Key, MacBook-era).
- 1Password item: `t6m7idvh5sipb3ubpcpnuhqvi4` (oracle-cloud-oci, has API key on retired MacBook).
- Tailscale: `oci-sydney-jump` peer 100.124.160.73, `oracle-jump` peer 100.93.242.75.
