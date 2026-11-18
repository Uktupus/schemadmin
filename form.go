package schemadmin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// Forms.
//
// Each editable column i of a table has up to three form fields:
//
//	v.i   the text of the value
//	n.i   a checkbox: the value is NULL (nullable columns only)
//	o.i   the fingerprint of the value when the form was rendered
//	      (edit forms only)
//
// NULL and the empty string are different values and stay different: the
// checkbox says NULL, an empty v.i on a text column says "".
//
// An edit writes only the columns whose submitted value differs from o.i.
// Every control is chosen so that it hands an untouched value back exactly
// as it was given, so an untouched form writes nothing.

const fkSelectLimit = 100

type formBody struct {
	TableLabel string
	ListURL    string
	Heading    string
	Notice     string
	Error      string
	Action     string
	Submit     string // empty when the form cannot be submitted
	CancelURL  string
	DeleteURL  string
	Fields     []fieldView
}

type fieldView struct {
	ID          string
	Label       string
	Meta        string
	Help        string
	Error       string
	Control     string // "input", "textarea", "select" or "static"
	Type        string
	InputMode   string
	Name        string
	Value       string
	Placeholder string
	MaxLength   int
	Rows        int
	Code        bool // monospaced
	Options     []option
	ListID      string
	OptionsURL  string
	NullName    string
	Null        bool
	OrigName    string
	Orig        string
	Static      cell
	RefURL      string
	RefRow      bool
}

// submission is a posted form, by column index.
type submission struct {
	text map[int]string
	null map[int]bool
	orig map[int]string
	sent map[int]bool
	errs map[int]string
}

// readForm parses the request body. It writes the response and returns
// nil when the body is unusable.
func (a *Admin) readForm(w http.ResponseWriter, rq *request, tm *tableMeta) *submission {
	r := rq.r
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.renderError(w, rq, http.StatusRequestEntityTooLarge, "Too large", "The submitted form is too large.")
		} else {
			a.renderError(w, rq, http.StatusBadRequest, "Bad request", "The submitted form could not be read.")
		}
		return nil
	}
	sub := &submission{text: map[int]string{}, null: map[int]bool{}, orig: map[int]string{}, sent: map[int]bool{}, errs: map[int]string{}}
	for _, c := range tm.cols {
		i := strconv.Itoa(c.idx)
		if r.PostForm.Has("v." + i) {
			sub.text[c.idx] = r.PostForm.Get("v." + i)
			sub.sent[c.idx] = true
		}
		if r.PostForm.Has("n." + i) {
			sub.null[c.idx] = true
			sub.sent[c.idx] = true
		}
		sub.orig[c.idx] = r.PostForm.Get("o." + i)
	}
	return sub
}

// interpret decides what a submitted field means. omit is true when the
// column is to be left out of an INSERT so that its default applies.
func interpret(c *colMeta, creating bool, text string, null bool) (v value, omit bool, msg string) {
	if null && c.c.Nullable {
		return value{Null: true}, false, ""
	}
	blank := text == ""
	if !c.textual() {
		blank = strings.TrimSpace(text) == ""
	}
	if !blank {
		return value{S: text}, false, ""
	}
	switch {
	case creating && c.c.HasDefault:
		return value{}, true, ""
	case c.textual():
		return value{S: ""}, false, ""
	case c.c.Nullable:
		return value{Null: true}, false, ""
	}
	return value{}, false, "Enter a value."
}

// formFingerprint is fingerprint, except that a masked value, which the
// admin never reads, is known only as set or NULL.
func formFingerprint(c *colMeta, v value) string {
	if c.masked && !v.Null {
		return "s"
	}
	return fingerprint(v)
}

// expandCompositeFKs copies a composite foreign-key selection from the
// lead column onto every column of the key, so the form needs only one
// control for the whole constraint. An unchanged selection leaves the
// whole group unsent so the update loop does not touch it.
func expandCompositeFKs(tm *tableMeta, sub *submission) {
	for _, c := range tm.cols {
		if c.fk == nil || len(c.fk.cols) < 2 || !sub.sent[c.idx] {
			continue
		}
		raw := value{Null: sub.null[c.idx], S: sub.text[c.idx]}
		if raw.Null {
			raw.S = ""
		}
		if sub.orig[c.idx] != "" && fingerprint(raw) == sub.orig[c.idx] {
			for _, sib := range c.fk.cols {
				sub.sent[sib.idx] = false
			}
			continue
		}
		if sub.null[c.idx] {
			for _, sib := range c.fk.cols {
				sub.null[sib.idx] = true
				sub.text[sib.idx] = ""
				sub.sent[sib.idx] = true
				sub.orig[sib.idx] = "" // force a write of NULL
			}
			continue
		}
		parts, ok := decodeKey(sub.text[c.idx], len(c.fk.cols))
		if !ok {
			sub.errs[c.idx] = "Choose one of the listed values."
			continue
		}
		for i, sib := range c.fk.cols {
			sub.text[sib.idx] = parts[i]
			sub.null[sib.idx] = false
			sub.sent[sib.idx] = true
			sub.orig[sib.idx] = "" // force a write of the new part
		}
	}
}

