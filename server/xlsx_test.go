package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestXlsxColumn(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 52: "BA"} {
		if got := xlsxColumn(i); got != want {
			t.Errorf("xlsxColumn(%d) = %q, want %q", i, got, want)
		}
	}
}

func TestWriteXLSX(t *testing.T) {
	wide := make([]any, 30)
	for i := range wide {
		wide[i] = "x"
	}
	var buf bytes.Buffer
	err := writeXLSX(&buf, []xlsxSheet{
		{Name: "Check-in & out", Rows: [][]any{{"ID", "Name"}, {"P1", "B&B <x>"}, {nil, int64(97500), ""}}},
		{Name: "Wide", Rows: [][]any{wide}},
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		parts[f.Name] = string(b)
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Errorf("%s: invalid XML: %v", f.Name, err)
				break
			}
		}
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml",
		"xl/_rels/workbook.xml.rels", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		if _, ok := parts[name]; !ok {
			t.Errorf("missing part %s", name)
		}
	}
	if wb := parts["xl/workbook.xml"]; !strings.Contains(wb, `name="Check-in &amp; out"`) || !strings.Contains(wb, `name="Wide"`) {
		t.Errorf("workbook.xml sheet names wrong: %s", wb)
	}
	s1 := parts["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`r="B2"`, `B&amp;B &lt;x&gt;`, `<c r="B3"><v>97500</v></c>`} {
		if !strings.Contains(s1, want) {
			t.Errorf("sheet1 missing %q: %s", want, s1)
		}
	}
	for _, ref := range []string{`r="A3"`, `r="C3"`} {
		if strings.Contains(s1, ref) {
			t.Errorf("nil or empty-string cell %s should not be written: %s", ref, s1)
		}
	}
	if !strings.Contains(parts["xl/worksheets/sheet2.xml"], `r="AD1"`) {
		t.Error("sheet2 missing AD1")
	}
}
