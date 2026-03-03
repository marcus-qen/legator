#!/usr/bin/env bash
# Pre-commit hook: scan staged files for secret patterns
# Install: cp hooks/pre-commit-secret-scan.sh .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit
# Or: cd .git/hooks && ln -sf ../../hooks/pre-commit-secret-scan.sh pre-commit

set -euo pipefail

PATTERNS_FILE="$(git rev-parse --show-toplevel)/.secret-patterns"
FOUND=0

if [ ! -f "$PATTERNS_FILE" ]; then
    echo "WARNING: .secret-patterns file not found — skipping secret scan"
    exit 0
fi

echo "🔒 Scanning staged files for secrets..."

# Get list of staged files (added/modified)
STAGED=$(git diff --cached --name-only --diff-filter=ACM 2>/dev/null || true)

if [ -z "$STAGED" ]; then
    exit 0
fi

while IFS= read -r pattern; do
    # Skip comments and empty lines
    [[ -z "$pattern" || "$pattern" =~ ^# ]] && continue

    for file in $STAGED; do
        # Skip binary files and the patterns file itself
        [[ "$file" == ".secret-patterns" ]] && continue
        [[ "$file" == "hooks/"* ]] && continue
        [[ "$file" == "*.db" ]] && continue

        if [ -f "$file" ]; then
            MATCHES=$(grep -nP "$pattern" "$file" 2>/dev/null || true)
            if [ -n "$MATCHES" ]; then
                echo ""
                echo "🚨 SECRET DETECTED in $file:"
                echo "$MATCHES" | head -5
                echo "   Pattern: $pattern"
                FOUND=1
            fi
        fi
    done
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
exit 0
