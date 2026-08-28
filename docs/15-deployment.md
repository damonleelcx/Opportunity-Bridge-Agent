# 15. Deployment

**Live at [https://jobs.heros-agent.space](https://jobs.heros-agent.space)** —
k3s on a single t4g.large (arm64) EC2 host in us-east-1, sharing the box with
another stack in its own namespace. The host, region and staging bucket are set
in `deploy/deploy.env` (gitignored — see `deploy.env.example`).

```bash
./deploy/build.sh      # compile linux/arm64, build the image, export a tarball
./deploy/deploy.sh     # stage to S3, import into containerd, apply, wait for rollout
```

Both are idempotent. Running them twice lands the same state.

## Shape

```
Route53  jobs.heros-agent.space  A → <node public IP>
   │
Traefik (websecure) ── cert-manager / letsencrypt-prod ── rate-limit middleware
   │                   (a second ingress on :80 redirects to https)
Service :8787 → Deployment (1 replica) → PVC (local-path, 1Gi) for session state
                     ▲
              ConfigMap (every knob) + Secret ← ExternalSecret ← AWS Secrets Manager
```

## Four decisions worth the words

**The image carries a prebuilt binary.** Compiling inside the image is more
self-contained on paper, but it puts a module download on the critical path of
every deploy — and the build VM here has no outbound network, so it simply
fails. The Go toolchain already emits a static arm64 binary; the image adds a
filesystem and a non-root user and nothing else. Result: distroless, ~20MB,
4.5MB compressed, and the image build takes under a second.

**Artefacts travel through S3, not through SSM.** There is no registry on this
box. ECR was the obvious alternative and the node already has
`AmazonEC2ContainerRegistryReadOnly` — but k3s does not use the instance role for
registry auth, so ECR would have meant an imagePullSecret refreshed every twelve
hours. S3 plus `ctr images import` is one moving part fewer. And manifests go the
same way because SSM's parameter encoding mangles newlines: a YAML document
arrives as a single line and `kubectl` rejects it.

**The API key is never in a manifest, a command line, or an SSM invocation.** It
lives in AWS Secrets Manager and is pulled into the cluster by the External
Secrets Operator already running on this node, using the node's own IAM role —
the same mechanism the heros namespace uses. Rotating it is one API call; the
operator re-syncs within the hour and the pod picks it up on its next roll.

**One replica, deliberately.** Session state is a JSON snapshot on a local
volume, so a second replica would split conversations at random depending on
which pod a request landed on. Making this horizontally scalable means moving
state out first. A replica count that looks scalable while silently losing
people's context is worse than a comment saying why it is 1.

## IAM added

One inline policy on the node's existing instance role, rendered from
`deploy/node-iam-policy.json.tmpl`, scoped to exactly two things:

- `s3:GetObject` on `<bucket>/images/*` and `<bucket>/manifests/*`
- Secrets Manager read on `opportunity-bridge/*` only

Nothing wildcard, and nothing that widens the node's access to whatever else
shares the box.

## ⚠️ The endpoint is public and unauthenticated

Anyone with the URL can hold a conversation, and every turn spends DeepSeek
tokens. What is in place:

- A Traefik rate limit (30 requests/minute per source IP, burst 60).
- Tighter budgets than local: 6 iterations, 12 tool calls, 150s wall clock per
  turn.
- No real personal data anywhere — the corpus is sample data, and the only thing
  stored is what a visitor types.

What is **not** in place is authentication. The exposure is a token bill, not a
data breach, but it is unbounded above the rate limit. Options, cheapest first:

1. Traefik basic-auth middleware in front of the ingress — five lines, no
   application change, and the obvious choice for a demo link shared with named
   people.
2. A shared access code checked by the app, so the URL alone is not enough.
3. Real accounts, which the product will need anyway before it holds anything
   about a real person.

Say which and it is a short change.

## Operating it

```bash
# on the node, via SSM
sudo k3s kubectl -n opportunity-bridge get pods,ingress,externalsecret
sudo k3s kubectl -n opportunity-bridge logs deploy/opportunity-bridge --tail=100
sudo k3s kubectl -n opportunity-bridge rollout restart deploy/opportunity-bridge
```

Rolling back is `./deploy/deploy.sh <older-tag>` — the tarballs stay in the
staging bucket under `images/`.
