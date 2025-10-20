#!/bin/bash
# Script to check for qt testing anti-patterns in Go test files

set -e

echo "🔍 Checking for qt testing anti-patterns..."

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

ERRORS=0

# Check for len() + qt.Equals patterns
echo "Checking for len() + qt.Equals anti-patterns..."
len_equals_files=$(find . -name "*_test.go" -exec grep -l "c\.Assert.*len(.*qt\.Equals" {} \; 2>/dev/null || true)
if [ ! -z "$len_equals_files" ]; then
    echo -e "${RED}❌ Found len() + qt.Equals anti-patterns in:${NC}"
    for file in $len_equals_files; do
        echo -e "  ${file}:"
        grep -n "c\.Assert.*len.*qt\.Equals" "$file" | sed 's/^/    /'
    done
    echo -e "${YELLOW}   💡 Use qt.HasLen instead: c.Assert(slice, qt.HasLen, N)${NC}"
    ERRORS=$((ERRORS + 1))
fi

# Check for == nil + qt.Equals patterns
echo "Checking for == nil + qt.Equals anti-patterns..."
nil_equals_files=$(find . -name "*_test.go" -exec grep -l "c\.Assert.*== nil.*qt\.Equals" {} \; 2>/dev/null || true)
if [ ! -z "$nil_equals_files" ]; then
    echo -e "${RED}❌ Found == nil + qt.Equals anti-patterns in:${NC}"
    for file in $nil_equals_files; do
        echo -e "  ${file}:"
        grep -n "c\.Assert.*== nil.*qt\.Equals" "$file" | sed 's/^/    /'
    done
    echo -e "${YELLOW}   💡 Use qt.IsNil instead: c.Assert(value, qt.IsNil)${NC}"
    ERRORS=$((ERRORS + 1))
fi

# Check for boolean + qt.Equals patterns  
echo "Checking for boolean + qt.Equals anti-patterns..."
bool_equals_files=$(find . -name "*_test.go" -exec grep -l "c\.Assert.*qt\.Equals.*true\|c\.Assert.*qt\.Equals.*false" {} \; 2>/dev/null || true)
if [ ! -z "$bool_equals_files" ]; then
    echo -e "${RED}❌ Found boolean + qt.Equals anti-patterns in:${NC}"
    for file in $bool_equals_files; do
        echo -e "  ${file}:"
        grep -n "c\.Assert.*qt\.Equals.*true\|c\.Assert.*qt\.Equals.*false" "$file" | sed 's/^/    /'
    done
    echo -e "${YELLOW}   💡 Use qt.IsTrue/qt.IsFalse instead: c.Assert(value, qt.IsTrue)${NC}"
    ERRORS=$((ERRORS + 1))
fi

# Summary
if [ $ERRORS -eq 0 ]; then
    echo -e "${GREEN}✅ No qt anti-patterns found!${NC}"
    exit 0
else
    echo -e "${RED}❌ Found $ERRORS types of qt anti-patterns${NC}"
    echo -e "${YELLOW}📖 See .clinerules for complete guidelines${NC}"
    exit 1
fi