// argument validates v for column c and returns what to bind. Line breaks
// arrive from browsers as CRLF; they are stored as LF unless the value
// being replaced used CRLF itself.
func argument(c *colMeta, v value, current *value) (any, string) {
	if v.Null {
		return nil, ""
	}
	arg, msg := parseInput(c.c, c.choices, v.S)
	if msg != "" {
		return nil, msg
	}
	if s, ok := arg.(string); ok && strings.Contains(s, "\r\n") {
		if current == nil || current.Null || !strings.Contains(current.S, "\r\n") {
			arg = strings.ReplaceAll(s, "\r\n", "\n")
		}
	}
	return arg, ""
}

func (a *Admin) handleNew(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionCreate)
	if tm == nil {
		return
	}
	a.renderForm(w, rq, http.StatusOK, tm, "", nil, nil, "")
}

func (a *Admin) handleCreate(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionCreate)
	if tm == nil {
		return
	}
	sub := a.readForm(w, rq, tm)
	if sub == nil {
		return
	}
	expandCompositeFKs(tm, sub)

	var cols []*Column
	var vals []any
	for _, c := range tm.cols {
		if !c.writable() {
			continue
		}
		v, omit, msg := interpret(c, true, sub.text[c.idx], sub.null[c.idx])
		if msg == "" && !omit {
			var arg any
			if arg, msg = argument(c, v, nil); msg == "" {
				cols, vals = append(cols, c.c), append(vals, arg)
			}
		}
		if msg != "" {
			sub.errs[c.idx] = msg
		}
	}
	if len(sub.errs) > 0 {
		a.renderForm(w, rq, http.StatusUnprocessableEntity, tm, "", nil, sub, "")
		return
	}

	ctx, cancel := rq.query()
	defer cancel()
	query, args := buildInsert(a.d, tm, cols, vals, tm.pk)
	var created [][]value
	var err error
	switch {
	case len(tm.pk) == 0:
		_, err = a.db.ExecContext(ctx, query, args...)
	case supportsReturning(a.d):
		created, err = readRows(ctx, a.db, tm.pk, query, args)
	default:
		res, execErr := a.db.ExecContext(ctx, query, args...)
		err = execErr
		if err == nil && len(tm.pk) == 1 && tm.pk[0].c.AutoIncrement {
			if id, idErr := res.LastInsertId(); idErr == nil {
				created = [][]value{{{S: strconv.FormatInt(id, 10)}}}
			}
		}
	}
	if err != nil {
		a.renderForm(w, rq, http.StatusUnprocessableEntity, tm, "", nil, sub, a.writeError(rq, err, false))
		return
	}

	target := a.listURL(tm)
	key := ""
	if len(created) == 1 {
		if k, ok := tm.keyOf(created[0]); ok {
			key, target = k, a.rowURL(tm, k)
		}
	}
	a.log.InfoContext(r.Context(), "schemadmin: row created", slog.String("table", tm.key), slog.String("key", key))
	http.Redirect(w, r, target+"?m=created", http.StatusSeeOther)
}

func (a *Admin) handleEdit(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionView)
	if tm == nil {
		return
	}
	key, ok := tm.keyArgs(r.PathValue("key"))
	if !ok {
		a.notFound(w, rq)
		return
	}
	ctx, cancel := rq.query()
	defer cancel()
	row, found, err := a.get(ctx, a.db, tm, key)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	if !found {
		a.notFound(w, rq)
		return
	}
	a.renderForm(w, rq, http.StatusOK, tm, r.PathValue("key"), row, nil, "")
}

