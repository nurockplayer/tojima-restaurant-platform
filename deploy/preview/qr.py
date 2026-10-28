"""Render tables.csv (storeadmin tables -csv) as a printable QR page."""
import csv
import html
import os
import sys

os.umask(0o077)  # the page embeds live table locators

rows = list(csv.DictReader(open(sys.argv[1])))
cards = ''.join(
    '<figure><div class="qr" data-url="%s"></div><figcaption>%s</figcaption></figure>'
    % (html.escape(r['url_or_locator']), html.escape(r['name'] or r['table_id'])) for r in rows)
page = (
    '<!doctype html><meta charset="utf-8"><title>Table QR</title>'
    '<style>body{font-family:sans-serif;display:flex;flex-wrap:wrap;gap:32px;padding:24px}'
    'figure{text-align:center;font-size:20px}</style>'
    '<script src="https://cdn.jsdelivr.net/npm/qrcode-generator@1.4.4/qrcode.js"></script>'
    + cards +
    "<script>document.querySelectorAll('.qr').forEach(function(e){"
    "var q=qrcode(0,'M');q.addData(e.dataset.url);q.make();e.innerHTML=q.createSvgTag(6);});</script>")
open(sys.argv[2], 'w').write(page)
