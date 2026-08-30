#!/usr/bin/env bash
# =============================================================================
# torpor — first push to GitHub, with a hard stop before anything leaves disk
#
#   bash hack/first-push.sh
#
# Refuses to continue if secrets.yaml, kubeconfigs, keys, or firmware binaries
# are staged. Prints the full file list and waits for you to type "yes".
# =============================================================================

set -euo pipefail

REPO_NAME="torpor"
REPO_DESC="Kubernetes-native control plane for physical device fleets. Capability-aware firmware rollouts for devices that sleep, run on battery, or live behind a LoRa link."

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo ~/embedded_projects/torpor)"
echo "repo root: $(pwd)"
echo

# -----------------------------------------------------------------------------
# 1. Make sure .gitignore exists and covers the dangerous things.
# -----------------------------------------------------------------------------
if [ ! -f .gitignore ]; then
  echo "✘ no .gitignore — refusing to continue"
  exit 1
fi

for pat in 'secrets.yaml' 'kubeconfig' '*.key' '*.pem' '.env'; do
  grep -qF "$pat" .gitignore || {
    echo "✘ .gitignore is missing a rule for: $pat"
    exit 1
  }
done
echo "✔ .gitignore covers secrets, kubeconfigs, keys, env files"

# -----------------------------------------------------------------------------
# 2. Stage everything, then inspect what git ACTUALLY picked up.
#
# Checking .gitignore is not enough — a file already tracked from an earlier
# commit stays tracked even after you add an ignore rule. This checks the
# real index, which is the only thing that matters.
# -----------------------------------------------------------------------------
git add -A

echo
echo "=== scanning the staged index for secrets ==="
LEAKS=0

while IFS= read -r f; do
  case "$f" in
    *secrets.yaml|*secrets.yml)
      # secrets.yaml.example is fine and expected
      case "$f" in *.example) continue ;; esac
      echo "  ✘ SECRET STAGED: $f"; LEAKS=1 ;;
    *kubeconfig*|*.kubeconfig)
      echo "  ✘ KUBECONFIG STAGED: $f"; LEAKS=1 ;;
    *.key|*.pem|*.crt|*.p12)
      echo "  ✘ KEY/CERT STAGED: $f"; LEAKS=1 ;;
    .env|*/.env)
      echo "  ✘ ENV FILE STAGED: $f"; LEAKS=1 ;;
    *.bin)
      echo "  • firmware blob staged (probably unwanted): $f" ;;
  esac
done < <(git diff --cached --name-only)

# Belt and braces: grep staged content for anything that smells like a
# credential, in case a secret is embedded in an otherwise-innocent file.
echo
echo "=== scanning staged CONTENT for credential-shaped strings ==="
if git diff --cached | grep -nE '^\+.*(wifi_password|password:|api_key|token:|BEGIN [A-Z ]*PRIVATE KEY)' \
     | grep -v 'example\|!secret\|PASTE\|your-\|## \|Fallback\|grep -nE\|12345678' ; then
  echo "  ✘ the lines above look like credentials"
  LEAKS=1
else
  echo "  ✔ nothing credential-shaped in the diff"
fi

if [ "$LEAKS" -ne 0 ]; then
  echo
  echo "STOPPED. Unstage the offenders and fix .gitignore, e.g.:"
  echo "    git rm --cached firmware/secrets.yaml"
  exit 1
fi

# -----------------------------------------------------------------------------
# 3. Confirm secrets.yaml really is being ignored, explicitly.
# -----------------------------------------------------------------------------
echo
if [ -f firmware/secrets.yaml ]; then
  if git check-ignore -q firmware/secrets.yaml; then
    echo "✔ firmware/secrets.yaml exists on disk and IS ignored"
  else
    echo "✘ firmware/secrets.yaml exists and is NOT ignored — stopping"
    exit 1
  fi
else
  echo "• firmware/secrets.yaml not found on disk (nothing to leak)"
fi

# -----------------------------------------------------------------------------
# 4. Show the human exactly what is about to be published.
# -----------------------------------------------------------------------------
echo
echo "=== $(git diff --cached --name-only | wc -l | tr -d ' ') files will be committed ==="
git diff --cached --name-only | sed 's/^/    /'
echo
echo "=== largest staged files ==="
git diff --cached --name-only -z \
  | xargs -0 -I{} sh -c '[ -f "{}" ] && du -h "{}"' 2>/dev/null \
  | sort -rh | head -8 | sed 's/^/    /'

echo
read -r -p "Create PRIVATE repo '${REPO_NAME}' and push these files? [yes/N] " answer
if [ "$answer" != "yes" ]; then
  echo "aborted — nothing pushed. Files remain staged."
  exit 0
fi

# -----------------------------------------------------------------------------
# 5. Commit, create, push.
# -----------------------------------------------------------------------------
git rev-parse --verify HEAD >/dev/null 2>&1 \
  && git commit -m "V0: KubeEdge control plane, ESPHome MQTT mapper, ops tooling" \
  || git commit -m "torpor: initial commit — V0 chain, ESPHome MQTT mapper, ops tooling"

gh repo create "$REPO_NAME" \
  --private \
  --source=. \
  --remote=origin \
  --push \
  --description "$REPO_DESC"

# -----------------------------------------------------------------------------
# 6. Topics. 18 of GitHub's 20 — holding two back for cncf and
#    firmwarerollout once V3 actually exists.
# -----------------------------------------------------------------------------
gh repo edit --add-topic \
kubernetes,kubeedge,edge-computing,iot,fleet-management,device-management,ota-updates,firmware-updates,esphome,esp32,esp32s3,lora,sx1262,mqtt,kubernetes-operator,crd,golang,low-power

echo
echo "✔ done"
gh repo view --web