func (a *Admin) handleUpdate(w http.ResponseWriter, r *http.Request) {
	rq := a.request(r)
	tm := a.table(w, rq, ActionUpdate)
	if tm == nil {
		return
	}
	keyText := r.PathValue("key")
	key, ok := tm.keyArgs(keyText)
	if !ok {
		a.notFound(w, rq)
		return
	}
	sub := a.readForm(w, rq, tm)
	if sub == nil {
		return
	}

	ctx, cancel := rq.query()
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	row, found, err := a.get(ctx, tx, tm, key)
	if err != nil {
		a.fail(w, rq, err)
		return
	}
	if !found {
		_ = tx.Rollback()
		a.notFound(w, rq)
		return
	}

	// Composite foreign keys are submitted as one encoded value on the
	// lead column. Detect conflicts against that encoding, then unpack.
	for _, c := range tm.cols {
		if c.fk == nil || len(c.fk.cols) < 2 || !sub.sent[c.idx] || sub.orig[c.idx] == "" {
			continue
		}
		raw := value{Null: sub.null[c.idx], S: sub.text[c.idx]}
		if raw.Null {
			raw.S = ""
		}
		if fingerprint(raw) == sub.orig[c.idx] {
			continue
		}
		if fingerprint(compositeFKValue(c.fk, row)) != sub.orig[c.idx] {
			sub.errs[c.idx] = "This value was changed by someone else after you opened the form. Reload the page to see it."
		}
	}
	expandCompositeFKs(tm, sub)

	var cols []*Column
	var vals []any
	var names []string
	newKey := append([]any(nil), key...)
	for _, c := range tm.cols {
		cur := row[c.idx]
		orig := sub.orig[c.idx]
		if !c.writable() || !sub.sent[c.idx] {
			continue
		}
		forced := orig == "" && (c.fkFollow || (c.fk != nil && len(c.fk.cols) > 1))
		if orig == "" && !forced {
			continue
		}
		if !forced && !c.masked && c.c.Kind != KindBytes && !cur.Null && !representable(cur.S) {
			continue
		}
		if forced {
			orig = formFingerprint(c, cur)
		}
		text, null := sub.text[c.idx], sub.null[c.idx] && c.c.Nullable

		var v value
		if c.masked {
			// Write-only: an empty field keeps what is there.
			switch {
			case null:
				v = value{Null: true}
			case text != "":
				v = value{S: text}
			default:
				continue
			}
		} else {
			// Unchanged as submitted: not touched.
			raw := value{Null: null, S: text}
			if null {
				raw.S = ""
			}
			if fingerprint(raw) == orig {
				continue
			}
			var msg string
			if v, _, msg = interpret(c, false, text, null); msg != "" {
				sub.errs[c.idx] = msg
				continue
			}
		}
		// Unchanged once interpreted, e.g. NULL unticked and left blank
		// on a number: still NULL.
		if formFingerprint(c, v) == orig && !(c.masked && !v.Null) {
			continue
		}
		if formFingerprint(c, cur) != orig {
			sub.errs[c.idx] = "This value was changed by someone else after you opened the form. Reload the page to see it."
			continue
		}
		arg, msg := argument(c, v, &cur)
		if msg != "" {
			sub.errs[c.idx] = msg
			continue
		}
		cols, vals, names = append(cols, c.c), append(vals, arg), append(names, c.c.Name)
		for i, k := range tm.pk {
			if k == c {
				newKey[i] = arg
			}
		}
	}
	// The transaction is closed before anything is rendered: rendering
	// reads through the pool, which may have no second connection.
	if len(sub.errs) > 0 {
		_ = tx.Rollback()
		a.renderForm(w, rq, http.StatusUnprocessableEntity, tm, keyText, row, sub, "")
		return
	}
	if len(cols) == 0 {
		_ = tx.Rollback()
		http.Redirect(w, r, a.rowURL(tm, keyText)+"?m=nochange", http.StatusSeeOther)
		return
	}

	query, args := buildUpdate(a.d, tm, cols, vals, key)
	if err := execOne(ctx, tx, query, args); err != nil {
		_ = tx.Rollback()
		msg := "The row could not be singled out, so nothing was saved."
		if !errors.Is(err, errRowCount) {
			msg = a.writeError(rq, err, false)
		}
		a.renderForm(w, rq, http.StatusUnprocessableEntity, tm, keyText, row, sub, msg)
		return
	}
	// The key may have been edited; find out what the row is called now.
	target := a.listURL(tm)
	if updated, found, err := a.get(ctx, tx, tm, newKey); err == nil && found {
		if k, ok := tm.rowKey(updated); ok {
			target = a.rowURL(tm, k)
		}
	}
	if err := tx.Commit(); err != nil {
		a.renderForm(w, rq, http.StatusUnprocessableEntity, tm, keyText, row, sub, a.writeError(rq, err, false))
		return
	}
	a.log.InfoContext(r.Context(), "schemadmin: row updated",
		slog.String("table", tm.key), slog.String("key", keyText), slog.Any("columns", names))
	http.Redirect(w, r, target+"?m=saved", http.StatusSeeOther)
}

