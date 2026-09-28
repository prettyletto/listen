from __future__ import annotations

from pathlib import Path
from zipfile import ZIP_DEFLATED, ZIP_STORED, ZipFile

ROOT = Path(__file__).resolve().parents[1] / "testdata"

CONTAINER = '''<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>
'''


def write_epub(name: str, files: dict[str, str]) -> None:
    out = ROOT / name
    with ZipFile(out, "w") as z:
        z.writestr("mimetype", "application/epub+zip", compress_type=ZIP_STORED)
        z.writestr("META-INF/container.xml", CONTAINER, compress_type=ZIP_DEFLATED)
        for path, text in files.items():
            z.writestr(path, text, compress_type=ZIP_DEFLATED)


def xhtml(title: str, body: str) -> str:
    return f'''<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>{title}</title></head><body>{body}</body></html>'''


# EPUB 3: nested nav + fragment subsections + front/back matter.
files = {
"OEBPS/package.opf": '''<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
 <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">nested</dc:identifier><dc:title>Nested Book</dc:title><dc:language>en</dc:language></metadata>
 <manifest>
  <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  <item id="preface" href="preface.xhtml" media-type="application/xhtml+xml"/>
  <item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/>
  <item id="c2" href="chapter2.xhtml" media-type="application/xhtml+xml"/>
  <item id="app" href="appendix.xhtml" media-type="application/xhtml+xml"/>
 </manifest>
 <spine><itemref idref="nav"/><itemref idref="preface"/><itemref idref="c1"/><itemref idref="c2"/><itemref idref="app"/></spine>
</package>''',
"OEBPS/nav.xhtml": xhtml("Contents", '''<nav epub:type="toc"><h1>Contents</h1><ol>
<li><a href="preface.xhtml#pref">Preface</a></li>
<li><span>Part I</span><ol>
 <li><a href="chapter1.xhtml#c1">Chapter 1</a><ol><li><a href="chapter1.xhtml#s1">Section 1.1</a></li><li><a href="chapter1.xhtml#s2">Section 1.2</a></li></ol></li>
 <li><a href="chapter2.xhtml#c2">Chapter 2</a></li>
</ol></li>
<li><a href="appendix.xhtml#app">Appendix A</a></li>
</ol></nav>'''),
"OEBPS/preface.xhtml": xhtml("Preface", '<section epub:type="preface"><h1 id="pref">Preface</h1><p>Preface body.</p></section>'),
"OEBPS/chapter1.xhtml": xhtml("Chapter 1", '<section epub:type="chapter"><h1 id="c1">Chapter 1</h1><p>Chapter one opening.</p><h2 id="s1">Section 1.1</h2><p>Section one body.</p><h2 id="s2">Section 1.2</h2><p>Section two body.</p></section>'),
"OEBPS/chapter2.xhtml": xhtml("Chapter 2", '<section epub:type="chapter"><h1 id="c2">Chapter 2</h1><p>Chapter two body.</p><a epub:type="noteref" href="#note1">1</a><aside epub:type="footnote" id="note1"><p>A footnote that should not interrupt default prose.</p></aside></section>'),
"OEBPS/appendix.xhtml": xhtml("Appendix", '<section epub:type="appendix"><h1 id="app">Appendix A</h1><p>Appendix body.</p></section>'),
}
write_epub("epub3_nested/nested.epub", files)

# One chapter starts in one spine file and continues through a second file with no nav node.
files = {
"OEBPS/package.opf": '''<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">split</dc:identifier><dc:title>Split Chapter</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="a" href="c1a.xhtml" media-type="application/xhtml+xml"/><item id="b" href="c1b.xhtml" media-type="application/xhtml+xml"/><item id="c" href="c2.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="a"/><itemref idref="b"/><itemref idref="c"/></spine></package>''',
"OEBPS/nav.xhtml": xhtml("Contents", '<nav epub:type="toc"><ol><li><a href="c1a.xhtml#c1">Chapter 1</a></li><li><a href="c2.xhtml#c2">Chapter 2</a></li></ol></nav>'),
"OEBPS/c1a.xhtml": xhtml("Chapter 1a", '<h1 id="c1">Chapter 1</h1><p>First half.</p>'),
"OEBPS/c1b.xhtml": xhtml("Chapter 1b", '<p>Second half of the same chapter.</p>'),
"OEBPS/c2.xhtml": xhtml("Chapter 2", '<h1 id="c2">Chapter 2</h1><p>Next chapter.</p>'),
}
write_epub("epub3_nested/split_chapter.epub", files)

# Two chapters in one XHTML document, distinguished only by fragment targets.
files = {
"OEBPS/package.opf": '''<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">multi</dc:identifier><dc:title>Multi Chapter File</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="body" href="body.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="body"/></spine></package>''',
"OEBPS/nav.xhtml": xhtml("Contents", '<nav epub:type="toc"><ol><li><a href="body.xhtml#c1">Chapter 1</a></li><li><a href="body.xhtml#c2">Chapter 2</a></li></ol></nav>'),
"OEBPS/body.xhtml": xhtml("Body", '<h1 id="c1">Chapter 1</h1><p>First chapter.</p><h1 id="c2">Chapter 2</h1><p>Second chapter.</p>'),
}
write_epub("epub3_nested/multi_chapter_one_file.epub", files)

# EPUB 2 NCX fallback.
files = {
"OEBPS/package.opf": '''<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">ncx</dc:identifier><dc:title>NCX Book</dc:title><dc:language>en</dc:language></metadata><manifest><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/><item id="c1" href="c1.xhtml" media-type="application/xhtml+xml"/><item id="c2" href="c2.xhtml" media-type="application/xhtml+xml"/></manifest><spine toc="ncx"><itemref idref="c1"/><itemref idref="c2"/></spine></package>''',
"OEBPS/toc.ncx": '''<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><head/><docTitle><text>NCX Book</text></docTitle><navMap><navPoint id="n1" playOrder="1"><navLabel><text>Chapter 1</text></navLabel><content src="c1.xhtml#c1"/></navPoint><navPoint id="n2" playOrder="2"><navLabel><text>Chapter 2</text></navLabel><content src="c2.xhtml#c2"/></navPoint></navMap></ncx>''',
"OEBPS/c1.xhtml": xhtml("Chapter 1", '<h1 id="c1">Chapter 1</h1><p>One.</p>'),
"OEBPS/c2.xhtml": xhtml("Chapter 2", '<h1 id="c2">Chapter 2</h1><p>Two.</p>'),
}
write_epub("epub3_nested/ncx_fallback.epub", files)

print("generated EPUB fixtures")
