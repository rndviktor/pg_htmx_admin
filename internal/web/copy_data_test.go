package web

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildCopySQL(t *testing.T) {
	base := map[string]string{"schema": "public", "table": "orders"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name string
		dir  string
		v    map[string]string
		cols []string
		want string
		err  string
	}{
		{"export csv header", copyExport, with("header", "on"), nil,
			`COPY public.orders TO STDOUT WITH (FORMAT csv, HEADER true)`, ""},
		{"import columns", copyImport, with(), []string{"id", "Total Amount"},
			`COPY public.orders ("id", "Total Amount") FROM STDIN WITH (FORMAT csv)`, ""},
		{"tab delimiter and null", copyExport, with("format", "text", "delimiter", "tab", "null", "N/A"), nil,
			`COPY public.orders TO STDOUT WITH (FORMAT text, DELIMITER E'\t', NULL 'N/A')`, ""},
		{"csv quote escape encoding", copyImport, with("delimiter", ";", "quote", "'", "escape", "\\", "encoding", "LATIN1"), nil,
			`COPY public.orders FROM STDIN WITH (FORMAT csv, DELIMITER ';', QUOTE '''', ESCAPE '\', ENCODING 'LATIN1')`, ""},
		{"binary ignores text options", copyExport, with("format", "binary", "delimiter", ";", "header", "on", "null", "x"), nil,
			`COPY public.orders TO STDOUT WITH (FORMAT binary)`, ""},
		{"odd identifiers", copyExport, map[string]string{"schema": `my"s`, "table": "T 1"}, nil,
			`COPY "my""s"."T 1" TO STDOUT WITH (FORMAT csv)`, ""},
		{"null literal is quoted", copyExport, with("null", "x'); DROP TABLE t; --"), nil,
			`NULL 'x''); DROP TABLE t; --'`, ""},
		{"bad format", copyExport, with("format", "json"), nil, "", "Unsupported format"},
		{"bad delimiter", copyExport, with("delimiter", ",,"), nil, "", "Delimiter must be"},
		{"bad encoding", copyExport, with("encoding", "utf8'; --"), nil, "", "Invalid encoding"},
		{"missing table", copyExport, map[string]string{"schema": "public"}, nil, "", "Table is required"},
		{"bad direction", "sideways", with(), nil, "", "Unknown direction"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildCopySQL(tc.dir, tc.v, tc.cols)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error %q, got %q / %v", tc.err, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func multipartBody(t *testing.T, parts ...[2]string) *multipart.Reader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p[0] == "file" {
			fw, _ := mw.CreateFormFile("file", p[1])
			fw.Write([]byte("id,name\n1,a\n"))
			continue
		}
		mw.WriteField(p[0], p[1])
	}
	mw.Close()
	return multipart.NewReader(&buf, mw.Boundary())
}

func TestReadImportRequest(t *testing.T) {
	t.Run("fields before file", func(t *testing.T) {
		fields, file, err := readImportRequest(multipartBody(t,
			[2]string{"table", "orders"}, [2]string{"col", "id"}, [2]string{"col", "name"}, [2]string{"file", "o.csv"}))
		if err != nil {
			t.Fatal(err)
		}
		if fields.Get("table") != "orders" || len(fields["col"]) != 2 {
			t.Errorf("fields = %v", fields)
		}
		if file.FileName() != "o.csv" {
			t.Errorf("file name = %q", file.FileName())
		}
	})
	t.Run("no file", func(t *testing.T) {
		_, _, err := readImportRequest(multipartBody(t, [2]string{"table", "orders"}))
		if err == nil || !strings.Contains(err.Error(), "Choose a file") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("empty file name", func(t *testing.T) {
		_, _, err := readImportRequest(multipartBody(t, [2]string{"file", ""}))
		if err == nil || !strings.Contains(err.Error(), "Choose a file") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestCopyModalRenders(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Skipf("templates unavailable: %v", err)
	}
	cases := []struct {
		dir  string
		want []string
		not  []string
	}{
		{copyImport, []string{`hx-post="/api/copy/import"`, `hx-encoding="multipart/form-data"`, `type="file" name="file"`,
			`hx-post="/api/copy/import/preview"`, `name="col" value="id" checked`}, []string{`data-action="/api/copy/export"`}},
		{copyExport, []string{`data-action="/api/copy/export"`, `copyExport(this)`, `hx-post="/api/copy/export/preview"`},
			[]string{`type="file"`, `hx-post="/api/copy/import"`}},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		RenderPartial(w, "import_export_modal.html", copyModalData{
			Dir: c.dir, ServerID: 3, DB: "shop", Schema: "public", Table: "orders",
			Columns: []string{"id", "name"}, Values: map[string]string{"format": "csv"}, Message: "Imported 2 rows",
		})
		body := w.Body.String()
		for _, want := range c.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q in\n%s", c.dir, want, body)
			}
		}
		for _, bad := range c.not {
			if strings.Contains(body, bad) {
				t.Errorf("%s: unexpected %q", c.dir, bad)
			}
		}
	}
}
