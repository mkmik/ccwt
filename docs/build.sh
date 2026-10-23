#!/bin/sh
# Builds the book into docs/book/, with ccwt's own todography map at
# docs/book/map/index.html, as it's published on GitHub Pages.
#
# Needs mdbook 0.4 and todography on the PATH. mdbook empties docs/book/
# first, so the map goes in after it.
set -eu
cd "$(dirname "$0")/.."

mdbook build docs

# The build time the page records: the last commit's, so rebuilding the
# same commit gives the same file.
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct 2>/dev/null || date +%s)
export SOURCE_DATE_EPOCH
todography build --out docs/book/map/index.html
