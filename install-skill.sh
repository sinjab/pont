#!/bin/sh
set -e

REPO="sinjab/pont"
SKILL_URL="https://raw.githubusercontent.com/${REPO}/main/SKILL.md"

echo "Installing pont agent skill..."
echo ""

# Install for Claude Code
CLAUDE_DIR="${HOME}/.claude/skills/whatsapp"
mkdir -p "$CLAUDE_DIR"
curl -fsSL "$SKILL_URL" -o "${CLAUDE_DIR}/SKILL.md"
echo "  Installed: ${CLAUDE_DIR}/SKILL.md"

# Install for Cursor
CURSOR_DIR="${HOME}/.cursor/skills/whatsapp"
mkdir -p "$CURSOR_DIR"
curl -fsSL "$SKILL_URL" -o "${CURSOR_DIR}/SKILL.md"
echo "  Installed: ${CURSOR_DIR}/SKILL.md"

echo ""
echo "Agent skill installed for:"
echo "  - Claude Code"
echo "  - Cursor"
echo ""
echo "(Re-run this script to update)"
echo ""
echo "The skill will be auto-detected when you ask about WhatsApp messages."
echo ""
echo "Prerequisites:"
echo "  - pont must be installed (brew install sinjab/tap/pont)"
echo "  - Run 'pont auth login' to authenticate (a whatsapp symlink is also installed)"
