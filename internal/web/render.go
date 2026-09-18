package web

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

// static/htmx.min.js is HTMX 2.0.4, vendored so the pod needs no CDN.
//
//go:embed static/*
var staticFS embed.FS

func parsePages() map[string]*template.Template {
	names := []string{"dashboard.html", "plants.html", "detail.html", "form.html", "settings_tado.html", "settings_diagnostics.html"}
	out := make(map[string]*template.Template, len(names))
	for _, name := range names {
		out[name] = template.Must(
			template.New(name).Funcs(template.FuncMap{
				"fieldErr": fieldErr,
				"selected": selectedAttr,
				"checked":  checkedAttr,
			}).ParseFS(templateFS, "templates/layout.html", "templates/fragments.html", "templates/"+name),
		)
	}
	return out
}

func fieldErr(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return m[key]
}

func selectedAttr(got, want string) string {
	if got == want {
		return "selected"
	}
	return ""
}

func checkedAttr(got, want string) string {
	if got == want {
		return "checked"
	}
	return ""
}

type page struct {
	DigestFailed bool
	Nav          string
}

func (s *Server) page(r *http.Request, nav string) page {
	return page{Nav: nav, DigestFailed: s.digestFailed(r.Context())}
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	t := s.pages[name]
	if t == nil {
		slog.Error("unknown template", "page", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("executing template", "page", name, "err", err)
	}
}

func (s *Server) notFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

func (s *Server) internal(w http.ResponseWriter, msg string, err error) {
	slog.Error(msg, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