// rowTitle names a row for headings: its display value, or its key.
func rowTitle(tm *tableMeta, row []value) string {
	if tm.display != nil {
		if v := row[tm.display.idx]; !v.Null && strings.TrimSpace(v.S) != "" {
			return showValue(tm.display, v, 60).Text
		}
	}
	var parts []string
	for _, c := range tm.pk {
		parts = append(parts, showValue(c, row[c.idx], 40).Text)
	}
	return tm.label + " " + strings.Join(parts, ", ")
}

// renderForm shows the create form (row == nil) or the edit form. sub, when
// set, is a rejected submission to show again with its errors.
func (a *Admin) renderForm(w http.ResponseWriter, rq *request, status int, tm *tableMeta, key string, row []value, sub *submission, formError string) {
	creating := row == nil
	body := formBody{
		TableLabel: tm.label,
		ListURL:    a.listURL(tm),
		CancelURL:  a.listURL(tm),
		Error:      formError,
		Notice:     notices[rq.r.URL.Query().Get("m")],
	}
	canWrite := false
	if creating {
		body.Heading = "New row"
		body.Action = a.newURL(tm)
		body.Submit = "Create"
		canWrite = true
	} else {
		body.Heading = rowTitle(tm, row)
		body.Action = a.rowURL(tm, key)
		if canWrite = rq.can(ActionUpdate, tm); canWrite {
			body.Submit = "Save"
		}
		if rq.can(ActionDelete, tm) {
			body.DeleteURL = a.deleteURL(tm, key)
		}
	}
	if sub != nil && len(sub.errs) > 0 && body.Error == "" {
		body.Error = "Nothing was saved. Correct the marked fields and try again."
	}

	for _, c := range tm.cols {
		if creating && !c.writable() {
			continue
		}
		var cur value
		if !creating {
			cur = row[c.idx]
			if c.fk != nil && len(c.fk.cols) > 1 {
				cur = compositeFKValue(c.fk, row)
			}
		}
		if sub != nil && sub.sent[c.idx] && c.fk != nil && len(c.fk.cols) > 1 {
			cur = value{Null: sub.null[c.idx], S: sub.text[c.idx]}
		}
		body.Fields = append(body.Fields, a.field(rq, c, creating, canWrite, cur, sub))
	}
	title := body.Heading
	a.render(w, rq, status, "form", title, tm, body)
}

func fieldMeta(c *colMeta) string {
	meta := c.c.DBType
	if meta == "" {
		meta = c.c.Kind.String()
	}
	if c.c.Generated {
		meta += " · generated"
	}
	return meta
}

func joinHelp(base, extra string) string {
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	}
	return base + " " + extra
}

// compositeFKValue is the form value of a composite foreign key: the
// encoded key of the referenced row, or NULL when any part is NULL.
func compositeFKValue(fk *fkMeta, row []value) value {
	parts := make([]string, len(fk.cols))
	for i, sib := range fk.cols {
		if row[sib.idx].Null {
			return value{Null: true}
		}
		parts[i] = row[sib.idx].S
	}
	return value{S: encodeKey(parts)}
}

