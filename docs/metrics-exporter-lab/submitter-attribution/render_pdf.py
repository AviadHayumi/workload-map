# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

"""Render DESIGN.md and the operator/issue appendices with ReportLab and pypdf."""

import html
import re
from pathlib import Path
from urllib.parse import urljoin

from pypdf import PdfReader
from reportlab.lib import colors
from reportlab.lib.pagesizes import A3, A4
from reportlab.lib.styles import ParagraphStyle
from reportlab.pdfbase.pdfmetrics import stringWidth
from reportlab.platypus import (
    BaseDocTemplate, Frame, Image, NextPageTemplate, PageBreak, PageTemplate,
    Paragraph, Preformatted, Spacer, Table, TableStyle,
)

ROOT = Path(__file__).resolve().parent
OUTPUT = ROOT / "submitter-attribution.pdf"
BASE = (
    "https://github.com/AviadHayumi/workload-map/blob/"
    "karta-metrics-exporter-poc-impl/docs/metrics-exporter-lab/submitter-attribution/"
)
WIDTH = A4[0] - 80
INK = colors.HexColor("#182d39")
TEAL = colors.HexColor("#175b75")
PALE = colors.HexColor("#edf4f7")
MUTED = colors.HexColor("#536670")
STYLES = {
    "title": ParagraphStyle("title", fontName="Helvetica-Bold", fontSize=21,
                            leading=25, spaceAfter=8, textColor=INK),
    "heading": ParagraphStyle("heading", fontName="Helvetica-Bold", fontSize=13,
                              leading=17, spaceBefore=10, spaceAfter=8,
                              textColor=TEAL, keepWithNext=True),
    "subheading": ParagraphStyle("subheading", fontName="Helvetica-Bold", fontSize=10.5,
                                 leading=14, spaceBefore=9, spaceAfter=5,
                                 textColor=TEAL, keepWithNext=True),
    "body": ParagraphStyle("body", fontName="Helvetica", fontSize=10.2,
                           leading=13.7, spaceAfter=7, textColor=INK),
    "meta": ParagraphStyle("meta", fontName="Helvetica", fontSize=8.5,
                           leading=11, spaceAfter=8, textColor=MUTED),
    "cell": ParagraphStyle("cell", fontName="Helvetica", fontSize=9.1,
                           leading=11.7, textColor=INK),
    "cellhead": ParagraphStyle("cellhead", fontName="Helvetica-Bold", fontSize=9.1,
                               leading=11.7, textColor=colors.white),
    "code": ParagraphStyle("code", fontName="Courier", fontSize=7.8,
                           leading=10, textColor=INK),
    "list": ParagraphStyle("list", fontName="Helvetica", fontSize=10.2,
                           leading=13.7, leftIndent=12, firstLineIndent=-10,
                           spaceAfter=5, textColor=INK),
}


def inline(value):
    parts = re.split(r"(\[[^\]]+\]\([^\s)]+\)|`[^`]+`)", value)
    result = []
    for part in parts:
        match = re.fullmatch(r"\[([^\]]+)\]\(([^\s)]+)\)", part)
        if match:
            target = html.escape(urljoin(BASE, match[2]), quote=True)
            result.append(f'<link href="{target}" color="#175b75">{html.escape(match[1])}</link>')
        elif part.startswith("`") and part.endswith("`"):
            result.append('<font name="Courier" size="8.2">' + html.escape(part[1:-1]) + '</font>')
        else:
            result.append(html.escape(part))
    return "".join(result)


def decoration(canvas, doc):
    canvas.saveState()
    w, h = canvas._pagesize
    canvas.setFillColor(TEAL)
    canvas.rect(40, h - 17, 36, 3, fill=1, stroke=0)
    canvas.setFillColor(MUTED)
    canvas.setFont("Helvetica", 8)
    if doc.page > 1:
        canvas.drawString(40, h - 30, "WORKLOAD SUBMITTER ATTRIBUTION")
    canvas.setStrokeColor(colors.HexColor("#d6e2e8"))
    canvas.line(40, 39, w - 40, 39)
    canvas.drawString(40, 26, "Workload Map | Lab findings and proposal | 16 October 2026")
    canvas.drawRightString(w - 40, 26, str(doc.page))
    canvas.restoreState()


def table_widths(headers):
    if headers[0] == "Path":
        return [0.17, 0.29, 0.26, 0.28]
    if headers[0] == "Proposed metadata":
        return [0.40, 0.16, 0.44]
    if headers[0] == "Area":
        return [0.19, 0.43, 0.38]
    if headers[0] == "Input":
        return [0.29, 0.23, 0.48]
    if len(headers) == 2:
        return [0.35, 0.65]
    return [1 / len(headers)] * len(headers)


