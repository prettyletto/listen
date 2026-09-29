# PDF fixtures

- `born-digital.pdf` is a three-page text-based book with two columns, repeated
  running headers, margin page numbers, typographic chapter headings, and
  document bookmarks. `born-digital.ps` is its readable Ghostscript source.
- `scanned.pdf` contains a page-sized raster image and no text layer.
  `scanned.ps` is its readable Ghostscript source.

`testdata/expected/pdf/born-digital.json` captures the normalized block order,
page provenance, and heading classification. Tests invoke Poppler when it is
installed; bookmark assertions also require qpdf.