// field builds one form field.
func (a *Admin) field(rq *request, c *colMeta, creating, canWrite bool, cur value, sub *submission) fieldView {
	n := strconv.Itoa(c.idx)
	f := fieldView{ID: "f" + n, Label: c.label, Meta: fieldMeta(c), Help: c.help}

	viewTarget := c.fk != nil && rq.can(ActionView, c.fk.target)
	if viewTarget && !creating && !cur.Null && c.fk.col != nil {
		f.RefURL, f.RefRow = a.refURL(c.fk, cur.S)
	}

	if !canWrite || !c.writable() || c.fkFollow || (!creating && !c.masked && c.c.Kind != KindBytes && !cur.Null && !representable(cur.S)) {
		f.Control = "static"
		f.Static = showValue(c, cur, 2000)
		if c.c.Kind == KindBytes && !cur.Null {
			f.Static = cell{Text: cur.S, State: "muted"}
			f.Help = joinHelp(c.help, groupDigits(int64(len(cur.S)/2))+" bytes as hexadecimal")
		}
		return f
	}

	// What the control shows: the stored value, or what was submitted.
	text, null := cur.S, cur.Null
	if creating {
		text, null = "", c.c.Nullable && !c.c.HasDefault
	}
	if c.masked {
		text = ""
	}
	if !creating {
		f.OrigName, f.Orig = "o."+n, formFingerprint(c, cur)
	}
	if sub != nil {
		if sub.sent[c.idx] {
			text, null = sub.text[c.idx], sub.null[c.idx] && c.c.Nullable
			if c.masked {
				text = "" // secrets are not echoed back
			}
		}
		if !creating && sub.orig[c.idx] != "" {
			f.Orig = sub.orig[c.idx]
		}
		f.Error = sub.errs[c.idx]
	}
	f.Name, f.Value, f.Null = "v."+n, text, null
	if c.c.Nullable {
		f.NullName = "n." + n
	}
	if creating && c.c.HasDefault {
		if c.c.AutoIncrement || c.c.Default == "" {
			f.Placeholder = "Assigned automatically"
		} else {
			f.Placeholder = "Default: " + truncate(c.c.Default, 60)
		}
	}

	choose := func(opts []option) {
		f.Control = "select"
		f.Options = []option{{Value: "", Label: "—", Selected: null || text == ""}}
		found := null || text == ""
		for _, o := range opts {
			o.Selected = !null && o.Value == text && text != ""
			found = found || o.Selected
			f.Options = append(f.Options, o)
		}
		if !found {
			f.Options = append(f.Options, option{Value: text, Label: text, Selected: true})
		}
	}
	plain := func(vals []string) []option {
		opts := make([]option, len(vals))
		for i, v := range vals {
			opts[i] = option{Value: v, Label: v}
		}
		return opts
	}
	multiline := strings.ContainsAny(text, "\r\n")

	switch {
	case c.masked:
		f.Control, f.Type = "input", "password"
		if !creating && !cur.Null {
			f.Placeholder = "Leave empty to keep the current value"
		}
	case c.c.Kind == KindBytes:
		f.Control = "textarea"
		f.Code = true
		f.Rows = min(max(1+len(text)/48, 2), 12)
		f.Help = joinHelp(f.Help, "Hexadecimal bytes, e.g. 00ff10.")
	case viewTarget && !multiline:
		opts, err := a.options(rq.r.Context(), c.fk, "", fkSelectLimit+1)
		switch {
		case err != nil:
			a.log.WarnContext(rq.r.Context(), "schemadmin: foreign-key options unavailable", slog.String("column", c.c.Name), slog.Any("error", err))
			f.Control, f.Type = "input", "text"
		case len(opts) <= fkSelectLimit:
			choose(opts)
		case c.fk.col != nil:
			f.Control, f.Type = "input", "text"
			f.ListID, f.OptionsURL = "l"+n, a.optionsURL(c.fk.target, c.fk.col)
			if f.Placeholder == "" {
				f.Placeholder = "Type to search " + c.fk.target.label
			}
		default:
			f.Control, f.Type = "input", "text"
			f.Placeholder = "Key of the referenced row"
		}
	case len(c.choices) > 0 && !multiline:
		choose(plain(c.choices))
	case c.c.Kind == KindBool && !multiline:
		choose(plain([]string{"true", "false"}))
	case multiline || c.widget == WidgetTextarea || (c.widget == WidgetAuto && (c.c.Kind == KindText || c.c.Kind == KindJSON)):
		// A text column gets a textarea that starts one line high, so a
		// name looks like a name and a note can still hold line breaks.
		f.Control = "textarea"
		f.Code = c.c.Kind == KindJSON
		lines := 0
		for _, line := range strings.Split(text, "\n") {
			lines += 1 + len(line)/90
		}
		least := 1
		if f.Code || c.widget == WidgetTextarea {
			least = 4
		}
		f.Rows = min(max(lines, least), 16)
	case c.c.Kind == KindDate && (text == "" || isDate(text)):
		f.Control, f.Type = "input", "date"
	default:
		f.Control, f.Type = "input", "text"
		switch c.c.Kind {
		case KindInt:
			f.InputMode = "numeric"
		case KindFloat, KindDecimal:
			f.InputMode = "decimal"
		case KindString:
			f.MaxLength = c.c.MaxLength
		case KindDateTime, KindDateTimeTZ:
			if f.Placeholder == "" {
				f.Placeholder = "YYYY-MM-DD HH:MM:SS"
			}
		case KindTime:
			if f.Placeholder == "" {
				f.Placeholder = "HH:MM:SS"
			}
		}
	}
	return f
}