def blocks(source, appendix=None):
    lines = source.read_text().splitlines()
    story = []
    i = 0
    while i < len(lines):
        line = lines[i].strip()
        i += 1
        if line == "<!-- pagebreak -->":
            story.append(PageBreak())
            continue
        if not line or line.startswith("<!--") or line in ("<details>", "</details>"):
            continue
        if line.startswith("<summary>"):
            title = re.sub(r"</?summary>", "", line)
            story.append(Paragraph(inline(title), STYLES["subheading"]))
            continue
        if line.startswith("#"):
            level = len(line) - len(line.lstrip("#"))
            title = line[level:].strip()
            if title.startswith("9. One end-to-end"):
                if story and isinstance(story[-1], PageBreak):
                    story.pop()
                story.extend([NextPageTemplate("sequence"), PageBreak()])
            if title.startswith("10. Evidence"):
                story.extend([NextPageTemplate("normal"), PageBreak()])
            if appendix and level == 1:
                title = appendix + ". " + title
            style = "title" if level == 1 else "heading" if level == 2 else "subheading"
            story.append(Paragraph(inline(title), STYLES[style]))
            continue
        match = re.fullmatch(r"!\[([^\]]*)\]\(([^)]+)\)", line)
        if match:
            img = Image(str(ROOT / match[2]))
            is_sequence = "sequence" in match[2]
            max_height = 900 if is_sequence else 265
            max_width = A3[0] - 92 if is_sequence else WIDTH
            scale = min(max_width / img.imageWidth, max_height / img.imageHeight)
            img.drawWidth, img.drawHeight = img.imageWidth * scale, img.imageHeight * scale
            img.hAlign = "CENTER"
            story.extend([img, Spacer(1, 9)])
            continue
        if line.startswith("```"):
            code = []
            while i < len(lines) and not lines[i].startswith("```"):
                code.append(lines[i])
                i += 1
            if i == len(lines):
                raise ValueError("Unclosed code fence")
            i += 1
            for row in code:
                if stringWidth(row, "Courier", 7.8) > WIDTH - 24:
                    raise ValueError("Code line too long: " + row)
            box = Table([[Preformatted("\n".join(code), STYLES["code"])]], colWidths=[WIDTH])
            box.setStyle(TableStyle([
                ("BACKGROUND", (0, 0), (-1, -1), PALE),
                ("BOX", (0, 0), (-1, -1), 0.4, colors.HexColor("#d4e2e8")),
                ("LEFTPADDING", (0, 0), (-1, -1), 10),
                ("TOPPADDING", (0, 0), (-1, -1), 8),
                ("BOTTOMPADDING", (0, 0), (-1, -1), 8),
            ]))
            story.extend([box, Spacer(1, 7)])
            continue
        if line.startswith("|"):
            rows = []
            i -= 1
            while i < len(lines) and lines[i].strip().startswith("|"):
                row = [cell.strip() for cell in lines[i].strip().strip("|").split("|")]
                if not all(re.fullmatch(r"[:\- ]+", cell) for cell in row):
                    rows.append(row)
                i += 1
            data = []
            for n, row in enumerate(rows):
                rendered = []
                for column, cell in enumerate(row):
                    value = inline(cell)
                    if n and column == 0 and rows[0][0] == "Proposed metadata" and len(cell) > 40:
                        domain, key = cell.strip("`").split("/", 1)
                        value = inline("`" + domain + "/`") + "<br/>" + inline("`" + key + "`")
                    rendered.append(Paragraph(value, STYLES["cellhead" if n == 0 else "cell"]))
                data.append(rendered)
            table = Table(data, colWidths=[WIDTH * f for f in table_widths(rows[0])],
                          repeatRows=1, hAlign="LEFT")
            table.setStyle(TableStyle([
                ("BACKGROUND", (0, 0), (-1, 0), TEAL),
                ("ROWBACKGROUNDS", (0, 1), (-1, -1), [colors.white, PALE]),
                ("VALIGN", (0, 0), (-1, -1), "TOP"),
                ("LEFTPADDING", (0, 0), (-1, -1), 7),
                ("RIGHTPADDING", (0, 0), (-1, -1), 7),
                ("TOPPADDING", (0, 0), (-1, -1), 5),
                ("BOTTOMPADDING", (0, 0), (-1, -1), 5),
            ]))
            story.extend([table, Spacer(1, 7)])
            continue
        marker = re.match(r"^(\d+\.|-)\s+(.*)", line)
        paragraph = [marker[2] if marker else line]
        while i < len(lines) and lines[i].strip():
            nxt = lines[i].strip()
            if nxt.startswith(("#", "```", "|", "<", "![")) or re.match(r"^(\d+\.|-)\s+", nxt):
                break
            paragraph.append(nxt)
            i += 1
        text = inline(" ".join(paragraph))
        if marker:
            text = (marker[1] if marker[1] != "-" else "&#8226;") + " " + text
        style = "list" if marker else "meta" if line.startswith("Workload Map |") else "body"
        story.append(Paragraph(text, STYLES[style]))
    return story


def main():
    story = blocks(ROOT / "DESIGN.md")
    story.extend([PageBreak(), *blocks(ROOT / "OPERATOR-CONTRACT.md", "Appendix A")])
    story.extend([PageBreak(), *blocks(ROOT / "NVCF-FEATURE-REQUEST.md", "Appendix B")])
    doc = BaseDocTemplate(str(OUTPUT), pagesize=A4, leftMargin=40, rightMargin=40,
                           topMargin=45, bottomMargin=49,
                           title="Who submitted this workload?",
                           author="Aviad Hayumi", subject="Workload Map: evidence and integration proposal")
    for name, size in [("normal", A4), ("sequence", A3)]:
        frame = Frame(40, 49, size[0] - 80, size[1] - 94, id=name)
        doc.addPageTemplates(PageTemplate(id=name, frames=[frame],
                                         pagesize=size, onPage=decoration))
    doc.build(story)
    pdf = PdfReader(OUTPUT)
    print(f"Created {len(pdf.pages)} pages, {OUTPUT.stat().st_size} bytes")
    for number, page in enumerate(pdf.pages, 1):
        print(f"Page {number}: {len(page.extract_text().split())} words, "
              f"{len(page.get('/Annots', []))} links")


if __name__ == "__main__":
    main()
