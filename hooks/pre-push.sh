#!/usr/bin/env bash
# Pre-push hook for Go repos: run vet + tests before push
# Install: cd .git/hooks && ln -sf ../../hooks/pre-push pre-push
set -euo pipefail

# Only run for Go repos
if [ ! -f "go.mod" ]; then
    exit 0
fi

echo "🔍 Running go vet..."
if ! go vet ./... 2>&1; then
    echo ""
    echo "❌ PUSH BLOCKED — go vet found issues."
    echo "   Bypass (EMERGENCY ONLY): git push --no-verify"
    exit 1
fi

echo "🧪 Running go test..."
if ! go test ./... -count=1 -timeout 120s 2>&1 | tail -20; then
    echo ""
    echo "❌ PUSH BLOCKED — tests failed."
    echo "   Bypass (EMERGENCY ONLY): git push --no-verify"
    exit 1
fi

echo "✅ All pre-push checks passed."
