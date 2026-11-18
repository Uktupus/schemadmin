package schemadmin

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

//go:embed assets/admin.css assets/admin.js assets/templates/*.html
var assets embed.FS

// pageSet holds the parsed templates and the static files.
type pageSet struct {
	tmpl    map[string]*template.Template
	static  map[string]staticFile
	version string // changes whenever a static file does
}

type staticFile struct {
	data        []byte
	contentType string
}

// modalHeader marks a request made by the admin's script for a page to be
// shown in a dialog. The response is the page's content without the frame.
const modalHeader = "X-Schemadmin-Modal"

var pageNames = []string{"index", "list", "form", "delete", "error"}

func loadPages() (*pageSet, error) {
	ps := &pageSet{tmpl: map[string]*template.Template{}, static: map[string]staticFile{}}
	for _, name := range pageNames {
		t, err := template.ParseFS(assets, "assets/templates/layout.html", "assets/templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("schemadmin: template %s: %w", name, err)
		}
		ps.tmpl[name] = t
	}
	sum := sha256.New()
	for file, contentType := range map[string]string{
		"admin.css": "text/css; charset=utf-8",
		"admin.js":  "text/javascript; charset=utf-8",
	} {
		data, err := assets.ReadFile("assets/" + file)
		if err != nil {
			return nil, fmt.Errorf("schemadmin: %w", err)
		}
		ps.static[file] = staticFile{data: data, contentType: contentType}
	}
	sum.Write(ps.static["admin.css"].data)
	sum.Write(ps.static["admin.js"].data)
	ps.version = hex.EncodeToString(sum.Sum(nil)[:6])
	return ps, nil
}

// pageData is what the layout needs; Body is what the page itself needs.
type pageData struct {
	Title     string
	Admin     string
	IndexURL  string
	CSS       string
	JS        string
	Nav       []navItem
	NavFilter bool
	Body      any
}

type navItem struct {
	Label   string
	URL     string
	Current bool
}

// nav lists the tables this request may view.
func (a *Admin) nav(rq *request, current *tableMeta) []navItem {
	a.mu.RLock()
	tables := a.tables
	a.mu.RUnlock()
	var items []navItem
	for _, tm := range tables {
		if rq.can(ActionView, tm) {
			items = append(items, navItem{Label: tm.label, URL: a.listURL(tm), Current: tm == current})
		}
	}
	return items
}

// render executes a page into a buffer first, so that a template error
// produces an error page rather than half a document.
func (a *Admin) render(w http.ResponseWriter, rq *request, status int, name, title string, current *tableMeta, body any) {
	data := pageData{
		Title:    title,
		Admin:    a.title,
		IndexURL: a.indexURL(),
		CSS:      a.staticURL("admin.css"),
		JS:       a.staticURL("admin.js"),
		Body:     body,
	}
	if rq != nil {
		data.Nav = a.nav(rq, current)
		data.NavFilter = len(data.Nav) > 8
	}
	// The script asks for a page without its frame to show it in a dialog.
	entry := "layout"
	if rq != nil && rq.r.Header.Get(modalHeader) != "" {
		entry = "fragment"
	}
	w.Header().Add("Vary", modalHeader)
	var buf bytes.Buffer
	if err := a.pages.tmpl[name].ExecuteTemplate(&buf, entry, data); err != nil {
		a.log.Error("schemadmin: template failed", "page", name, "error", err)
		http.Error(w, "Something went wrong on our side.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

type errorBody struct {
	Heading string
	Message string
	HomeURL string
}

// renderError shows an error inside the admin's layout.
func (a *Admin) renderError(w http.ResponseWriter, rq *request, status int, heading, message string) {
	a.render(w, rq, status, "error", heading, nil, errorBody{Heading: heading, Message: message, HomeURL: a.indexURL()})
}

// plainError shows an error to a request that has not been let in: no
// navigation, nothing about the database.
func (a *Admin) plainError(w http.ResponseWriter, status int, heading, message string) {
	a.render(w, nil, status, "error", heading, nil, errorBody{Heading: heading, Message: message})
}

func (a *Admin) handleStatic(w http.ResponseWriter, r *http.Request) {
	f, ok := a.pages.static[r.PathValue("file")]
	if !ok {
		a.notFound(w, a.request(r))
		return
	}
	w.Header().Set("Content-Type", f.contentType)
	if r.URL.Query().Get("v") == a.pages.version {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	_, _ = w.Write(f.data)
}

type indexBody struct {
	Heading   string
	Notice    string
	ReloadURL string
	Tables    []indexTable
}

type indexTable struct {
	Label   string
	Name    string
	URL     string
	Columns int
	View    bool
}

func (a *Admin) handleIndex(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	body := indexBody{Heading: a.title, Notice: notices[r.URL.Query().Get("m")]}
	if a.allowReload {
		body.ReloadURL = a.base + "/reload"
	}
	a.mu.RLock()
	tables := a.tables
	a.mu.RUnlock()
	for _, tm := range tables {
		if rq.can(ActionView, tm) {
			body.Tables = append(body.Tables, indexTable{
				Label: tm.label, Name: tm.key, URL: a.listURL(tm),
				Columns: len(tm.cols), View: tm.t.View,
			})
		}
	}
	a.render(w, rq, http.StatusOK, "index", "Tables", nil, body)
}

// cell is one value on a page.
type cell struct {
	Text  string
	State string // "", "null", "empty", "masked", "muted", "tag"
	Num   bool
	URL   string // where the value links to, if anywhere
	Modal bool   // the link is a row and opens over the page
	Extra string // the label of the row a foreign key points to
}

// showValue renders a value for reading.
func showValue(c *colMeta, v value, limit int) cell {
	switch {
	case v.Null:
		return cell{Text: "NULL", State: "null"}
	case c.masked:
		return cell{Text: "••••••", State: "masked"}
	case c.c.Kind == KindBytes:
		n := len(v.S) / 2
		return cell{Text: groupDigits(int64(n)) + " bytes", State: "muted"}
	case v.S == "":
		return cell{Text: "empty", State: "empty"}
	}
	s := v.S
	switch c.c.Kind {
	case KindDateTime, KindDateTimeTZ:
		s = prettyDateTime(s)
	}
	s = strings.ToValidUTF8(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\x00", "").Replace(truncate(s, limit)), "\uFFFD")
	num := false
	switch c.c.Kind {
	case KindInt, KindFloat, KindDecimal:
		num = c.fk == nil
	}
	return cell{Text: s, Num: num}
}

// prettyDateTime turns the ISO form "2024-01-02T03:04:05Z" into
// "2024-01-02 03:04:05 UTC" for reading. Other text is returned as it is.
func prettyDateTime(s string) string {
	if len(s) < 16 || s[10] != 'T' || !isDate(s[:10]) {
		return s
	}
	s = s[:10] + " " + s[11:]
	if strings.HasSuffix(s, "Z") {
		s = s[:len(s)-1] + " UTC"
	}
	return s
}

// groupDigits formats n with thin separators: 1234567 -> "1,234,567".
func groupDigits(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
