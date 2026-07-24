# runx-leak-scan: allow-file internal_ip
# Helixon k3s Cluster Diagram (v14563)

```mermaid
graph TD
    subgraph control["Control Plane"]
        wsl1[desktop-12ro1af<br/>wsl1 — primary<br/>2x RTX 3090 + RTX 2070<br/>engramd, sprintboard,<br/>llm-router, svcregistryd]
    end

    subgraph agents["Fleet Agents"]
        wsl2[desktop-0s5prj9<br/>wsl2 — secondary<br/>RTX 4070 Ti<br/>role: fleet-agent]
        wsl3[wsl3<br/>wsl3 — primary<br/>RTX 5070 Ti 12GB<br/>role: dev-planning]
        wsl4[desktop-p5bul0f<br/>wsl4 — primary<br/>RTX 3090<br/>role: llm-node]
    end

    wsl1 -.Tailscale.-> wsl2
    wsl1 -.Tailscale.-> wsl3
    wsl1 -.Tailscale.-> wsl4
    wsl2 -.Tailscale.-> wsl3
    wsl2 -.Tailscale.-> wsl4
    wsl3 -.Tailscale.-> wsl4

    wsl1 ==> wsl2
    wsl1 ==> wsl3
    wsl1 ==> wsl4
```

## Node list

| Hostname | Role | Tier | GPU | Internal IP | Tailscale IP |
|----------|------|------|-----|-------------|--------------|
| desktop-12ro1af (wsl1) | control-plane | primary | (none) | 172.29.144.56 | 100.84.108.92 |
| desktop-0s5prj9 (wsl2) | fleet-agent   | secondary | (none) | 192.168.4.64  | 100.110.82.5 |
| desktop-p5bul0f (wsl4) | llm-node      | primary | rtx3090 | 100.79.227.40 | 100.79.227.40 |
| wsl3                | dev-planning  | primary | rtx5070ti | 172.20.113.115 | 100.73.98.10  |

## Connectivity (verified via Tailscale)
- All 4 nodes are online and reachable
- All 4 nodes have helm-style labels for `helixon.io/*`
- wsl3 newly joined in v14563 (4m runtime)
