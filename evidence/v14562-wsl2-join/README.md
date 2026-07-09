# Sprint v14562 — wsl2 k3s agent join + label verification

## Summary
Confirmed wsl2 (desktop-0s5prj9) is already joined to the k3s
control plane on wsl1 (joined earlier in v14554). All required
Helixon labels applied (per `01-fleet-doctor-cadence.mdc`).

## Node list (3-node cluster)

| Hostname           | Role          | Alias | Tier      | GPU      | Status |
|--------------------|---------------|-------|-----------|----------|--------|
| desktop-12ro1af    | control-plane | wsl1  | primary   | (none)   | Ready  |
| desktop-0s5prj9    | fleet-agent   | wsl2  | secondary | (none)   | Ready  |
| desktop-p5bul0f    | llm-node      | wsl4  | primary   | rtx3090  | Ready  |

## Label application commands
```
kubectl label node desktop-12ro1af helixon.io/role=control-plane --overwrite
kubectl label node desktop-12ro1af helixon.io/tier=primary --overwrite
kubectl label node desktop-12ro1af helixon.io/node-alias=wsl1 --overwrite

kubectl label node desktop-0s5prj9 helixon.io/role=fleet-agent --overwrite
kubectl label node desktop-0s5prj9 helixon.io/tier=secondary --overwrite
kubectl label node desktop-0s5prj9 helixon.io/node-alias=wsl2 --overwrite

kubectl label node desktop-p5bul0f helixon.io/role=llm-node --overwrite
kubectl label node desktop-p5bul0f helixon.io/tier=primary --overwrite
kubectl label node desktop-p5bul0f helixon.io/node-alias=wsl4 --overwrite
kubectl label node desktop-p5bul0f helixon.io/gpu=rtx3090 --overwrite
```

## Per-spec check (per plan)
- wsl2 `helixon.io/role=fleet-agent` ✓
- wsl2 `helixon.io/tier=secondary` ✓

## Artefacts
- `label-application.txt` — full label cmd output + final `kubectl get nodes --show-labels`
- `node-labels.txt` — pre-fix labels
- `README.md` — this file

## Verification
- 3/3 nodes Ready
- 3/3 nodes labelled with helixon.io/* keys
- wsl2 matches the role/tier spec for "secondary fleet agent"
