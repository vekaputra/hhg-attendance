package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
)

// xlsxSheet is one worksheet: Rows[r][c] is a string (inline text), an int/int64/float64 (number), or nil (empty).
type xlsxSheet struct {
	Name string
	Rows [][]any
}

// xlsxColumn converts a zero-based column index to its letters: 0 → "A", 26 → "AA".
func xlsxColumn(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

func xlsxEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

const xlsxHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// writeXLSX writes a minimal workbook (no styles or shared strings) that Excel, Sheets and LibreOffice open cleanly.
func writeXLSX(w io.Writer, sheets []xlsxSheet) error {
	var types, wb, wbRels bytes.Buffer
	types.WriteString(xlsxHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	wb.WriteString(xlsxHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	wbRels.WriteString(xlsxHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)

	zw := zip.NewWriter(w)
	put := func(name, body string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, body)
		return err
	}

	for n, sh := range sheets {
		n++
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xlsxEscape(sh.Name), n, n)
		fmt.Fprintf(&wbRels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)

		var sb bytes.Buffer
		sb.WriteString(xlsxHeader + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
		for r, row := range sh.Rows {
			fmt.Fprintf(&sb, `<row r="%d">`, r+1)
			for c, v := range row {
				ref := xlsxColumn(c) + strconv.Itoa(r+1)
				switch v := v.(type) {
				case nil:
				case string:
					fmt.Fprintf(&sb, `<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, xlsxEscape(v))
				case int:
					fmt.Fprintf(&sb, `<c r="%s"><v>%d</v></c>`, ref, v)
				case int64:
					fmt.Fprintf(&sb, `<c r="%s"><v>%d</v></c>`, ref, v)
				case float64:
					fmt.Fprintf(&sb, `<c r="%s"><v>%s</v></c>`, ref, strconv.FormatFloat(v, 'f', -1, 64))
				default:
					return fmt.Errorf("xlsx: unsupported cell type %T at %s in %q", v, ref, sh.Name)
				}
			}
			sb.WriteString(`</row>`)
		}
		sb.WriteString(`</sheetData></worksheet>`)
		if err := put(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), sb.String()); err != nil {
			return err
		}
	}
	types.WriteString(`</Types>`)
	wb.WriteString(`</sheets></workbook>`)
	wbRels.WriteString(`</Relationships>`)

	rootRels := xlsxHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`
	for _, p := range []struct{ name, body string }{
		{"[Content_Types].xml", types.String()},
		{"_rels/.rels", rootRels},
		{"xl/workbook.xml", wb.String()},
		{"xl/_rels/workbook.xml.rels", wbRels.String()},
	} {
		if err := put(p.name, p.body); err != nil {
			return err
		}
	}
	return zw.Close()
}
