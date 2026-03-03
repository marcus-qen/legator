#!/usr/bin/env bash
# Pre-commit hook: scan staged files for secret patterns
# Install: cd .git/hooks && ln -sf ../../hooks/pre-commit-secret-scan.sh pre-commit

set -euo pipefail

PATTERNS_FILE="$(git rev-parse --show-toplevel)/.secret-patterns"

if [ ! -f "$PATTERNS_FILE" ]; then
    echo "⚠️  No .secret-patterns file found — skipping secret scan"
    exit 0
fi

echo "🔒 Scanning staged files for secrets..."

FOUND=0
while IFS= read -r pattern; do
    [[ -z "$pattern" || "$pattern" =~ ^# ]] && continue
    
    while IFS= read -r match; do
        if [ -n "$match" ]; then
            FILE=$(echo "$match" | cut -d: -f1)
            # Skip the patterns file itself and the hook
            [[ "$FILE" == ".secret-patterns" || "$FILE" == hooks/* ]] && continue
            echo ""
            echo "🚨 SECRET DETECTED in $match"
            echo "   Pattern: $pattern"
            FOUND=1
        fi
    done < <(git diff --cached --name-only -z | xargs -0 -I{} git show :"$1{}" 2>/dev/null | grep -nP "$pattern" 2>/dev/null | head -5 | sed "s|^|{}:|" || true) 2>/dev/null || \
    # Fallback: check staged content via diff
    while IFS= read -r match; do
        if [ -n "$match" ]; then
            echo ""
            echo "🚨 SECRET DETECTED in $match"
            echo "   Pattern: $pattern"
            FOUND=1
        fi
    done < <(git diff --cached -U0 | grep -P "^\+" | grep -vP "^\+\+\+" | grep -nP "$pattern" 2>/dev/null | head -5 || true)
    
done < "$PATTERNS_FILE"

if [ "$FOUND" -eq 1 ]; then
    echo ""
    echo "================================================================"
    echo "❌ COMMIT BLOCKED — secrets detected in staged files."
    echo ""
    echo "   Secrets belong in Vault, not in git."
    echo "   Replace with REDACTED_SEE_VAULT or remove the value."
    echo ""
    echo "   To bypass (EMERGENCY ONLY): git commit --no-verify"
    echo "================================================================"
    exit 1
fi

echo "✅ No secrets found in staged files."
