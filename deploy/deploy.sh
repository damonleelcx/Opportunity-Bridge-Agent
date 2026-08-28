#!/usr/bin/env bash
# Ship the image and the manifests to the k3s node and apply them.
#
# Both artefacts travel through S3 rather than through the SSM command payload.
# There is no registry here, and more to the point SSM's parameter encoding
# mangles anything with newlines in it — a YAML document arrives as one line and
# kubectl rejects it. S3 carries bytes; SSM carries only the instruction to fetch
# them.
#
# Every step is idempotent: running this twice lands the same state.
set -euo pipefail
cd "$(dirname "$0")/.."

# Account-specific identifiers live in deploy/deploy.env, which is gitignored:
# this repository is public, and naming an account, an instance and a bucket in
# it only narrows the work for somebody probing them.
if [ -f deploy/deploy.env ]; then
  set -a; . deploy/deploy.env; set +a
fi
: "${INSTANCE:?set INSTANCE in deploy/deploy.env (see deploy.env.example)}"
: "${BUCKET:?set BUCKET in deploy/deploy.env (see deploy.env.example)}"
REGION="${REGION:-us-east-1}"
NS="${NS:-opportunity-bridge}"
TAG="${1:-$(cat dist/TAG)}"
TARBALL="opportunity-bridge-${TAG}.tar.gz"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

# run_on_node executes a shell script on the node over SSM and streams back its
# output. The script is passed as a file so nothing has to survive shell quoting.
run_on_node() {
  local script="$1" cmd_id status params
  params=$(python3 -c 'import json,sys; print(json.dumps({"commands":[open(sys.argv[1]).read()]}))' "$script")
  cmd_id=$(aws ssm send-command --region "$REGION" --instance-ids "$INSTANCE" \
    --document-name AWS-RunShellScript --parameters "$params" \
    --query 'Command.CommandId' --output text)
  for _ in $(seq 1 180); do
    status=$(aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
      --instance-id "$INSTANCE" --query 'Status' --output text 2>/dev/null || echo Pending)
    case "$status" in Success|Failed|Cancelled|TimedOut) break ;; esac
    sleep 5
  done
  aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
    --instance-id "$INSTANCE" --query 'StandardOutputContent' --output text
  if [ "$status" != "Success" ]; then
    aws ssm get-command-invocation --region "$REGION" --command-id "$cmd_id" \
      --instance-id "$INSTANCE" --query 'StandardErrorContent' --output text >&2
    echo "node command failed: $status" >&2
    return 1
  fi
}

say "rendering manifests (image tag ${TAG})"
mkdir -p dist
: > dist/manifests.yaml
for f in deploy/k8s/*.yaml; do
  printf -- '---\n' >> dist/manifests.yaml
  sed "s|IMAGE_TAG|${TAG}|g" "$f" >> dist/manifests.yaml
done
grep -c '^---' dist/manifests.yaml | sed 's/^/    documents: /'

say "staging artefacts in s3://${BUCKET}"
aws s3 cp "dist/${TARBALL}" "s3://${BUCKET}/images/${TARBALL}" --region "$REGION" --only-show-errors
aws s3 cp dist/manifests.yaml "s3://${BUCKET}/manifests/opportunity-bridge-${TAG}.yaml" \
  --region "$REGION" --only-show-errors

say "importing the image and applying manifests on ${INSTANCE}"
cat > dist/node-step.sh <<NODE
set -euo pipefail
cd /tmp
echo "--- importing image ---"
aws s3 cp s3://${BUCKET}/images/${TARBALL} ${TARBALL} --region ${REGION} --only-show-errors
gunzip -f -c ${TARBALL} | sudo k3s ctr -n k8s.io images import -
rm -f ${TARBALL}
sudo k3s ctr -n k8s.io images ls -q | grep opportunity-bridge || true
echo "--- applying manifests ---"
aws s3 cp s3://${BUCKET}/manifests/opportunity-bridge-${TAG}.yaml oba.yaml --region ${REGION} --only-show-errors
sudo k3s kubectl apply -f oba.yaml
rm -f oba.yaml
echo "--- rollout ---"
sudo k3s kubectl -n ${NS} rollout status deploy/opportunity-bridge --timeout=240s
sudo k3s kubectl -n ${NS} get pods,svc,ingress,externalsecret
NODE
run_on_node dist/node-step.sh
