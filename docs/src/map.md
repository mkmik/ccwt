# The map

ccwt keeps a map of itself: what it is made of, how the pieces depend on each other, and how
far along each one is. It is a [todography](https://mkmik.github.io/todography/) map, kept as
Markdown files under `docs/map/` next to the code.

**[Open the map](map/index.html)**: it is published next to this book, and rebuilt from the
same commit.

The map is also where [`T`](tui/queue.md#prompts-from-todography) in the tui and in
`ccwt ws` gets its work from: `todography next` lists the wanted work whose needs are met.

## How the book and the map are built

Both live under `docs/`, and neither reads the other's files:

| Path | What it is |
| --- | --- |
| `docs/src/` | this book's pages |
| `docs/theme/` | the book's colours and icon |
| `docs/map/` | the map's components |
| `docs/book/` | the built site, not checked in |

`docs/build.sh` builds both: first `mdbook build docs`, which empties `docs/book/` and writes
the book there, then `todography build`, which writes the map's page to
`docs/book/map/index.html`. That is the folder GitHub Pages publishes, so the book is at the
top of the site and the map at `map/`.

`mdbook serve docs` is quicker while writing the book, but it shows the book alone: it empties
`docs/book/` on every change, map included.

`todography lint` checks the map. The docs workflow runs it before building, so a broken map
fails the pull request.
